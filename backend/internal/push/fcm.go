// Package push wakes a phone through Firebase Cloud Messaging (FR-26.3).
//
// What is sent is a wake-up and nothing else: a high-priority data message whose only field is
// {"t":"sync"}. No child, no command, no name — Google carries the message and so sees that this server
// woke this phone, and when; the phone then asks the control plane what changed, exactly as it would
// after a poll. A notification block is never sent: it would put text on the child's screen and in
// Google's hands.
package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ErrUnregistered is FCM saying the installation ID no longer reaches an app: uninstalled, cleared,
// unregistered, or never valid. The caller drops the token; the phone falls back to polling until it reports a new one.
var ErrUnregistered = errors.New("push: the token is no longer registered")

// Options are what a phone needs to register with the same Firebase project (FR-26.3). None of it is
// secret — an Android app's Firebase API key ships inside every app that uses one — and it is handed
// out with the policy rather than compiled in, so the public repository names nobody's project.
type Options struct {
	ProjectID     string `json:"project_id"`
	ApplicationID string `json:"application_id"`
	APIKey        string `json:"api_key"`
	SenderID      string `json:"sender_id"`
}

// Sender sends wake-ups with a service account's credentials.
type Sender struct {
	endpoint string
	project  string
	client   *http.Client
}

const scope = "https://www.googleapis.com/auth/firebase.messaging"

// New builds a sender from a service-account key. endpoint is https://fcm.googleapis.com in
// production and a local server in tests; the token endpoint comes from the key itself.
func New(ctx context.Context, credentials []byte, endpoint string) (*Sender, error) {
	creds, err := google.CredentialsFromJSONWithType(ctx, credentials, google.ServiceAccount, scope)
	if err != nil {
		return nil, fmt.Errorf("push: the credentials are not a service-account key: %w", err)
	}
	if creds.ProjectID == "" {
		return nil, errors.New("push: the service-account key names no project")
	}
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 10 * time.Second
	return &Sender{endpoint: strings.TrimRight(endpoint, "/"), project: creds.ProjectID, client: client}, nil
}

// ProjectID is the Firebase project the key belongs to.
func (s *Sender) ProjectID() string { return s.project }

// Tickle wakes the phone registered as fid, its Firebase Installation ID. FCM's send API addresses a
// phone by `fid` since firebase-messaging 25.1 deprecated the registration token.
func (s *Sender) Tickle(ctx context.Context, fid string) error {
	body, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"fid":  fid,
			"data": map[string]string{"t": "sync"},
			// High priority is what lets the message through Doze. Five minutes to live: a wake-up
			// older than the next poll is worth nothing.
			"android": map[string]any{"priority": "HIGH", "ttl": "300s"},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.endpoint+"/v1/projects/"+s.project+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var fail struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &fail)
	for _, d := range fail.Error.Details {
		// UNREGISTERED is the token gone; INVALID_ARGUMENT on a message this small can only be the
		// token, since everything else in it is fixed.
		if d.ErrorCode == "UNREGISTERED" || d.ErrorCode == "INVALID_ARGUMENT" {
			return fmt.Errorf("%w (%s)", ErrUnregistered, d.ErrorCode)
		}
	}
	return fmt.Errorf("push: FCM answered %d %s", resp.StatusCode, fail.Error.Status)
}

// DecodeCredentials accepts a service-account key as its JSON or as base64 of it — the secret store
// holds it base64, one line, which is the shape an environment variable carries without surprises.
func DecodeCredentials(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		return []byte(raw), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || !json.Valid(decoded) {
		return nil, errors.New("push: the credentials are neither JSON nor base64 of JSON")
	}
	return decoded, nil
}
