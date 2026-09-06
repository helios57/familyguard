package e2e

// A real browser, driven over the DevTools protocol, with no dependencies.
//
// Why a browser at all: every other guard over the console reads its *source* — that the viewport
// meta is present, that the stylesheet declares a 44 px tap constant, that the manifest would
// install. None of them can see a rendered box. A stylesheet whose comment header promises "no tap
// target smaller than 44 px" and whose `.pill` rule says `min-height: 36px` passes every source
// guard ever written for it, and the only instrument that disagrees is one that measures the pixels
// a thumb has to hit. That is this file's whole reason to exist.
//
// Why hand-written: the module's own doc comment says it has no dependencies, and the reason given
// there — a suite that mints its tokens with the library under test agrees with that library's bugs
// — does not apply to a WebSocket. What does apply is the second-order cost: a browser automation
// library brings a driver, a version matrix and a download step, and every one of those is a way for
// the check to stop running without going red. RFC 6455's client half is about a hundred lines, and
// CDP is JSON over that socket. Both are written out here for the same reason the ID tokens are.
//
// The browser is a stated precondition of the suite (see TestMain and run.sh), not an optional
// extra: a machine without one exits 2, NOT MEASURED. Skipping instead would leave a suite that
// reports PASS having never laid out a page.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
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
	"syscall"
	"testing"
	"time"
)

// ---- WebSocket (RFC 6455), client half ------------------------------------

// wsMagic is the constant the server mixes into the key it echoes back. Checking the echo is what
// distinguishes "a WebSocket server accepted the upgrade" from "something answered 101".
const wsMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func wsDial(rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("devtools url %q: %w", rawURL, err)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(host, "80")
	}
	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}

	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes[:])

	path := u.RequestURI()
	handshake := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := io.WriteString(conn, handshake); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake write: %w", err)
	}

	br := bufio.NewReader(conn)
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake read: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("handshake got %s, expected 101", resp.Status)
	}
	sum := sha1.Sum([]byte(key + wsMagic))
	if want := base64.StdEncoding.EncodeToString(sum[:]); resp.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, fmt.Errorf("Sec-WebSocket-Accept is %q, expected %q",
			resp.Header.Get("Sec-WebSocket-Accept"), want)
	}
	return &wsConn{conn: conn, br: br}, nil
}

// writeText sends one unfragmented, masked text frame. A client frame that is not masked is a
// protocol error the server closes on, so the mask is not optional even on a loopback socket.
func (w *wsConn) writeText(payload []byte) error {
	var head []byte
	n := len(payload)
	switch {
	case n < 126:
		head = []byte{0x81, byte(0x80 | n)}
	case n < 1<<16:
		head = []byte{0x81, 0x80 | 126, 0, 0}
		binary.BigEndian.PutUint16(head[2:], uint16(n))
	default:
		head = make([]byte, 10)
		head[0], head[1] = 0x81, 0x80|127
		binary.BigEndian.PutUint64(head[2:], uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.conn.Write(append(append(head, mask[:]...), masked...)); err != nil {
		return err
	}
	return nil
}

// readText returns the payload of the next text message, answering pings and reassembling
// continuation frames on the way. A close frame is returned as an error rather than as an empty
// message: an empty message is what a caller would then treat as a reply.
func (w *wsConn) readText() ([]byte, error) {
	var assembled []byte
	for {
		var head [2]byte
		if _, err := io.ReadFull(w.br, head[:]); err != nil {
			return nil, err
		}
		fin := head[0]&0x80 != 0
		opcode := head[0] & 0x0f
		if head[1]&0x80 != 0 {
			return nil, fmt.Errorf("the server masked a frame, which RFC 6455 forbids")
		}
		length := uint64(head[1] & 0x7f)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return nil, err
			}
			length = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return nil, err
			}
			length = binary.BigEndian.Uint64(ext[:])
		}
		if length > 64<<20 {
			return nil, fmt.Errorf("frame of %d bytes is beyond anything this client asks for", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return nil, err
		}

		switch opcode {
		case 0x0, 0x1: // continuation, text
			assembled = append(assembled, payload...)
			if fin {
				return assembled, nil
			}
		case 0x8:
			return nil, fmt.Errorf("the browser closed the devtools socket")
		case 0x9: // ping -> pong, same payload
			var h []byte
			if len(payload) < 126 {
				h = []byte{0x8a, byte(0x80 | len(payload))}
			} else {
				return nil, fmt.Errorf("ping payload of %d bytes is out of spec", len(payload))
			}
			var mask [4]byte
			if _, err := rand.Read(mask[:]); err != nil {
				return nil, err
			}
			masked := make([]byte, len(payload))
			for i := range payload {
				masked[i] = payload[i] ^ mask[i%4]
			}
			if _, err := w.conn.Write(append(append(h, mask[:]...), masked...)); err != nil {
				return nil, err
			}
		case 0xa: // pong, ignored
		default:
			return nil, fmt.Errorf("unexpected websocket opcode 0x%x", opcode)
		}
	}
}

