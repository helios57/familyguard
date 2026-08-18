package e2e

// The harness starts the real server binary against a real PostgreSQL database and talks to it over
// a real socket. Nothing here reaches inside the server: every fact the suite asserts is a fact a
// third-party client could observe.
//
// Two rules shape the whole file:
//
//   - A precondition that is missing is reported as "NOT MEASURED" and exits 2. A suite that cannot
//     start and exits 0 is the most expensive kind of green, because it looks exactly like coverage.
//   - Each test gets its own database and its own server process. Tests that share a database pass
//     or fail depending on the order the runner happened to choose, and the first flake is then
//     debugged as if it were a product bug.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	// The OAuth client this deployment is configured with. Every parent ID token must carry it as
	// its audience, so it is also what the negative controls vary.
	e2eClientID     = "family-guard-e2e.apps.example.test"
	e2eClientSecret = "e2e-client-secret-not-a-real-credential"
	// Long enough to satisfy config.MinSessionKeyBytes, written out rather than imported: the suite
	// must fail if the server lowers that bound, not silently agree with it.
	e2eSessionKey = "e2e-session-signing-key-0123456789-abcdef"

	startTimeout = 30 * time.Second
	stopTimeout  = 25 * time.Second
	callTimeout  = 20 * time.Second
)

// The environment run.sh hands over. Read once, in TestMain, so a missing one is reported before a
// single test has had the chance to fail for the wrong reason.
var (
	serverBin   string
	pgContainer string
	pgHost      string
	pgPort      string
	pgUser      string
	pgPassword  string
	// The browser the rendered-layout guard drives. A stated precondition rather than an optional
	// extra: without it the suite would still report PASS having never laid a page out, which is
	// the exact shape of coverage that is not there.
	chromeBin string

	dbSeq atomic.Int64
)

// The people in the fixtures. Two parents so the roles are distinguishable, and a stranger who is
// authenticated by the identity provider and still must never get in.
var (
	primaryParent = identity{Email: "primary@family.test", Subject: "google-primary", Name: "Primary Parent", Verified: true}
	secondParent  = identity{Email: "second@family.test", Subject: "google-second", Name: "Second Parent", Verified: true}
	stranger      = identity{Email: "stranger@example.test", Subject: "google-stranger", Name: "Not A Parent", Verified: true}
)

