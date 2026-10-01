// Package webpush tells parents' browsers about something that waits for them (FR-28.4): a child
// asking for more time, a task reported done. The console subscribes a browser with the Push API;
// the server signs each message with its own VAPID key and sends it, encrypted end to end, to the
// push service that browser names.
//
// The push service's address is a parent's input and the request leaves the cluster, so an address
// is accepted only on the hosts the browsers' push services actually use, over https (see
// AllowedEndpoint). A bench adds its own receiver with Options.ExtraHosts.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/store"
)

// pushHosts are the push services of the browsers a parent uses: Chrome and every Chromium
// browser on Android and desktop (FCM), Firefox (Mozilla autopush), Safari on iPhone and Mac
// (Apple), Edge on Windows (WNS). A suffix match, each starting with a dot or equal to the host.
var pushHosts = []string{
	"fcm.googleapis.com",
	".push.services.mozilla.com",
	".push.apple.com",
	".notify.windows.com",
}

// Store is what the sender reads and writes.
type Store interface {
	WebPushSubscriptions(ctx context.Context) ([]store.WebPushSubscription, error)
	DeleteWebPushSubscription(ctx context.Context, parentID uuid.UUID, endpoint string) error
	MarkWebPushDelivered(ctx context.Context, endpoint string) error
	ServerKey(ctx context.Context, name string, mint func() (string, error)) (string, error)
}

// Message is what a notification shows. Tag collapses: a newer message with the same tag replaces
// the older one on the parent's screen rather than stacking under it.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	// URL is the console page the notification opens, relative to the console ("#/" is Übersicht).
	URL string `json:"url"`
}

// Options configures a Sender.
type Options struct {
	// Subscriber is the VAPID "sub": the console's https address, or a mailto.
	Subscriber string
	// ExtraHosts are host[:port] values accepted besides the push services, over http too. A bench
	// that receives its own pushes sets it; a deployment never does.
	ExtraHosts []string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Sender sends to every subscribed parent browser.
type Sender struct {
	store Store
	opts  Options
}

const (
	keyPrivate = "vapid_private"
	keyPublic  = "vapid_public"
)

// New makes a sender. It does not touch the store until it is first used.
func New(s Store, opts Options) *Sender {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{
			Timeout: 15 * time.Second,
			// A push service answers; it does not send the server on. A redirect would take the
			// request past the address check to wherever the answer pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Sender{store: s, opts: opts}
}

// keys returns the VAPID pair, minting it once. Public and private are minted together: a private
// key whose public half was lost would sign messages no subscription accepts.
func (s *Sender) keys(ctx context.Context) (private, public string, err error) {
	var mintedPublic string
	private, err = s.store.ServerKey(ctx, keyPrivate, func() (string, error) {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		mintedPublic = pub
		return priv, err
	})
	if err != nil {
		return "", "", err
	}
	public, err = s.store.ServerKey(ctx, keyPublic, func() (string, error) {
		if mintedPublic == "" {
			return "", errors.New("webpush: a private VAPID key exists without its public half")
		}
		return mintedPublic, nil
	})
	return private, public, err
}

// PublicKey is the application server key a browser subscribes with.
func (s *Sender) PublicKey(ctx context.Context) (string, error) {
	_, public, err := s.keys(ctx)
	return public, err
}

// AllowedEndpoint reports whether an address is one a push is sent to.
func (s *Sender) AllowedEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	for _, extra := range s.opts.ExtraHosts {
		if extra != "" && strings.EqualFold(u.Host, extra) && (u.Scheme == "http" || u.Scheme == "https") {
			return true
		}
	}
	if u.Scheme != "https" || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pushHosts {
		if host == strings.TrimPrefix(h, ".") || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

// Notify sends m to every subscribed browser and returns how many push services accepted it. A
// subscription the push service says is gone (404, 410) is dropped; any other failure is logged and
// kept, because a push service having a bad minute is not a browser that unsubscribed.
func (s *Sender) Notify(ctx context.Context, m Message) (int, error) {
	subs, err := s.store.WebPushSubscriptions(ctx)
	if err != nil || len(subs) == 0 {
		return 0, err
	}
	private, public, err := s.keys(ctx)
	if err != nil {
		return 0, fmt.Errorf("webpush keys: %w", err)
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, sub := range subs {
		if !s.AllowedEndpoint(sub.Endpoint) {
			s.opts.Logger.Warn("webpush: skipped an address outside the push services", "parent", sub.ParentID)
			continue
		}
		res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, &webpush.Options{
			HTTPClient:      s.opts.HTTPClient,
			Subscriber:      s.opts.Subscriber,
			VAPIDPublicKey:  public,
			VAPIDPrivateKey: private,
			// An answer that waits an hour is still worth having; one that waits a day is about a
			// day that is over.
			TTL:     6 * 60 * 60,
			Urgency: webpush.UrgencyHigh,
		})
		if err != nil {
			s.opts.Logger.Warn("webpush: not delivered", "parent", sub.ParentID, "error", err)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		switch {
		case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
			if err := s.store.DeleteWebPushSubscription(ctx, uuid.Nil, sub.Endpoint); err != nil {
				s.opts.Logger.Error("webpush: could not drop a gone subscription", "error", err)
			}
			s.opts.Logger.Info("webpush: the browser unsubscribed; dropped it", "parent", sub.ParentID)
		case res.StatusCode >= 200 && res.StatusCode < 300:
			delivered++
			if err := s.store.MarkWebPushDelivered(ctx, sub.Endpoint); err != nil {
				s.opts.Logger.Error("webpush: could not record a delivery", "error", err)
			}
		default:
			s.opts.Logger.Warn("webpush: refused", "parent", sub.ParentID, "status", res.StatusCode)
		}
	}
	return delivered, nil
}