func (w *wsConn) close() { w.conn.Close() }

// ---- the browser ----------------------------------------------------------

type browser struct {
	t    *testing.T
	cmd  *exec.Cmd
	dir  string
	ws   *wsConn
	next int
	// Everything the page itself complained about, in order: uncaught exceptions and console
	// errors, including the parse error a script that never ran reports.
	//
	// Collected because of what a missing one costs. The first run of this suite failed with
	// "waited 15s for the sign-in screen and it never happened" — true, and it named neither the
	// cause nor even the layer: the console's app.js had a syntax error, so not one line of it had
	// ever executed, in any browser, ever. Diagnosing that took a throwaway probe test. With this
	// slice the same failure prints the SyntaxError and its line number.
	pageErrors []string
}

// startBrowser launches headless Chrome and attaches to its first page.
//
// The binary comes from the environment, resolved by run.sh, for the same reason the server binary
// and the database do: a test that goes looking for a browser itself can find a different one than
// the harness was checked against, and "which browser measured this" would then be unrecorded.
func startBrowser(t *testing.T) *browser {
	t.Helper()

	// Not t.TempDir(). Chrome's helper processes — zygote, crashpad, the network service — outlive
	// the process this test kills by a moment and keep writing into the profile, and t.TempDir()
	// removes its directory as its own cleanup and *fails the test* when that races:
	// "TempDir RemoveAll cleanup: ... directory not empty". Observed intermittently, and it reddens
	// a run that measured the page perfectly — a flake that says nothing about the console. So the
	// profile is ours: the whole process group is killed (below), then the directory is removed with
	// a few attempts, and a leftover scratch directory is never reported as a test result.
	dir, err := os.MkdirTemp("", "familyguard-console-profile-")
	if err != nil {
		t.Fatalf("could not create a browser profile directory: %v", err)
	}
	cmd := exec.Command(chromeBin,
		"--headless=new",
		"--remote-debugging-port=0",
		"--user-data-dir="+dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-dev-shm-usage",
		// Overlay scrollbars come with the mobile emulation below; this keeps a classic scrollbar
		// from stealing width before the override is applied, which would make the very first
		// measurement disagree with every later one for a reason nothing reports.
		"--hide-scrollbars",
		"about:blank",
	)
	// Chrome's own diagnostics go to stderr. Captured rather than discarded: a browser that refuses
	// to start prints the reason there, and the alternative is a timeout with no cause.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// Its own process group, so the cleanup can kill every process Chrome forked rather than only
	// the one it started. Killing the parent alone leaves the helpers running, which is what makes
	// the profile directory refuse to be removed and, on a loaded machine, leaves browsers behind.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start %s: %v", chromeBin, err)
	}

	b := &browser{t: t, cmd: cmd, dir: dir}
	t.Cleanup(func() {
		if b.ws != nil {
			b.ws.close()
		}
		// The negative pid is the process group. Errors ignored on purpose: a browser that has
		// already exited is the outcome this wants, not a failure to report.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		for i := 0; i < 30; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	port := waitForDevToolsPort(t, dir, &stderr, cmd)
	wsURL := firstPageTarget(t, port)
	ws, err := wsDial(wsURL)
	if err != nil {
		t.Fatalf("could not attach to the browser page target: %v\nchrome stderr:\n%s", err, stderr.String())
	}
	b.ws = ws

	b.call("Page.enable", nil)
	b.call("Runtime.enable", nil)
	// Log for the errors the page reports about itself (a script that fails to parse, a blocked
	// request); Runtime.exceptionThrown for the ones it throws. Both, because neither is a superset:
	// a CSP violation arrives only as a Log entry and an uncaught TypeError only as an exception.
	b.call("Log.enable", nil)
	return b
}

// waitForDevToolsPort reads the port out of the file Chrome writes once it is listening. Polled
// rather than parsed out of stderr, because the stderr line is a debug message and has moved
// between versions; the file is the documented contract.
func waitForDevToolsPort(t *testing.T, dir string, stderr *strings.Builder, cmd *exec.Cmd) int {
	t.Helper()
	path := filepath.Join(dir, "DevToolsActivePort")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) >= 1 {
				if port, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil && port > 0 {
					return port
				}
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("the browser exited before it began listening\nstderr:\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the browser never wrote %s\nstderr:\n%s", path, stderr.String())
	return 0
}

func firstPageTarget(t *testing.T, port int) string {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err != nil {
			last = err.Error()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var targets []struct {
			Type string `json:"type"`
			URL  string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &targets); err != nil {
			last = fmt.Sprintf("/json/list is not a target list: %v (%s)", err, truncate(string(body), 200))
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, target := range targets {
			if target.Type == "page" && target.URL != "" {
				return target.URL
			}
		}
		last = "no page target yet: " + truncate(string(body), 200)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the browser never offered a page target (%s)", last)
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- the DevTools protocol ------------------------------------------------

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// call sends one command and returns its result.
//
// Synchronous on purpose: everything this file needs is request/response, so frames that are not the
// reply — the protocol's events — are read past rather than dispatched. A goroutine and a channel map
// would buy nothing here except two more ways to hang.
func (b *browser) call(method string, params map[string]any) json.RawMessage {
	b.t.Helper()
	raw, err := b.tryCall(method, params)
	if err != nil {
		b.t.Fatalf("%s: %v", method, err)
	}
	return raw
}

// tryCall is call without the fatal, for the one caller that treats "this browser does not
// implement that" as a fact to report rather than a failure.
func (b *browser) tryCall(method string, params map[string]any) (json.RawMessage, error) {
	b.next++
	id := b.next
	if params == nil {
		params = map[string]any{}
	}
	msg, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := b.ws.conn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return nil, err
	}
	if err := b.ws.writeText(msg); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	for {
		frame, err := b.ws.readText()
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		var envelope struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *cdpError       `json:"error"`
		}
		if err := json.Unmarshal(frame, &envelope); err != nil {
			return nil, fmt.Errorf("undecodable frame %s: %w", truncate(string(frame), 200), err)
		}
		if envelope.ID != id {
			b.recordIfPageError(frame)
			continue // an event, or a reply to a command this client has already finished with
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("%s (code %d) %s", envelope.Error.Message, envelope.Error.Code, envelope.Error.Data)
		}
		return envelope.Result, nil
	}
}

// recordIfPageError keeps the page's own complaints, so a failing assertion can print them.
//
// Deliberately lossy in one direction only: anything it cannot decode is ignored, because this is
// diagnostics and a malformed event must not fail a test. It is never the thing being asserted on —
// an empty list is not evidence the page is healthy, only that nothing was captured.
func (b *browser) recordIfPageError(frame []byte) {
	var event struct {
		Method string `json:"method"`
		Params struct {
			ExceptionDetails struct {
				Text       string `json:"text"`
				LineNumber int    `json:"lineNumber"`
				URL        string `json:"url"`
				Exception  *struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
			Entry struct {
				Level      string `json:"level"`
				Text       string `json:"text"`
				URL        string `json:"url"`
				LineNumber int    `json:"lineNumber"`
			} `json:"entry"`
		} `json:"params"`
	}
	if err := json.Unmarshal(frame, &event); err != nil {
		return
	}
	switch event.Method {
	case "Runtime.exceptionThrown":
		d := event.Params.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = d.Exception.Description
		}
		b.pageErrors = append(b.pageErrors, fmt.Sprintf("%s:%d %s", d.URL, d.LineNumber+1, truncate(text, 300)))
	case "Log.entryAdded":
		e := event.Params.Entry
		if e.Level != "error" {
			return
		}
		b.pageErrors = append(b.pageErrors, fmt.Sprintf("%s:%d %s", e.URL, e.LineNumber+1, truncate(e.Text, 300)))
	}
}

// pageErrorReport renders what the page complained about, for a failure message.
func (b *browser) pageErrorReport() string {
	if len(b.pageErrors) == 0 {
		return "the page reported no errors (which is not the same as none having occurred)"
	}
	return "the page reported:\n  " + strings.Join(b.pageErrors, "\n  ")
}

// focus points the driver's failure reporting at t until the returned function runs.
//
// The driver holds one *testing.T, taken when the browser started, so without this a `b.eval` that
// fails inside a `t.Run` closure calls Fatalf on the *parent* test from the subtest's goroutine. Go
// reports that as "test executed panic(nil) or runtime.Goexit: subtest may have called FailNow on a
// parent test" — a sentence about the harness, printed where the sentence about the page should be,
// and the subtest that actually failed is marked failed with no reason at all.
//
// Used as `defer b.focus(t)()` on the first line of every subtest.
func (b *browser) focus(t *testing.T) func() {
	prev := b.t
	b.t = t
	return func() { b.t = prev }
}

// phone puts the page into the shape the requirement is written about: a 360 px portrait phone with
// a touch screen, not a narrow desktop window. `mobile: true` is what makes the emulation use a
// phone's overlay scrollbars and layout viewport; without it a 360 px window still lays out like a
// desktop and the measurement would be of the wrong thing.
func (b *browser) phone(width, height int) {
	b.t.Helper()
	b.call("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height,
		"deviceScaleFactor": 3, "mobile": true,
		"screenWidth": width, "screenHeight": height,
	})
	b.call("Emulation.setTouchEmulationEnabled", map[string]any{"enabled": true, "maxTouchPoints": 5})
}

// laptop is the other side of the 900 px breakpoint: a pointer, no touch, no device pixel ratio.
//
// It is not `phone` with a bigger number. `mobile: true` keeps the layout viewport and the overlay
// scrollbars a handset has, and a 1280 px "phone" would measure a layout no hardware produces —
// while `mobile: false` is what makes the media query, the scrollbar gutter and hover-only affordances
// behave the way they do on the machine a parent actually opens the console on.
func (b *browser) laptop(width, height int) {
	b.t.Helper()
	b.call("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height,
		"deviceScaleFactor": 1, "mobile": false,
		"screenWidth": width, "screenHeight": height,
	})
	// No maxTouchPoints on the way off: the protocol validates it as 1..16 even when `enabled` is
	// false, so passing 0 fails the call with "Touch points must be between 1 and 16" and leaves
	// the page in whatever touch mode the previous override set.
	b.call("Emulation.setTouchEmulationEnabled", map[string]any{"enabled": false})
}