func TestMain(m *testing.M) {
	var missing []string
	read := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}
	serverBin = read("E2E_SERVER_BIN")
	pgContainer = read("E2E_PG_CONTAINER")
	pgHost = read("E2E_PG_HOST")
	pgPort = read("E2E_PG_PORT")
	pgUser = read("E2E_PG_USER")
	pgPassword = read("E2E_PG_PASSWORD")
	chromeBin = read("E2E_CHROME")

	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"NOT MEASURED: this suite is started by tests/e2e/run.sh, which builds the server and owns\n"+
				"the database container. Missing: %s\n", strings.Join(missing, ", "))
		os.Exit(2)
	}
	if _, err := os.Stat(serverBin); err != nil {
		fmt.Fprintf(os.Stderr, "NOT MEASURED: E2E_SERVER_BIN %q: %v\n", serverBin, err)
		os.Exit(2)
	}
	if _, err := os.Stat(chromeBin); err != nil {
		fmt.Fprintf(os.Stderr, "NOT MEASURED: E2E_CHROME %q: %v\n", chromeBin, err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// ---- harness --------------------------------------------------------------

type harness struct {
	t      *testing.T
	issuer *issuer

	base   string // http://127.0.0.1:<port>
	port   int
	dbName string

	env map[string]string

	// The APK signing certificate the provisioning payload publishes a checksum of. The bytes are
	// known here, so the expected checksum is computed by the test rather than copied from the
	// response it is supposed to be checking.
	apkCertPath string
	apkCertSum  string

	// apkPath is set by withSelfHostedAPK. Held on the harness rather than read back out of the
	// environment because bind() needs it: the download URL carries the port, and start() rebinds
	// on a lost port race.
	apkPath string

	client       *http.Client
	streamClient *http.Client

	mu      sync.Mutex
	cmd     *exec.Cmd
	logs    *logCapture
	exited  chan struct{}
	running bool
}

type harnessOption func(*harness)

// withEnv overrides one configuration value for this server. Tests that need a specific limit say
// so out loud rather than depending on a default that may change.
func withEnv(key, value string) harnessOption {
	return func(h *harness) { h.env[key] = value }
}

// withoutEnv removes a variable the defaults set, for the tests that prove a deployment which is
// missing it is refused rather than degraded.
func withoutEnv(key string) harnessOption {
	return func(h *harness) { delete(h.env, key) }
}

// withSelfHostedAPK configures the deployment shape the cluster manifests actually describe: the
// server holds the DPC on disk and publishes its own download URL, so the checksum in the QR and
// the bytes the phone receives come from one file. The default harness instead points APK_URL at
// an external host, which is the other supported shape and cannot check that correspondence.
func withSelfHostedAPK(path string) harnessOption {
	return func(h *harness) {
		h.apkPath = path
		h.env["APK_PATH"] = path
		h.bind(h.port)
	}
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()

	h := &harness{
		t:      t,
		issuer: newIssuer(t),
		dbName: newDatabase(t),
		client: &http.Client{
			Timeout: callTimeout,
			// Redirects are the thing under test in the OAuth flow, so nothing follows them
			// automatically anywhere in this suite.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		streamClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}

	h.apkCertPath, h.apkCertSum = writeAPKCert(t)

	h.env = map[string]string{
		// ADDR and PUBLIC_URL are both written by bind(), below, and never here — PUBLIC_URL drives
		// the OAuth redirect_uri, so a port set in one place and not the other sends the browser to
		// a dead address and the failure reads as the identity provider's.
		"DATABASE_URL": databaseURL(h.dbName),

		"OIDC_ISSUER":         h.issuer.URL(),
		"OIDC_JWKS_URL":       h.issuer.JWKSURL(),
		"OAUTH_AUTH_URL":      h.issuer.AuthURL(),
		"OAUTH_TOKEN_URL":     h.issuer.TokenURL(),
		"OAUTH_CLIENT_ID":     e2eClientID,
		"OAUTH_CLIENT_SECRET": e2eClientSecret,
		"SESSION_SIGNING_KEY": e2eSessionKey,

		"BOOTSTRAP_PARENT_EMAILS": primaryParent.Email + "," + secondParent.Email,
		"FAMILY_NAME":             "E2E Family",

		"APK_URL":       "https://apk.example.test/familyguard.apk",
		"APK_CERT_PATH": h.apkCertPath,

		// The rate limiter has its own test, which sets this low deliberately. Leaving the default
		// here would make every long journey race a limit that is not what it is measuring — and the
		// resulting 429 would be reported as a product bug.
		"RATE_LIMIT_PER_MINUTE": "6000",
		"LOG_LEVEL":             "info",
	}
	h.bind(freePort(t))
	for _, opt := range opts {
		opt(h)
	}

	t.Cleanup(h.stop)
	t.Cleanup(func() {
		if t.Failed() {
			h.dumpLogs()
		}
	})
	h.start()
	return h
}

// startAttempts bounds the retries described on start(). Three, not "until it works": a port that
// is occupied by something permanent must still fail the test rather than spin.
const startAttempts = 3

// start launches the server and does not return until it has proven it is serving.
//
// It retries a failure to bind, and only that one. freePort asks the kernel for a port and hands it
// straight back, so between the close and the child's bind the port belongs to nobody — and this
// suite opens hundreds of loopback connections, whose source ports come out of the same ephemeral
// range. Losing the race is rare and entirely silent when it happens: the server exits with
// "address already in use" and the failure surfaces as a test that has nothing to do with ports.
// One such red appeared in a full four-layer run and did not reproduce in five more, which is
// exactly the shape of this race and exactly the shape of a red nobody can chase.
//
// The retry is narrow on purpose. Any other startup failure — a bad DSN, a missing key, a migration
// error — is fatal on the first attempt, because retrying those would turn a deterministic product
// failure into a slow, intermittent one. The retry is also announced through t.Logf, so a run that
// needed it says so; a silent retry would hide a port that is permanently taken.
func (h *harness) start() {
	h.t.Helper()

	for attempt := 1; ; attempt++ {
		err := h.tryStart()
		if err == nil {
			return
		}
		if attempt >= startAttempts || !strings.Contains(err.Error(), "address already in use") {
			h.t.Fatalf("%v", err)
		}
		h.t.Logf("port %d was taken between reserving it and binding it; retrying on a new port "+
			"(attempt %d of %d)", h.port, attempt+1, startAttempts)
		h.bind(freePort(h.t))
	}
}

// bind points the harness, the client base URL and the server's environment at one port together.
// They were three separate assignments, which is a drift waiting to happen: PUBLIC_URL drives the
// OAuth redirect_uri, so a port that is updated in two of the three places produces a sign-in
// failure that looks like the identity provider's.
func (h *harness) bind(port int) {
	h.port = port
	h.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	h.env["ADDR"] = fmt.Sprintf("127.0.0.1:%d", port)
	h.env["PUBLIC_URL"] = h.base
	// A server that hosts the DPC publishes its own download URL, and that URL carries the port.
	// Derived here for the same reason as PUBLIC_URL: a rebind that moved two of the three values
	// would leave the QR pointing at a dead port while still looking entirely correct.
	if h.apkPath != "" {
		h.env["APK_URL"] = h.base + apkDownloadPath
	}
}

// The path the server serves the DPC from. Written out rather than imported from the backend
// module: this suite must fail if the route moves, not silently follow it.
const apkDownloadPath = "/dpc.apk"

// tryStart is one attempt. It returns an error for a server that exited while starting — the only
// failure start() is willing to retry — and calls Fatalf for everything else.
func (h *harness) tryStart() error {
	h.t.Helper()

	logs := &logCapture{}
	cmd := exec.Command(serverBin)
	// A clean environment, never os.Environ(): an ambient DATABASE_URL or OAUTH_CLIENT_ID on the
	// developer's machine would silently change what is under test, and the run that finds it is
	// the one that cannot be reproduced anywhere else.
	cmd.Env = envSlice(h.env)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		h.t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("start %s: %v", serverBin, err)
	}

	listening := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); logs.consume(stdout, listening) }()
	go func() { defer wg.Done(); logs.consume(stderr, nil) }()

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		wg.Wait()
		close(exited)
	}()

	h.mu.Lock()
	h.cmd, h.logs, h.exited, h.running = cmd, logs, exited, true
	h.mu.Unlock()

	deadline := time.After(startTimeout)
	var addr string
	select {
	case addr = <-listening:
	case <-exited:
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
		return fmt.Errorf("the server exited during startup:\n%s", logs.String())
	case <-deadline:
		h.t.Fatalf("the server never logged that it was listening within %s:\n%s", startTimeout, logs.String())
	}

	// The announced address is checked against the one we asked for. The server binds before it
	// logs, so this line is also the standing calibration of that ordering: if it ever goes back to
	// announcing first, a port collision shows up here as a mismatch instead of as a mystery.
	if _, port, err := net.SplitHostPort(addr); err != nil {
		h.t.Fatalf("server logged an unparseable address %q: %v", addr, err)
	} else if port != strconv.Itoa(h.port) {
		h.t.Fatalf("server announced port %s but was asked for %d", port, h.port)
	}

	h.waitReady()
	return nil
}

