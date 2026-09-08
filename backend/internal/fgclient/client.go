// Package fgclient is the parent-side API client shared by the fgctl CLI and its MCP server.
//
// It exists so the two front ends cannot disagree about what an endpoint returns. Both decode into
// the *server's own* types from internal/store, so a field renamed on the server breaks this at
// compile time rather than silently producing an empty column in a table a parent is reading.
package fgclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one FamilyGuard control plane as one parent.
//
// The token is an API key (`fgk_…`) or a session JWT; the server accepts either in the same header,
// so nothing here needs to know which it holds. It is never logged and never included in an error:
// an error string is a thing that ends up in a bug report.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client with a timeout that is short enough to fail a wedged server rather than hang
// a terminal, and long enough for an APK upload on a domestic uplink.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// APIError is a response the server refused, carried whole so a caller can branch on the code
// rather than matching on prose.
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	}
	if e.RequestID != "" {
		return fmt.Sprintf("%s: %s (HTTP %d, request %s)", e.Code, e.Message, e.Status, e.RequestID)
	}
	return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
}

// Unauthorized reports whether the credential was refused, which is the one failure with a specific
// remedy — run `fgctl login` — and so is worth distinguishing from every other error.
func (e *APIError) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// ErrNoCredential is returned before any request is made. A client with no token must not send an
// unauthenticated request that the server answers 401: that reads in a log exactly like a wrong
// key, and it teaches a user to doubt a credential that was never presented.
var ErrNoCredential = errors.New("no credential: run `fgctl login`")

// ErrNoServer is the same argument for the base URL. There is deliberately no compiled-in default
// host: this repository is public, so a binary that reaches for a real server when unconfigured
// would carry that server's address in its source.
var ErrNoServer = errors.New("no server configured: run `fgctl login --url https://guard.example.com`")

// Do performs one request and decodes the body into out, which may be nil to discard it.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if c.Token == "" {
		// Checked before BaseURL is, deliberately: "you are not signed in" is the more useful of
		// the two messages when neither is set.
		if c.BaseURL == "" {
			return ErrNoServer
		}
		return ErrNoCredential
	}
	return c.do(ctx, method, path, body, out, true)
}

// DoAnonymous is Do without a credential, for the routes that deliberately have none.
//
// Only the fgctl manifest and download qualify, and the reason they are unauthenticated is the
// reason this method exists: `fgctl self-update` has to work on a machine whose stored key has been
// revoked, which is precisely when a working binary matters most. Sending the key anyway would be
// harmless -- the server ignores it there -- but it would make a revoked key look like the cause of
// any failure, and it would mean self-update could not run at all before the first login.
func (c *Client) DoAnonymous(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out, false)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, authenticate bool) error {
	if c.BaseURL == "" {
		return ErrNoServer
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}
	if authenticate {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL is included because a typo in the host is the most common cause and the message
		// is otherwise unactionable. The token is not, and net/http does not put headers in this
		// error either -- checked, because "no secret in an error" has to survive wrapping.
		return fmt.Errorf("%s %s: %w", method, c.BaseURL+path, err)
	}
	defer resp.Body.Close()

	// Read the body before branching on status: an error body is JSON too, and a server that
	// returns 500 with prose must not be reported as "unexpected end of JSON input".
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("reading the response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		var envelope struct {
			Error     string `json:"error"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error != "" {
			apiErr.Code, apiErr.Message, apiErr.RequestID = envelope.Error, envelope.Message, envelope.RequestID
		} else {
			// Not the server's envelope at all -- an ingress 502, or an HTML error page. Say so
			// rather than inventing a code, and keep a little of the body: "unexpected response"
			// with nothing in it is the least useful error a CLI can print.
			apiErr.Code = "unexpected_response"
			apiErr.Message = summarise(payload)
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decoding the response from %s: %w", path, err)
	}
	return nil
}

// summarise turns an unexpected body into one short line. Bodies of this kind are HTML often enough
// that returning them whole would fill a terminal with markup.
func summarise(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if text == "" {
		return "empty body"
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}

// Get is the common case, spelled once.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// GetAnonymous is Get with no credential attached. See DoAnonymous.
func (c *Client) GetAnonymous(ctx context.Context, path string, out any) error {
	return c.DoAnonymous(ctx, http.MethodGet, path, nil, out)
}

// Query builds a path with escaped query parameters, skipping empty values so a caller can pass
// optional filters without branching at every call site.
func Query(path string, params map[string]string) string {
	values := url.Values{}
	for key, value := range params {
		if value != "" {
			values.Set(key, value)
		}
	}
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}