// installabilityErrors asks Chrome whether it would install this page.
//
// tryCall rather than call: the method is experimental, so "this browser does not implement it" is a
// fact to report — with the method named — and not a generic protocol failure.
func (b *browser) installabilityErrors(t *testing.T) []installabilityError {
	t.Helper()
	raw, err := b.tryCall("Page.getInstallabilityErrors", nil)
	if err != nil {
		t.Fatalf("Page.getInstallabilityErrors is unavailable in this browser (%v); installability "+
			"cannot be measured here, and a suite that skipped it would report FR-13.3 as green", err)
	}
	var out struct {
		InstallabilityErrors []installabilityError `json:"installabilityErrors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Page.getInstallabilityErrors: %v", err)
	}
	return out.InstallabilityErrors
}

type installabilityError struct {
	ErrorID        string `json:"errorId"`
	ErrorArguments []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"errorArguments"`
}

func (b *browser) navigate(rawURL string) {
	b.t.Helper()
	result := b.call("Page.navigate", map[string]any{"url": rawURL})
	var out struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(result, &out); err == nil && out.ErrorText != "" {
		b.t.Fatalf("navigating to %s: %s", rawURL, out.ErrorText)
	}
	b.waitFor("document.readyState === 'complete'", 30*time.Second, "the page to finish loading")
}

// eval runs an expression in the page and decodes its value.
//
// `awaitPromise` so an async expression is waited for rather than returning a pending promise that
// decodes as an empty object, and `exceptionDetails` is checked rather than ignored: a thrown
// TypeError with a swallowed result is the shape of a measurement that silently measured nothing.
func (b *browser) eval(expression string, out any) {
	b.t.Helper()
	if thrown := b.tryEval(expression, out); thrown != "" {
		b.t.Fatalf("the page threw while evaluating:\n%s\n--- expression ---\n%s", thrown, expression)
	}
}

// tryEval is eval without the verdict: it returns the exception text rather than failing, and "" when
// the expression evaluated. Only a poll should use it — a one-shot measurement that throws has not
// measured anything and must be fatal, which is what eval above is for.
func (b *browser) tryEval(expression string, out any) string {
	b.t.Helper()
	result := b.call("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	var envelope struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		b.t.Fatalf("Runtime.evaluate returned something undecodable: %v", err)
	}
	if envelope.Exception != nil {
		detail := envelope.Exception.Text
		if envelope.Exception.Exception != nil && envelope.Exception.Exception.Description != "" {
			detail = envelope.Exception.Exception.Description
		}
		return detail
	}
	if out == nil {
		return ""
	}
	if len(envelope.Result.Value) == 0 {
		b.t.Fatalf("the expression returned undefined:\n%s", expression)
	}
	if err := json.Unmarshal(envelope.Result.Value, out); err != nil {
		b.t.Fatalf("could not decode %s into %T: %v", truncate(string(envelope.Result.Value), 300), out, err)
	}
	return ""
}

// waitFor polls a boolean expression. The timeout is a failure with the expression in it, never a
// skip: a condition that never came true is the measurement not happening.
//
// A poll whose expression THROWS is "not yet", not a failure, and that distinction is load-bearing.
// Every wait here reaches into the DOM — `!document.getElementById('app').hidden` — and during a
// navigation the element is legitimately absent for a moment, so the expression raises a TypeError
// on a document that is merely still arriving. Failing on it turns a slow machine into a red that
// names a null property rather than a missing condition, which is how a contended CI box produces a
// failure indistinguishable from a broken console. Measured 2026-09-06: this test suite went red on
// `Cannot read properties of null (reading 'hidden')` in a run where every other layer was green,
// solely because the page took 38 s to load under load.
//
// What is NOT lost by tolerating it: an expression that throws every time still fails at the
// deadline, and the last exception is printed with the rest of the report — so a genuinely wrong
// expression says so, and says what it threw, instead of only "it never happened".
func (b *browser) waitFor(expression string, timeout time.Duration, what string) {
	b.t.Helper()
	deadline := time.Now().Add(timeout)
	lastThrow := ""
	for {
		var ok bool
		if thrown := b.tryEval("!!("+expression+")", &ok); thrown != "" {
			lastThrow, ok = thrown, false
		} else {
			lastThrow = ""
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			var url, html string
			b.eval("location.href", &url)
			b.eval("document.body ? document.body.innerHTML.slice(0, 600) : '<no body>'", &html)
			threw := ""
			if lastThrow != "" {
				threw = "\nthe expression threw every time, most recently: " + lastThrow
			}
			b.t.Fatalf("waited %s for %s and it never happened.\nexpression: %s%s\nat: %s\n%s\nbody: %s",
				timeout, what, expression, threw, url, b.pageErrorReport(), html)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