// waitReady polls /readyz, which reports the database connection rather than the process.
func (h *harness) waitReady() {
	h.t.Helper()
	deadline := time.Now().Add(startTimeout)
	var last string
	for time.Now().Before(deadline) {
		resp, err := h.client.Get(h.base + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = fmt.Sprintf("status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
		} else {
			last = err.Error()
		}
		select {
		case <-h.exited:
			h.t.Fatalf("the server exited before it was ready:\n%s", h.logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	h.t.Fatalf("/readyz never answered 200 within %s (last: %s)\n%s", startTimeout, last, h.logs.String())
}

// stop asks the server to shut down the way Kubernetes does, and fails the test if it does not.
func (h *harness) stop() {
	h.mu.Lock()
	cmd, exited, running := h.cmd, h.exited, h.running
	h.running = false
	h.mu.Unlock()
	if !running || cmd == nil || cmd.Process == nil {
		return
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		h.t.Errorf("signal the server: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(stopTimeout):
		_ = cmd.Process.Kill()
		<-exited
		// Not a warning. The deployment's terminationGracePeriodSeconds is finite, so a server that
		// ignores SIGTERM is a server that gets killed mid-response in production.
		h.t.Errorf("the server did not exit within %s of SIGTERM; it was killed", stopTimeout)
	}
}

// restart stops and starts the same server against the same database. The tests that use it are
// asserting persistence, so nothing else may change — same port, same environment, same rows.
func (h *harness) restart() {
	h.t.Helper()
	h.stop()
	h.start()
}

func (h *harness) dumpLogs() {
	h.mu.Lock()
	logs := h.logs
	h.mu.Unlock()
	if logs == nil {
		return
	}
	h.t.Logf("---- server log (%s) ----\n%s", h.dbName, logs.String())
}

// ---- log capture ----------------------------------------------------------

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

// consume reads one line at a time and, for stdout, reports the address from the "listening" line.
func (l *logCapture) consume(r io.Reader, listening chan<- string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		l.mu.Lock()
		l.lines = append(l.lines, line)
		l.mu.Unlock()

		if listening == nil {
			continue
		}
		var rec struct {
			Msg  string `json:"msg"`
			Addr string `json:"addr"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "listening" {
			select {
			case listening <- rec.Addr:
			default:
			}
		}
	}
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// contains reports whether any captured line contains sub. Used to assert on what the server said
// about a request it answered with a deliberately vague body.
func (l *logCapture) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

// ---- HTTP ------------------------------------------------------------------

type apiResponse struct {
	t      *testing.T
	method string
	path   string

	Status int
	Header http.Header
	Body   []byte
}

// expect fails the test unless the status matches, printing the body — which is where the server
// says what was wrong.
func (r apiResponse) expect(status int) apiResponse {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("%s %s: expected %d, got %d\nbody: %s", r.method, r.path, status, r.Status, r.Body)
	}
	return r
}

func (r apiResponse) decode(dst any) apiResponse {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		r.t.Fatalf("%s %s: decode %T: %v\nbody: %s", r.method, r.path, dst, err, r.Body)
	}
	return r
}

// errorCode returns the machine-readable half of an error body, or "" if this was not one.
func (r apiResponse) errorCode() string {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Error
}

func (r apiResponse) expectError(status int, code string) apiResponse {
	r.t.Helper()
	r.expect(status)
	if got := r.errorCode(); got != code {
		r.t.Fatalf("%s %s: expected error code %q, got %q\nbody: %s", r.method, r.path, code, got, r.Body)
	}
	return r
}

// call performs one API request. body may be nil, a []byte or string (sent verbatim, which is how
// malformed payloads are tested), or any value that marshals to JSON.
func (h *harness) call(method, path, token string, body any) apiResponse {
	h.t.Helper()
	return h.send(h.newRequest(method, path, token, body))
}

func (h *harness) newRequest(method, path, token string, body any) *http.Request {
	h.t.Helper()
	var reader io.Reader
	hasBody := false
	switch v := body.(type) {
	case nil:
	case []byte:
		reader, hasBody = bytes.NewReader(v), true
	case string:
		reader, hasBody = strings.NewReader(v), true
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			h.t.Fatalf("marshal request body: %v", err)
		}
		reader, hasBody = bytes.NewReader(encoded), true
	}

	req, err := http.NewRequest(method, h.url(path), reader)
	if err != nil {
		h.t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func (h *harness) send(req *http.Request) apiResponse {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("%s %s: read body: %v", req.Method, req.URL.Path, err)
	}
	return apiResponse{
		t:      h.t,
		method: req.Method,
		path:   req.URL.Path,
		Status: resp.StatusCode,
		Header: resp.Header.Clone(),
		Body:   payload,
	}
}

// url resolves a suite-relative path. Anything starting with /api or a bare "/" is taken as is;
// everything else is relative to the versioned API prefix, which keeps the tests readable.
func (h *harness) url(path string) string {
	switch {
	case strings.HasPrefix(path, "http://"), strings.HasPrefix(path, "https://"):
		return path
	case strings.HasPrefix(path, "/api/"), path == "/", strings.HasPrefix(path, "/healthz"),
		strings.HasPrefix(path, "/readyz"), strings.HasPrefix(path, "/index.html"),
		strings.HasPrefix(path, "/app."), strings.HasPrefix(path, "/manifest"),
		strings.HasPrefix(path, "/icon."):
		return h.base + path
	default:
		return h.base + "/api/v1" + path
	}
}

// ---- sign-in ---------------------------------------------------------------

type parentDTO struct {
	ID          string `json:"id"`
	FamilyID    string `json:"family_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at"`
	// Deliberately present so the suite can assert they are absent. google_sub is json:"-" on the
	// server and frp_account does not exist there at all (FR-2.3) — a client that could read either
	// would be reading a credential-adjacent identifier out of an ordinary parent listing, and this
	// is the assertion that notices if one is ever added back.
	GoogleSub  string `json:"google_sub"`
	FRPAccount string `json:"frp_account"`
}

type sessionDTO struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Parent    parentDTO `json:"parent"`
}

// signIn takes the ID-token route: the caller already holds a token from the provider. This is the
// path the console does not use, and it is the one that lets a test hold a token it can then vary.
func (h *harness) signIn(who identity) sessionDTO {
	h.t.Helper()
	return h.signInWithToken(h.issuer.validToken(e2eClientID, who))
}

func (h *harness) signInWithToken(token string) sessionDTO {
	h.t.Helper()
	var out sessionDTO
	h.call(http.MethodPost, "/auth/google", "", map[string]string{"id_token": token}).
		expect(http.StatusOK).
		decode(&out)
	if out.Token == "" {
		h.t.Fatal("sign-in returned an empty session token")
	}
	return out
}

// browserSignIn drives the whole authorization-code flow the way a browser would: follow every
// redirect by hand, carry the cookies, and read the result out of the fragment of the final hop.
//
// Following redirects manually is the point. The interesting hop is the one back to the console,
// and http.Client would silently swallow it into a request for index.html.
func (h *harness) browserSignIn(who identity) url.Values {
	h.t.Helper()
	h.issuer.setNextLogin(who)

	// The jar is keyed by host, not by name. A flat jar would attach the handshake cookie — which
	// carries the PKCE verifier and the nonce — to the hops that go to the identity provider, which
	// is precisely the leak the cookie's scoping exists to prevent. Getting that wrong here would
	// also make the suite unable to notice if the server ever stopped scoping it.
	jar := map[string]map[string]string{}
	next := h.base + "/api/v1/auth/google/start"

	for hop := 0; hop < 6; hop++ {
		req, err := http.NewRequest(http.MethodGet, next, nil)
		if err != nil {
			h.t.Fatalf("hop %d: %v", hop, err)
		}
		for name, value := range jar[req.URL.Host] {
			req.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		resp, err := h.client.Do(req)
		if err != nil {
			h.t.Fatalf("hop %d (%s): %v", hop, next, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		host := req.URL.Host
		for _, c := range resp.Cookies() {
			if jar[host] == nil {
				jar[host] = map[string]string{}
			}
			if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
				delete(jar[host], c.Name)
				continue
			}
			jar[host][c.Name] = c.Value
		}

		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			h.t.Fatalf("hop %d (%s): expected a redirect, got %d", hop, next, resp.StatusCode)
		}
		location, err := resp.Location()
		if err != nil {
			h.t.Fatalf("hop %d (%s): no Location header: %v", hop, next, err)
		}
		// The console is served from the root, and the sign-in result rides in the fragment so it
		// never reaches a proxy log or a Referer header.
		if location.Path == "/" || location.Path == "" {
			result, err := url.ParseQuery(location.Fragment)
			if err != nil {
				h.t.Fatalf("final redirect fragment %q is not parseable: %v", location.Fragment, err)
			}
			return result
		}
		next = location.String()
	}
	h.t.Fatal("the browser sign-in flow did not settle within six redirects")
	return nil
}

// ---- server-sent events ----------------------------------------------------

type sseEvent struct {
	Type string
	Data string
}

type sseStream struct {
	t      *testing.T
	cancel context.CancelFunc
	body   io.ReadCloser
	events chan sseEvent
	fail   chan error
	once   sync.Once
}

// openStream connects an event stream and waits for the hello frame, so a caller that publishes
// immediately afterwards knows the subscription is registered. A test that publishes into an empty
// hub and then asserts nothing arrived would pass whether or not the hub works.
func (h *harness) openStream(path, token string) *sseStream {
	h.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url(path), nil)
	if err != nil {
		cancel()
		h.t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.streamClient.Do(req)
	if err != nil {
		cancel()
		h.t.Fatalf("open stream %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		h.t.Fatalf("open stream %s: status %d: %s", path, resp.StatusCode, payload)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		resp.Body.Close()
		cancel()
		h.t.Fatalf("open stream %s: content type %q", path, got)
	}

	s := &sseStream{
		t:      h.t,
		cancel: cancel,
		body:   resp.Body,
		events: make(chan sseEvent, 32),
		fail:   make(chan error, 1),
	}
	go s.read()
	h.t.Cleanup(s.Close)

	if hello := s.next(10 * time.Second); hello.Type != "connected" {
		h.t.Fatalf("stream %s: first frame was %q, expected \"connected\"", path, hello.Type)
	}
	return s
}

func (s *sseStream) read() {
	scanner := bufio.NewScanner(s.body)
	scanner.Buffer(make([]byte, 0, 8*1024), 256*1024)
	var current sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.Type != "" || current.Data != "" {
				select {
				case s.events <- current:
				default:
				}
			}
			current = sseEvent{}
		case strings.HasPrefix(line, ":"):
			// A keep-alive comment. Not an event, deliberately: a client that treated it as one
			// would report activity every twenty seconds forever.
		case strings.HasPrefix(line, "event: "):
			current.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case s.fail <- err:
		default:
		}
	}
	close(s.events)
}

// next waits for one event and fails the test if none arrives in time.
func (s *sseStream) next(within time.Duration) sseEvent {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			s.t.Fatal("the event stream closed while waiting for an event")
		}
		return ev
	case err := <-s.fail:
		s.t.Fatalf("event stream failed: %v", err)
	case <-time.After(within):
		s.t.Fatalf("no event arrived within %s", within)
	}
	return sseEvent{}
}

// expectSilence asserts that nothing arrives. It is the negative control for every wake-up test:
// without it, a hub that published to everyone would pass every positive assertion in the suite.
func (s *sseStream) expectSilence(within time.Duration) {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			return
		}
		s.t.Fatalf("expected no events, got %q %s", ev.Type, ev.Data)
	case <-time.After(within):
	}
}

func (s *sseStream) Close() {
	s.once.Do(func() {
		s.cancel()
		s.body.Close()
	})
}

// ---- fixtures --------------------------------------------------------------

// newDatabase creates one database per test and drops it afterwards. psql runs inside the container
// so the suite needs no PostgreSQL client on the host.
func newDatabase(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("e2e_%d_%d", os.Getpid(), dbSeq.Add(1))
	psql(t, "CREATE DATABASE "+quoteIdent(name))
	t.Cleanup(func() {
		// FORCE terminates any connection the server left behind. Without it a lingering pgx pool
		// makes the drop fail, and the failure is reported against the next test.
		psql(t, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
	})
	return name
}

func psql(t *testing.T, statement string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-e", "PGPASSWORD="+pgPassword, pgContainer,
		"psql", "-U", pgUser, "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-q", "-c", statement)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql %q: %v\n%s", statement, err, out)
	}
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func databaseURL(name string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(pgUser), url.QueryEscape(pgPassword), pgHost, pgPort, name)
}

// freePort asks the kernel for a port and gives it straight back. There is a window between the
// close and the server's bind, which is why start() checks the address the server announces rather
// than assuming it got what it asked for.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// writeAPKCert produces stand-in signing-certificate bytes and the checksum the server must publish
// for them. The expected value is computed here, from the same bytes, in the same encoding Android
// requires — so the assertion is on the encoding, not on a constant copied out of the server.
func writeAPKCert(t *testing.T) (path, checksum string) {
	t.Helper()
	content := []byte("e2e signing certificate bytes — not a real certificate")
	path = filepath.Join(t.TempDir(), "signing-cert.der")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write apk certificate: %v", err)
	}
	sum := sha256.Sum256(content)
	return path, b64(sum[:])
}

// writeAPKFixture stands in for the built DPC. Not a real APK: nothing on either side parses one,
// and a real build output would make this test run only where a Gradle assemble happened to have
// been done. The bytes are known here so the expected checksum is computed by the test rather than
// read back out of the answer it is checking.
func writeAPKFixture(t *testing.T) (path, checksum string) {
	t.Helper()
	content := []byte("e2e device policy controller bytes — not a real apk")
	path = filepath.Join(t.TempDir(), "familyguard.apk")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write apk fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	return path, b64(sum[:])
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
