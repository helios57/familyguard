package e2e

// FR-28: "Mehr Zeit erbitten". The child asks from the phone; every parent's browser is told by Web
// Push, end to end encrypted; a parent answers in Übersicht; the answer is today's Extrazeit, which
// the phone reads back with the policy.
//
// The push is received by this test, as the push service would receive it, and decrypted with the
// browser's private key (RFC 8291) — so what is asserted is what a parent's phone would show, not
// that a sender function was called.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/hkdf"
)

type timeRequestDTO struct {
	ID             string `json:"id"`
	Minutes        int    `json:"minutes"`
	Note           string `json:"note"`
	State          string `json:"state"`
	GrantedMinutes int    `json:"granted_minutes"`
}

type todayRequestsDTO struct {
	TimeRequests     []timeRequestDTO `json:"time_requests"`
	TimeRequestsLeft int              `json:"time_requests_left"`
}

// pushInbox is a push service: it keeps every message posted to it and answers with status.
type pushInbox struct {
	mu       sync.Mutex
	srv      *httptest.Server
	status   int
	location string
	messages []receivedPush
}

type receivedPush struct {
	Path    string
	Headers http.Header
	Body    []byte
}

func newPushInbox(t *testing.T) *pushInbox {
	in := &pushInbox{status: http.StatusCreated}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		in.messages = append(in.messages, receivedPush{Path: r.URL.Path, Headers: r.Header.Clone(), Body: body})
		status, location := in.status, in.location
		in.mu.Unlock()
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(in.srv.Close)
	return in
}

func (in *pushInbox) host() string { u, _ := url.Parse(in.srv.URL); return u.Host }

func (in *pushInbox) await(t *testing.T, n int) []receivedPush {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		in.mu.Lock()
		got := append([]receivedPush(nil), in.messages...)
		in.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the push service received %d message(s), want %d", len(got), n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// browserKeys is the key material a browser makes when it subscribes.
type browserKeys struct {
	private *ecdh.PrivateKey
	auth    []byte
}

func newBrowserKeys(t *testing.T) browserKeys {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return browserKeys{private: priv, auth: auth}
}

func (k browserKeys) subscription(endpoint string) map[string]any {
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]any{"endpoint": endpoint, "keys": map[string]string{
		"p256dh": enc(k.private.PublicKey().Bytes()), "auth": enc(k.auth),
	}}
}

// decrypt opens an aes128gcm Web Push message (RFC 8188 + RFC 8291) as the browser would.
func (k browserKeys) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("a push body of %d bytes has no header", len(body))
	}
	salt, idlen := body[:16], int(body[20])
	rs := binary.BigEndian.Uint32(body[16:20])
	senderPub := body[21 : 21+idlen]
	ciphertext := body[21+idlen:]
	if rs < 18 || len(ciphertext) > int(rs) {
		t.Fatalf("record size %d for %d bytes of ciphertext: not one record", rs, len(ciphertext))
	}
	peer, err := ecdh.P256().NewPublicKey(senderPub)
	if err != nil {
		t.Fatalf("the key id is not the sender's P-256 key: %v", err)
	}
	secret, err := k.private.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	info := "WebPush: info\x00" + string(k.private.PublicKey().Bytes()) + string(senderPub)
	ikm, err := hkdf.Key(sha256.New, secret, k.auth, info, 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("the push does not decrypt with the browser's keys: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("the last record does not end with the 0x02 delimiter: %q", plain)
	}
	return plain[:len(plain)-1]
}

type pushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
}

