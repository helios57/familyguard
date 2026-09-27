package agenda

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// MaxCalendarBytes bounds what one calendar may be; a school's year is well under it.
const MaxCalendarBytes = 1 << 20

// ErrLocalAddress is a calendar address that leads to a private, loopback or link-local address.
var ErrLocalAddress = errors.New("the calendar address leads to a private or local network address, which is refused")

// NormalizeCalendarURL checks a calendar address a parent typed (FR-25.1): https, or webcal (read as
// https); plain http only on a bench that allows local fetches.
func NormalizeCalendarURL(raw string, allowLocal bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("the calendar address is not a web address")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "webcal", "webcals":
		u.Scheme = "https"
	case "http":
		if !allowLocal {
			return "", errors.New("the calendar address must be https (or webcal)")
		}
	default:
		return "", errors.New("the calendar address must be https (or webcal)")
	}
	return u.String(), nil
}

// localIP is the address class the fence refuses.
func localIP(ip net.IP) bool {
	cgnat := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || cgnat.Contains(ip)
}

// Fetcher reads calendars over the network, fenced (FR-25.2).
type Fetcher struct {
	client *http.Client
}

// NewFetcher builds the fenced client. The fence is checked on the address actually dialled — after
// DNS, and for every redirect — so neither a DNS answer nor a redirect can lead it into the cluster.
func NewFetcher(allowLocal bool) *Fetcher {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowLocal {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || localIP(ip) {
				return ErrLocalAddress
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		Proxy:                 nil,
	}
	return &Fetcher{client: &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("the calendar address redirects more than three times")
			}
			if _, err := NormalizeCalendarURL(req.URL.String(), allowLocal); err != nil {
				return err
			}
			return nil
		},
	}}
}

// Fetch reads a calendar. The error says what went wrong in words a parent can act on; it never
// repeats the address, which is a credential.
func (f *Fetcher) Fetch(ctx context.Context, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("the calendar address is not a web address")
	}
	req.Header.Set("Accept", "text/calendar, */*;q=0.1")
	req.Header.Set("User-Agent", "FamilyGuard calendar reader")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrLocalAddress) {
			return nil, ErrLocalAddress
		}
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return nil, errors.New("the calendar did not answer within 10 seconds")
		}
		return nil, fmt.Errorf("the calendar could not be reached (%s)", redact(err, address))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the calendar answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCalendarBytes+1))
	if err != nil {
		return nil, fmt.Errorf("the calendar could not be read (%s)", redact(err, address))
	}
	if len(body) > MaxCalendarBytes {
		return nil, errors.New("the calendar is larger than 1 MiB")
	}
	return body, nil
}

// redact removes the address from an error's text: net/http quotes the URL in every error it makes.
func redact(err error, address string) string {
	msg := err.Error()
	if u, perr := url.Parse(address); perr == nil {
		msg = strings.ReplaceAll(msg, address, u.Host)
		msg = strings.ReplaceAll(msg, u.String(), u.Host)
	}
	return msg
}
