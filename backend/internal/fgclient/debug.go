package fgclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DebugUpgrade is the protocol token of a remote adb stream (FR-19). The server refuses a request
// that does not ask for it with 426, before anything reaches the phone.
const DebugUpgrade = "familyguard-debug"

// DebugTarget is which of the phone's two adb services a stream reaches.
type DebugTarget string

const (
	// DebugConnect is adb itself: `adb connect`.
	DebugConnect DebugTarget = "connect"
	// DebugPair is the one-time pairing service: `adb pair`, with the code the phone shows.
	DebugPair DebugTarget = "pair"
)

// DebugStream is one open remote adb stream: bytes written go to the phone's adbd, bytes read come
// from it. Close ends it on both ends.
type DebugStream struct {
	conn net.Conn
	r    io.Reader
}

func (s *DebugStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *DebugStream) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s *DebugStream) Close() error                { return s.conn.Close() }

// OpenDebugStream asks the server to reach deviceID's adbd and returns the stream once the phone
// has dialled back (FR-19.1). port 0 lets the phone find its own port.
//
// A hand-written HTTP/1.1 request on a raw connection rather than net/http's client, for one
// reason: net/http offers HTTP/2 over TLS, and HTTP/2 has no Upgrade — the proxy would answer the
// request as an ordinary GET. ALPN pins http/1.1 here, so what reaches ingress-nginx is the request
// it forwards as an upgrade.
//
// The wait for the phone is the server's (about thirty seconds), so this sets no deadline of its
// own on the response beyond ctx; a refusal comes back as the same *APIError every other call
// returns, with the phone's own sentence in it when the phone gave one.
func (c *Client) OpenDebugStream(ctx context.Context, deviceID string, target DebugTarget, port int) (*DebugStream, error) {
	if c.Token == "" {
		if c.BaseURL == "" {
			return nil, ErrNoServer
		}
		return nil, ErrNoCredential
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("the configured server %q is not an http(s) URL", c.BaseURL)
	}
	query := url.Values{"target": {string(target)}}
	if port > 0 {
		query.Set("port", strconv.Itoa(port))
	}
	path := base.EscapedPath() + "/api/v1/devices/" + url.PathEscape(deviceID) + "/debug?" + query.Encode()

	conn, err := dialServer(ctx, base)
	if err != nil {
		return nil, err
	}
	// Until the 101 arrives, ctx owns the connection: an interrupt while the phone is being
	// reached must not leave a socket open to a server still waiting on its behalf.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	request := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + base.Host + "\r\n" +
		"Authorization: Bearer " + c.Token + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: " + DebugUpgrade + "\r\n" +
		"Accept: application/json\r\n" +
		"User-Agent: fgctl\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sending the debug request to %s: %w", base.Host, err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("reading the server's answer from %s: %w", base.Host, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		conn.Close()
		return nil, apiError(resp.StatusCode, payload)
	}
	var r io.Reader = conn
	if n := reader.Buffered(); n > 0 {
		r = io.MultiReader(io.LimitReader(reader, int64(n)), conn)
	}
	return &DebugStream{conn: conn, r: r}, nil
}

func dialServer(ctx context.Context, base *url.URL) (net.Conn, error) {
	host := base.Hostname()
	port := base.Port()
	if port == "" {
		port = "443"
		if base.Scheme == "http" {
			port = "80"
		}
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	address := net.JoinHostPort(host, port)
	if base.Scheme == "http" {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", address, err)
		}
		return conn, nil
	}
	tlsDialer := &tls.Dialer{
		NetDialer: dialer,
		Config:    &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12},
	}
	conn, err := tlsDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", address, err)
	}
	return conn, nil
}