func (h *harness) requests(token, childID string) todayRequestsDTO {
	h.t.Helper()
	var out todayRequestsDTO
	h.call(http.MethodGet, "/children/"+childID+"/today", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

func TestAChildAsksForMoreTimeAndAParentAnswersInTheConsole(t *testing.T) {
	inbox := newPushInbox(t)
	h := newHarness(t, withEnv("WEB_PUSH_EXTRA_HOSTS", inbox.host()))
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	// The guardian's browser subscribes; the answer comes from the same role.
	keys := newBrowserKeys(t)
	h.call(http.MethodPut, "/push/subscription", guardian.Token, keys.subscription(inbox.srv.URL+"/push/guardian")).
		expect(http.StatusOK)
	var status struct {
		Subscribed bool `json:"subscribed"`
	}
	h.call(http.MethodPost, "/push/subscription/status", guardian.Token,
		map[string]string{"endpoint": inbox.srv.URL + "/push/guardian"}).expect(http.StatusOK).decode(&status)
	if !status.Subscribed {
		t.Fatal("the subscription the guardian just made is not held")
	}

	// The child asks.
	var asked todayRequestsDTO
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(),
		map[string]any{"minutes": 30, "note": "  Film fertig schauen  "}).expect(http.StatusOK).decode(&asked)
	if len(asked.TimeRequests) != 1 || asked.TimeRequests[0].State != "OPEN" || asked.TimeRequests[0].Minutes != 30 ||
		asked.TimeRequests[0].Note != "Film fertig schauen" || asked.TimeRequestsLeft != 2 {
		t.Fatalf("the phone's answer to its request is %+v", asked)
	}
	// A second tap is the same question.
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).
		expectError(http.StatusConflict, "already_asked")

	// What the guardian's phone shows, decrypted with the browser's key.
	got := inbox.await(t, 1)[0]
	if got.Path != "/push/guardian" || got.Headers.Get("Content-Encoding") != "aes128gcm" ||
		!strings.HasPrefix(got.Headers.Get("Authorization"), "vapid t=") || got.Headers.Get("TTL") == "" {
		t.Errorf("the push was not a signed aes128gcm Web Push: path %s, headers %v", got.Path, got.Headers)
	}
	var shown pushMessage
	if err := json.Unmarshal(keys.decrypt(t, got.Body), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Title != "Mira bittet um 30 Min. mehr" || shown.Body != "Film fertig schauen" || shown.URL != "#/" ||
		shown.Tag != "time-"+f.child.ID {
		t.Errorf("the notification reads %+v", shown)
	}

	// The guardian answers in Übersicht, with less than was asked.
	b := signInBrowser(t, h, guardianIdentity)
	row := `document.querySelector('#view .waiting-card li[data-request="` + asked.TimeRequests[0].ID + `"]')`
	b.waitFor(`!!`+row, 15*time.Second, "the request under Wartet auf dich")
	var text string
	b.eval(row+`.textContent`, &text)
	if !strings.Contains(text, "Mira bittet um 30 min mehr") || !strings.Contains(text, "«Film fertig schauen»") ||
		!strings.Contains(text, "nur heute, bis Mitternacht") {
		t.Errorf("the request reads %q", text)
	}
	var answers []string
	b.eval(`Array.from(`+row+`.querySelectorAll('button[data-answer]')).map((x) => x.dataset.answer)`, &answers)
	if strings.Join(answers, ",") != "30,15,0" {
		t.Errorf("the answers offered are %v, want 30 (as asked), 15 and Nein", answers)
	}
	b.measure(t, "overview with a time request").check(t, "overview with a time request")
	b.eval(row+`.querySelector('button[data-answer="15"]').click()`, nil)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("%s never happened", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	waitFor("the grant to reach the server", func() bool {
		r := h.requests(f.parent.Token, f.child.ID).TimeRequests
		return len(r) == 1 && r[0].State == "GRANTED" && r[0].GrantedMinutes == 15
	})
	// The answer is today's Extrazeit, which is what the phone's engine adds to the limit.
	if d := h.desiredState(f.parent.Token, f.device.ID, ""); d.Desired.BonusMinutes != 15 || d.Desired.QuotaMinutes != 75 {
		t.Errorf("after +15 the day is %d extra, quota %d; want 15 and 75", d.Desired.BonusMinutes, d.Desired.QuotaMinutes)
	}
	var phone struct {
		Today todayRequestsDTO `json:"today"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&phone)
	if len(phone.Today.TimeRequests) != 1 || phone.Today.TimeRequests[0].State != "GRANTED" ||
		phone.Today.TimeRequests[0].GrantedMinutes != 15 {
		t.Errorf("the phone reads its answer as %+v", phone.Today.TimeRequests)
	}
	b.waitFor(`!document.querySelector('#view .waiting-card li[data-request]') &&
	  (document.querySelector('#view .child-card [data-kind="extra"]') || {}).textContent === '+15 min'`,
		10*time.Second, "the request gone and the Extrazeit row saying +15 min")

	// Asked again, and declined: nothing given.
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 60}).expect(http.StatusOK)
	inbox.await(t, 2)
	second := h.requests(f.parent.Token, f.child.ID).TimeRequests[0]
	b.waitFor(`!!document.querySelector('#view li[data-request="`+second.ID+`"] button[data-answer="0"]')`, 15*time.Second, "the second request")
	b.eval(`document.querySelector('#view li[data-request="`+second.ID+`"] button[data-answer="0"]').click()`, nil)
	waitFor("the decline to reach the server", func() bool {
		return h.requests(f.parent.Token, f.child.ID).TimeRequests[0].State == "DECLINED"
	})
	if d := h.desiredState(f.parent.Token, f.device.ID, ""); d.Desired.BonusMinutes != 15 {
		t.Errorf("a decline changed the day's Extrazeit to %d", d.Desired.BonusMinutes)
	}
	// An answer is given once.
	h.call(http.MethodPost, "/children/"+f.child.ID+"/time-requests/"+second.ID+"/decision", f.parent.Token,
		map[string]any{"decision": "grant"}).expectError(http.StatusConflict, "already_decided")

	// Three a day, and then the phone is told so.
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).expect(http.StatusOK)
	third := h.requests(f.parent.Token, f.child.ID).TimeRequests[0]
	h.call(http.MethodPost, "/children/"+f.child.ID+"/time-requests/"+third.ID+"/decision", f.parent.Token,
		map[string]any{"decision": "grant"}).expect(http.StatusOK)
	if d := h.desiredState(f.parent.Token, f.device.ID, ""); d.Desired.BonusMinutes != 30 {
		t.Errorf("a grant with no minutes gave %d in all, want 15 + the 15 asked", d.Desired.BonusMinutes)
	}
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).
		expectError(http.StatusConflict, "no_requests_left")
	if left := h.requests(f.parent.Token, f.child.ID).TimeRequestsLeft; left != 0 {
		t.Errorf("%d requests left after three", left)
	}

	if len(b.pageErrors) != 0 {
		t.Errorf("the console complained: %s", b.pageErrorReport())
	}
}

func TestATimeRequestIsRefusedWhereItCannotMeanAnything(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	// No daily limit: there is no time to ask for.
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 30}).
		expectError(http.StatusConflict, "no_daily_limit")
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60})
	for _, body := range []map[string]any{
		{"minutes": 0}, {"minutes": 4}, {"minutes": 241}, {"minutes": 30, "note": strings.Repeat("ä", 141)},
	} {
		h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), body).expectError(http.StatusBadRequest, "invalid_input")
	}
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 30, "note": strings.Repeat("ä", 140)}).
		expect(http.StatusOK)
	r := h.requests(f.parent.Token, f.child.ID).TimeRequests[0]

	// Another profile's id, and a bad answer.
	other := h.newChild(f.parent.Token, "Nils")
	h.call(http.MethodPost, "/children/"+other.ID+"/time-requests/"+r.ID+"/decision", f.parent.Token,
		map[string]any{"decision": "decline"}).expectError(http.StatusNotFound, "not_found")
	for _, body := range []map[string]any{{"decision": "maybe"}, {"decision": "decline", "minutes": 5}, {"decision": "grant", "minutes": 241}} {
		h.call(http.MethodPost, "/children/"+f.child.ID+"/time-requests/"+r.ID+"/decision", f.parent.Token, body).
			expectError(http.StatusBadRequest, "invalid_input")
	}

	// A push address outside the browsers' push services is refused: it is a parent's input, and
	// the server would send to it from inside the cluster.
	keys := newBrowserKeys(t)
	for _, endpoint := range []string{"http://10.0.0.1/x", "https://kubernetes.default.svc/x", "https://fcm.googleapis.com.evil.example/x"} {
		h.call(http.MethodPut, "/push/subscription", f.parent.Token, keys.subscription(endpoint)).
			expectError(http.StatusBadRequest, "invalid_input")
	}
}

// A browser that unsubscribed is forgotten when its push service says so, and not asked again.
func TestAGoneBrowserIsDroppedFromWebPush(t *testing.T) {
	inbox := newPushInbox(t)
	inbox.status = http.StatusGone
	h := newHarness(t, withEnv("WEB_PUSH_EXTRA_HOSTS", inbox.host()))
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60})
	endpoint := inbox.srv.URL + "/push/gone"
	h.call(http.MethodPut, "/push/subscription", f.parent.Token, newBrowserKeys(t).subscription(endpoint)).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).expect(http.StatusOK)
	inbox.await(t, 1)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status struct {
			Subscribed bool `json:"subscribed"`
		}
		h.call(http.MethodPost, "/push/subscription/status", f.parent.Token, map[string]string{"endpoint": endpoint}).
			expect(http.StatusOK).decode(&status)
		if !status.Subscribed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a subscription its push service called gone (410) is still held")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The console's service worker, in Chrome: a push it receives is a notification a parent sees, and
// it carries the page a tap opens. Delivered through the DevTools protocol, which hands the worker
// the same push event a push service would.
func TestTheConsoleServiceWorkerShowsAPush(t *testing.T) {
	h := newHarness(t)
	b := signInBrowser(t, h, primaryParent)
	b.call("Browser.grantPermissions", map[string]any{"origin": h.base, "permissions": []string{"notifications"}})
	b.call("ServiceWorker.enable", nil)
	var scope string
	b.eval(`navigator.serviceWorker.register('/sw.js', { scope: '/' }).then(() => navigator.serviceWorker.ready).then((r) => r.scope)`, &scope)
	if scope != h.base+"/" {
		t.Fatalf("the service worker controls %q, want the whole console %q", scope, h.base+"/")
	}
	// Chrome reports more than one registration id (measured: "0" and "1"); the console's is the
	// one whose scope is the console.
	registration := ""
	deadline := time.Now().Add(10 * time.Second)
	for registration == "" {
		for id, s := range b.swRegistrations {
			if s == h.base+"/" {
				registration = id
			}
		}
		if registration != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Chrome reported no registration for the console's scope: %v", b.swRegistrations)
		}
		b.eval(`1`, nil) // reads the events that arrived meanwhile
		time.Sleep(100 * time.Millisecond)
	}
	payload, _ := json.Marshal(pushMessage{Title: "Mira bittet um 30 Min. mehr", Body: "Film fertig schauen", Tag: "time-x", URL: "#/"})
	b.call("ServiceWorker.deliverPushMessage", map[string]any{
		"origin": h.base, "registrationId": registration, "data": string(payload),
	})
	b.waitFor(`navigator.serviceWorker.ready.then((r) => r.getNotifications()).then((n) => window.__fgShown = n.map((x) => [x.title, x.body, x.tag, x.data && x.data.url].join('|'))) && (window.__fgShown || []).length > 0`,
		10*time.Second, "the notification")
	var shown []string
	b.eval(`window.__fgShown`, &shown)
	if len(shown) != 1 || shown[0] != "Mira bittet um 30 Min. mehr|Film fertig schauen|time-x|#/" {
		t.Errorf("the notification shown is %v", shown)
	}
}

// Aktivität counts Bonuszeit apart from the day's limit (FR-22.5): seventy minutes, ten of them paid
// from Bonuszeit, are sixty against a sixty-minute limit and ten in gold — never a red bar over the
// limit for a child spending what they earned (the deferred finding of 0.6.34).
func TestAktivitätSaysBonuszeitApartFromTheLimit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"day":     time.Now().In(mustZurich()).Format("2006-01-02"),
		"samples": map[string]int64{pkgGame: 70 * 60 * 1000},
		"earned":  map[string]int64{pkgGame: 10 * 60 * 1000},
	}).expect(http.StatusOK)
	var tl struct {
		Screen struct {
			Counted int `json:"counted_minutes"`
			Earned  int `json:"earned_minutes"`
		} `json:"screen_time"`
	}
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline", f.parent.Token, nil).expect(http.StatusOK).decode(&tl)
	if tl.Screen.Counted != 60 || tl.Screen.Earned != 10 {
		t.Errorf("the day reads %d counted and %d Bonuszeit; want 60 and 10", tl.Screen.Counted, tl.Screen.Earned)
	}

	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="activity"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .st-summary .meter > span')`, 15*time.Second, "the day's bar")
	var got struct {
		Bar    string `json:"bar"`
		Used   string `json:"used"`
		Earned string `json:"earned"`
	}
	b.eval(`({
	  bar: document.querySelector('#view .st-summary .meter > span').className,
	  used: document.querySelector('#view [data-screen="used"]').textContent,
	  earned: (document.querySelector('#view [data-screen="earned"]') || {}).textContent || '',
	})`, &got)
	if got.Bar != "bonus" || got.Used != "1 h von 1 h" || got.Earned != "+ 10 min mit Bonuszeit" {
		t.Errorf("Aktivität draws %+v; want a gold bar, \"1 h von 1 h\" and \"+ 10 min mit Bonuszeit\"", got)
	}
}

// A push service answers; it never sends the server on. An allowed host that answers with a redirect
// would otherwise take the request past the address check, to wherever it pointed.
func TestAPushIsNeverRedirected(t *testing.T) {
	elsewhere := newPushInbox(t)
	inbox := newPushInbox(t)
	inbox.status = http.StatusTemporaryRedirect
	inbox.location = elsewhere.srv.URL + "/inside"
	h := newHarness(t, withEnv("WEB_PUSH_EXTRA_HOSTS", inbox.host()))
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60})
	h.call(http.MethodPut, "/push/subscription", f.parent.Token, newBrowserKeys(t).subscription(inbox.srv.URL+"/push/r")).
		expect(http.StatusOK)
	h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).expect(http.StatusOK)
	inbox.await(t, 1)
	time.Sleep(2 * time.Second)
	elsewhere.mu.Lock()
	followed := len(elsewhere.messages)
	elsewhere.mu.Unlock()
	if followed != 0 {
		t.Errorf("the server followed a push service's redirect %d time(s)", followed)
	}
}
