package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGoogle is the two Google endpoints a push touches: the OAuth token endpoint the service account
// signs a JWT for, and FCM itself. Real HTTP, real JWT signing — only the far end is ours.
type fakeGoogle struct {
	srv      *httptest.Server
	mu       sync.Mutex
	sent     []map[string]any
	auth     []string
	answer   func(token string) (int, string)
	tokenHit int
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{answer: func(string) (int, string) { return 200, `{"name":"projects/p/messages/1"}` }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			f.mu.Lock()
			f.tokenHit++
			f.mu.Unlock()
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
				http.Error(w, "not a JWT grant", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"at-1","token_type":"Bearer","expires_in":3600}`)
		case r.URL.Path == "/v1/projects/push-test/messages:send":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.sent = append(f.sent, body)
			f.auth = append(f.auth, r.Header.Get("Authorization"))
			f.mu.Unlock()
			msg, _ := body["message"].(map[string]any)
			token, _ := msg["fid"].(string)
			status, out := f.answer(token)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) serviceAccount(t *testing.T) []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "push-test", "private_key_id": "k1",
		"private_key": pemKey, "client_email": "push@push-test.iam.gserviceaccount.com",
		"client_id": "1", "token_uri": f.srv.URL + "/token",
	})
	return sa
}

func TestATickleIsAnEmptyHighPriorityDataMessage(t *testing.T) {
	f := newFakeGoogle(t)
	s, err := New(context.Background(), f.serviceAccount(t), f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if s.ProjectID() != "push-test" {
		t.Errorf("project %q, want the service account's", s.ProjectID())
	}
	if err := s.Tickle(context.Background(), "tok-A"); err != nil {
		t.Fatalf("tickle: %v", err)
	}
	if len(f.sent) != 1 || f.auth[0] != "Bearer at-1" {
		t.Fatalf("sent %d messages, auth %v", len(f.sent), f.auth)
	}
	msg := f.sent[0]["message"].(map[string]any)
	if msg["fid"] != "tok-A" || msg["token"] != nil {
		t.Errorf("sent to fid=%v token=%v; FCM addresses a phone by its installation ID", msg["fid"], msg["token"])
	}
	if data, _ := msg["data"].(map[string]any); len(data) != 1 || data["t"] != "sync" {
		t.Errorf("the payload is %v; it must say nothing but sync — Google sees it", msg["data"])
	}
	if _, has := msg["notification"]; has {
		t.Error("a notification block would put text on the child's screen and in Google's hands")
	}
	android, _ := msg["android"].(map[string]any)
	if android["priority"] != "HIGH" {
		t.Errorf("priority %v; only a high-priority message wakes a phone in Doze", android["priority"])
	}
}

func TestAnUnregisteredTokenIsSaidSo(t *testing.T) {
	f := newFakeGoogle(t)
	f.answer = func(tok string) (int, string) {
		switch tok {
		case "gone":
			return 404, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`
		case "garbage":
			return 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The registration token is not a valid FCM registration token","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"}]}}`
		default:
			return 503, `{"error":{"code":503,"status":"UNAVAILABLE"}}`
		}
	}
	s, err := New(context.Background(), f.serviceAccount(t), f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"gone", "garbage"} {
		if err := s.Tickle(context.Background(), tok); !errors.Is(err, ErrUnregistered) {
			t.Errorf("%s: %v, want ErrUnregistered", tok, err)
		}
	}
	if err := s.Tickle(context.Background(), "fine-but-fcm-is-down"); err == nil || errors.Is(err, ErrUnregistered) {
		t.Errorf("an FCM outage is %v; it must be an error and must not cost the phone its token", err)
	}
}

func TestCredentialsAreJSONOrBase64OfIt(t *testing.T) {
	raw := `{"type":"service_account"}`
	for _, in := range []string{raw, base64.StdEncoding.EncodeToString([]byte(raw)), "  " + raw + "\n"} {
		got, err := DecodeCredentials(in)
		if err != nil || strings.TrimSpace(string(got)) != raw {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := DecodeCredentials("not json, not base64 !!"); err == nil {
		t.Error("garbage was accepted as credentials")
	}
}
