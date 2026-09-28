package e2e

// FR-26.3: a resting phone is woken by a push. The server under test is the real binary, configured
// with a real service-account key signed by a key this test generates; Google's two endpoints — OAuth
// and FCM — are served by this process, because the thing measured here is what the server sends and
// when, not Google. The real FCM is exercised on the emulator (tests/android/push.sh).

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"
)

type fakeFCM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	tokens []string
	bodies []map[string]any
	refuse map[string]bool
}

func newFakeFCM(t *testing.T) *fakeFCM {
	f := &fakeFCM{refuse: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msg, _ := body["message"].(map[string]any)
		token, _ := msg["fid"].(string)
		f.mu.Lock()
		f.tokens = append(f.tokens, token)
		f.bodies = append(f.bodies, msg)
		refused := f.refuse[token]
		f.mu.Unlock()
		if refused {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/e2e-push/messages/1"}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFCM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

func (f *fakeFCM) serviceAccount(t *testing.T) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "e2e-push", "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "push@e2e-push.iam.gserviceaccount.com", "client_id": "1",
		"token_uri": f.srv.URL + "/token",
	})
	return string(sa)
}

func TestAServerWithoutPushSaysSoToThePhone(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	var raw map[string]json.RawMessage
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&raw)
	if p, ok := raw["push"]; !ok || string(p) != "null" {
		t.Errorf("a server with no push key sends push=%s; the phone must read that as 'poll every 5 minutes'", p)
	}
}

func TestARestingPhoneIsWokenByAPushAndOnlyThen(t *testing.T) {
	fcm := newFakeFCM(t)
	h := newHarness(t,
		withEnv("FCM_CREDENTIALS", fcm.serviceAccount(t)),
		withEnv("FCM_APPLICATION_ID", "1:42:android:abc"),
		withEnv("FCM_API_KEY", "AIza-not-secret"),
		withEnv("FCM_SENDER_ID", "42"),
		withEnv("FCM_ENDPOINT", fcm.srv.URL))
	f := enrolledFixture(t, h)
	beat := func(token string) {
		h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{"connectivity": "wifi", "push_token": token}).
			expect(http.StatusOK)
	}
	waitPushes := func(n int, within time.Duration) {
		t.Helper()
		deadline := time.Now().Add(within)
		for fcm.count() < n && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}

	var policy struct {
		Push *struct {
			ProjectID     string `json:"project_id"`
			ApplicationID string `json:"application_id"`
			APIKey        string `json:"api_key"`
			SenderID      string `json:"sender_id"`
		} `json:"push"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&policy)
	if policy.Push == nil || policy.Push.ProjectID != "e2e-push" || policy.Push.ApplicationID != "1:42:android:abc" ||
		policy.Push.APIKey != "AIza-not-secret" || policy.Push.SenderID != "42" {
		t.Fatalf("the phone is told push=%+v", policy.Push)
	}

	// No token yet: an event wakes nobody.
	h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)
	time.Sleep(time.Second)
	if n := fcm.count(); n != 0 {
		t.Fatalf("%d pushes to a phone with no token", n)
	}

	// A new address is pushed at once, even with nothing to say: the phone stretches its poll to 30
	// minutes only once a push has arrived, and an event it was not woken for would not count.
	beat("tok-1")
	if !h.deviceView(f.parent.Token, f.device.ID).State.PushRegistered {
		t.Error("the phone reported a token and the view says it has none")
	}
	var listed struct {
		Devices []deviceViewDTO `json:"devices"`
	}
	h.call(http.MethodGet, "/devices", f.parent.Token, nil).expect(http.StatusOK).decode(&listed)
	if len(listed.Devices) != 1 || listed.Devices[0].State == nil || !listed.Devices[0].State.PushRegistered {
		t.Errorf("the device list does not show the phone as reachable by push: %+v", listed.Devices)
	}
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-push")}
	if r := fgctlRun(t, t.TempDir(), env, "device", f.device.ID); r.code != 0 ||
		!regexp.MustCompile(`reachable by push\s+true`).MatchString(r.stdout) {
		t.Errorf("fgctl device does not say the phone is reachable by push: exit %d, %q", r.code, r.stdout)
	}
	waitPushes(1, 5*time.Second)
	if n := fcm.count(); n != 1 {
		t.Fatalf("a new push address was pushed %d times, want 1", n)
	}
	fcm.mu.Lock()
	msg := fcm.bodies[0]
	fcm.mu.Unlock()
	if msg["fid"] != "tok-1" || msg["token"] != nil {
		t.Errorf("pushed to fid=%v token=%v", msg["fid"], msg["token"])
	}
	if data, _ := msg["data"].(map[string]any); len(data) != 1 || data["t"] != "sync" {
		t.Errorf("the push carries %v; it must carry nothing but a wake-up", msg["data"])
	}

	// The same address again is no news.
	time.Sleep(10500 * time.Millisecond) // past the coalescing window, so only the rule is measured
	beat("tok-1")
	time.Sleep(1500 * time.Millisecond)
	if n := fcm.count(); n != 1 {
		t.Fatalf("an unchanged push address was pushed again: %d pushes", n)
	}

	h.issueCommand(f.parent.Token, f.device.ID, "LOCATE_NOW", nil)
	waitPushes(2, 5*time.Second)
	if n := fcm.count(); n != 2 {
		t.Fatalf("a command for a resting phone with a token sent %d pushes, want 1 more", n-1)
	}

	// A burst is one push: the phone syncs everything in one go.
	h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)
	h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)
	time.Sleep(2 * time.Second)
	if n := fcm.count(); n != 2 {
		t.Errorf("a burst of commands inside 10 s sent %d more pushes, want none", n-2)
	}

	// A phone that holds its stream hears the event there, and is not pushed.
	time.Sleep(10 * time.Second)
	stream := h.openStream("/device/stream", f.deviceToken())
	deadline := time.Now().Add(5 * time.Second)
	for !h.deviceView(f.parent.Token, f.device.ID).StreamOpen && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)
	time.Sleep(2 * time.Second)
	if n := fcm.count(); n != 2 {
		t.Errorf("a phone holding its stream was pushed anyway: %d pushes", n)
	}
	stream.Close()

	// FCM says the token is gone: the server drops it, and the phone polls until it reports another.
	fcm.mu.Lock()
	fcm.refuse["tok-1"] = true
	fcm.mu.Unlock()
	deadline = time.Now().Add(5 * time.Second)
	for h.deviceView(f.parent.Token, f.device.ID).StreamOpen && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	h.issueCommand(f.parent.Token, f.device.ID, "SYNC_POLICY", nil)
	waitPushes(3, 5*time.Second)
	deadline = time.Now().Add(5 * time.Second)
	for h.deviceView(f.parent.Token, f.device.ID).State.PushRegistered && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if h.deviceView(f.parent.Token, f.device.ID).State.PushRegistered {
		t.Error("FCM said the token is unregistered and the server kept it")
	}

	// "" clears; absent leaves.
	beat("tok-2")
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if !h.deviceView(f.parent.Token, f.device.ID).State.PushRegistered {
		t.Error("a heartbeat without push_token cleared the token")
	}
	beat("")
	if h.deviceView(f.parent.Token, f.device.ID).State.PushRegistered {
		t.Error(`push_token "" did not clear the token`)
	}
}
