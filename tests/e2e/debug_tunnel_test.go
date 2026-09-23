package e2e

// FR-19: remote adb through the control plane, driven end to end with the real binary.
//
// Everything on the wire is real: the server, its database, `fgctl adb` as a separate process
// listening on a real port, and a real TCP server standing where the phone's adbd would be. The
// test plays the PHONE — it fetches the command over the device API with the device's own token and
// dials the phone's leg exactly as the DPC does — because the phone's half is Kotlin and is proven
// on an emulator by tests/android. What is proven here is everything between the parent's adb and
// the phone's socket: the upgrade on both legs, the hand-off, bytes in both directions without loss
// or reordering, the gate, the phone's refusal reaching the parent as a sentence, and that one
// phone cannot claim another phone's stream.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const adbdBanner = "ADBD-STAND-IN\n"

// standInADBD accepts connections, writes a banner FIRST — adb's connection is server-speaks-too,
// and bytes from the phone before the parent has written anything are the case a relay that only
// starts copying on the first client byte would lose — and then echoes.
func standInADBD(t *testing.T) (port int, accepted <-chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the stand-in adbd: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	ch := make(chan struct{}, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			ch <- struct{}{}
			go func() {
				defer c.Close()
				if _, err := io.WriteString(c, adbdBanner); err != nil {
					return
				}
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, ch
}

// fgctlADB starts `fgctl adb … --once --json` and returns the address it listens on.
type adbRelay struct {
	cmd    *exec.Cmd
	listen string
	stderr *lockedBuffer
	done   chan error
}

// lockedBuffer is fgctl's stderr, read by the test while the process is still writing it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// awaitPhone blocks until fgctl reports the phone's stream ready, and fails with fgctl's own words
// if it reports a refusal instead. This is the order a person follows too: `fgctl adb` says when
// the phone has answered, and adb is connected after that.
func (r *adbRelay) awaitPhone(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		said := r.stderr.String()
		switch {
		case strings.Contains(said, "waiting for adb"):
			return
		case strings.Contains(said, "the phone was not reached"):
			t.Fatalf("the phone refused the stream: %s", strings.TrimSpace(said))
		case time.Now().After(deadline):
			t.Fatalf("the phone did not answer within %s (fgctl said %q)", within, said)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitFor blocks until fgctl has said sub on stderr.
func (r *adbRelay) waitFor(t *testing.T, sub string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(r.stderr.String(), sub) {
		if time.Now().After(deadline) {
			t.Fatalf("fgctl adb never said %q within %s (stderr %q)", sub, within, r.stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startADBRelay(t *testing.T, env map[string]string, deviceID string, extra ...string) *adbRelay {
	t.Helper()
	home := t.TempDir()
	args := append([]string{"adb", deviceID, "--once", "--json"}, extra...)
	cmd := exec.Command(fgctlBin, args...)
	environ := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
	}
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	cmd.Env = environ
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fgctl adb: %v", err)
	}
	r := &adbRelay{cmd: cmd, stderr: stderr, done: make(chan error, 1)}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})
	lines := bufio.NewReader(stdout)
	lineCh := make(chan string, 1)
	go func() {
		line, _ := lines.ReadString('\n')
		lineCh <- line
		_, _ = io.Copy(io.Discard, lines)
	}()
	select {
	case line := <-lineCh:
		var announced struct {
			Listen string `json:"listen"`
			Target string `json:"target"`
		}
		if err := json.Unmarshal([]byte(line), &announced); err != nil || announced.Listen == "" {
			t.Fatalf("fgctl adb did not announce where it listens: %q (stderr %q)", line, stderr.String())
		}
		r.listen = announced.Listen
	case <-time.After(10 * time.Second):
		t.Fatalf("fgctl adb announced nothing within 10 s (stderr %q)", stderr.String())
	}
	go func() { r.done <- cmd.Wait() }()
	return r
}

// exitCode waits for the relay to finish.
func (r *adbRelay) exitCode(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case err := <-r.done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			return exitErr.ExitCode()
		}
		t.Fatalf("fgctl adb ended abnormally: %v", err)
	case <-time.After(within):
		t.Fatalf("fgctl adb had not exited after %s (stderr %q)", within, r.stderr.String())
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

type pendingCommand struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// awaitDebugCommand polls the device API the way the phone's drain does, until an
// OPEN_DEBUG_STREAM is there.
func awaitDebugCommand(t *testing.T, h *harness, deviceToken string, within time.Duration) pendingCommand {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var fetched struct {
			Commands []pendingCommand `json:"commands"`
		}
		h.call(http.MethodGet, "/device/commands", deviceToken, nil).expect(http.StatusOK).decode(&fetched)
		for _, c := range fetched.Commands {
			if c.Type == "OPEN_DEBUG_STREAM" {
				return c
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no OPEN_DEBUG_STREAM reached the phone within %s", within)
	return pendingCommand{}
}

// dialPhoneLeg does what the DPC does after reaching its adbd: one HTTP/1.1 upgrade on a raw
// connection. It returns the connection after a 101, or the status and body of anything else.
func dialPhoneLeg(t *testing.T, h *harness, deviceToken, stream string, upgrade bool) (net.Conn, *bufio.Reader, int, string) {
	t.Helper()
	address := strings.TrimPrefix(h.base, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dialling the server as the phone: %v", err)
	}
	req := "GET /api/v1/device/debug/" + stream + " HTTP/1.1\r\nHost: " + address +
		"\r\nAuthorization: Bearer " + deviceToken + "\r\n"
	if upgrade {
		req += "Connection: Upgrade\r\nUpgrade: familyguard-debug\r\n"
	}
	req += "\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("writing the phone's leg: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		t.Fatalf("reading the answer to the phone's leg: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		conn.Close()
		return nil, nil, resp.StatusCode, string(body)
	}
	return conn, reader, resp.StatusCode, ""
}

// playPhone fetches the command, connects to the stand-in adbd, dials the phone leg and splices.
func playPhone(t *testing.T, h *harness, deviceToken string, adbdPort int) <-chan pendingCommand {
	out := make(chan pendingCommand, 1)
	go func() {
		cmd := awaitDebugCommand(t, h, deviceToken, 20*time.Second)
		out <- cmd
		adbd, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", adbdPort))
		if err != nil {
			t.Errorf("the phone could not reach the stand-in adbd: %v", err)
			return
		}
		stream, _ := cmd.Params["stream"].(string)
		leg, reader, status, body := dialPhoneLeg(t, h, deviceToken, stream, true)
		if leg == nil {
			t.Errorf("the phone's leg was answered %d: %s", status, body)
			adbd.Close()
			return
		}
		h.call(http.MethodPost, "/device/commands/"+cmd.ID+"/ack", deviceToken,
			map[string]any{"ok": true, "result": map[string]any{"state": "connected"}}).expect(http.StatusOK)
		go func() { _, _ = io.Copy(adbd, reader); adbd.Close(); leg.Close() }()
		_, _ = io.Copy(leg, adbd)
		leg.Close()
	}()
	return out
}

func TestRemoteADBRelaysBytesBothWays(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": true})
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb")}

	adbdPort, accepted := standInADBD(t)
	relay := startADBRelay(t, env, f.device.ID)
	phone := playPhone(t, h, f.deviceToken(), adbdPort)

	// The phone is reached BEFORE adb connects. adb gives a connection about ten seconds to answer,
	// and reaching a phone can take longer than that; a relay that only asked for the phone once adb
	// had connected failed `adb connect` on a real emulator for exactly that reason.
	cmd := <-phone
	relay.waitFor(t, "waiting for adb", 20*time.Second)

	local, err := net.Dial("tcp", relay.listen)
	if err != nil {
		t.Fatalf("connecting to the relay's local port: %v", err)
	}
	defer local.Close()
	_ = local.SetDeadline(time.Now().Add(30 * time.Second))
	dialled := time.Now()

	if cmd.Params["target"] != "connect" {
		t.Errorf("the phone was asked for target %v; a plain `fgctl adb` is an adb connection", cmd.Params["target"])
	}
	if port, _ := cmd.Params["port"].(float64); port != 0 {
		t.Errorf("the phone was given port %v; with no --port it must find its own (0)", cmd.Params["port"])
	}
	select {
	case <-accepted:
	case <-time.After(20 * time.Second):
		t.Fatal("nothing connected to the stand-in adbd")
	}

	// Phone to parent, before the parent has sent a byte.
	banner := make([]byte, len(adbdBanner))
	if _, err := io.ReadFull(local, banner); err != nil {
		t.Fatalf("the adbd banner never reached the parent: %v (fgctl said %q)", err, relay.stderr.String())
	}
	if string(banner) != adbdBanner {
		t.Fatalf("the parent read %q, the phone sent %q", banner, adbdBanner)
	}
	if took := time.Since(dialled); took > 2*time.Second {
		t.Errorf("adb's connection waited %s for its first byte; the stream was supposed to be ready", took)
	}

	// Both ways, a megabyte, byte for byte. Large enough to cross every buffer on the path
	// several times, so a relay that dropped or reordered a chunk cannot pass by luck.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = local.Write(payload) }()
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(local, echoed); err != nil {
		t.Fatalf("the echo came back short: %v", err)
	}
	if !bytes.Equal(payload, echoed) {
		t.Fatal("the megabyte that came back is not the megabyte that was sent")
	}

	local.Close()
	if code := relay.exitCode(t, 15*time.Second); code != 0 {
		t.Fatalf("fgctl adb --once exited %d after a clean session: %q", code, relay.stderr.String())
	}
	if !strings.Contains(relay.stderr.String(), "stream closed") {
		t.Errorf("fgctl adb did not report the stream closing: %q", relay.stderr.String())
	}

	// The audit trail names both ends of the session, and the close carries what was carried.
	var entries []auditEntryDTO
	deadline := time.Now().Add(5 * time.Second)
	var closed *auditEntryDTO
	for time.Now().Before(deadline) && closed == nil {
		entries = h.readAudit(f.parent.Token)
		for i := range entries {
			if entries[i].Action == "DEBUG_STREAM_CLOSED" {
				closed = &entries[i]
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	requested := false
	for _, e := range entries {
		requested = requested || e.Action == "DEBUG_STREAM_REQUESTED"
	}
	if !requested {
		t.Error("no DEBUG_STREAM_REQUESTED entry: a remote debug session left no trace of being asked for")
	}
	if closed == nil {
		t.Fatal("no DEBUG_STREAM_CLOSED entry: the audit cannot say how long a session lasted")
	}
	if n, _ := closed.Detail["bytes_to_phone"].(float64); n < float64(len(payload)) {
		t.Errorf("the close records %v bytes to the phone; at least %d were sent", closed.Detail["bytes_to_phone"], len(payload))
	}
}

func TestRemoteADBIsRefusedWhileDebuggingIsOff(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb-off")}

	relay := startADBRelay(t, env, f.device.ID)
	// No adb connection is needed to hear the refusal: the phone is asked as soon as fgctl starts.
	// 45 s, past the server's 30 s dial window: if the gate were missing the relay would wait out
	// the window and then say the WRONG thing, and it is the sentence below that must go red.
	if code := relay.exitCode(t, 45*time.Second); code == 0 {
		t.Errorf("fgctl adb exited 0 over a refusal; a script cannot tell that from a session")
	}
	if !strings.Contains(relay.stderr.String(), "Allow debugging is off") {
		t.Errorf("fgctl did not say WHY: %q", relay.stderr.String())
	}

	// Refused before anything was asked of the phone.
	var cmds struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/commands", f.parent.Token, nil).
		expect(http.StatusOK).decode(&cmds)
	for _, c := range cmds.Commands {
		if c.Type == "OPEN_DEBUG_STREAM" {
			t.Fatalf("a debug stream was queued for a phone whose debugging is off: %+v", c)
		}
	}
}

// TestRemoteADBCarriesThePhonesReason is FR-19.7.
func TestRemoteADBCarriesThePhonesReason(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": true})
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb-why")}

	started := time.Now()
	relay := startADBRelay(t, env, f.device.ID)

	const reason = "Wireless debugging is switched off on this phone"
	cmd := awaitDebugCommand(t, h, f.deviceToken(), 20*time.Second)
	h.call(http.MethodPost, "/device/commands/"+cmd.ID+"/ack", f.deviceToken(),
		map[string]any{"ok": false, "error": reason}).expect(http.StatusOK)

	relay.exitCode(t, 45*time.Second) // past the dial window, for the same reason as above
	if !strings.Contains(relay.stderr.String(), reason) {
		t.Errorf("the phone's sentence did not reach the parent: %q", relay.stderr.String())
	}
	// Not the thirty-second timeout wearing the right words: the refusal must arrive as soon as
	// the phone gave it.
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("the refusal took %s to arrive; it should not wait out the dial window", took)
	}
}

func TestRemoteADBStreamBelongsToOnePhone(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": true})
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb-own")}

	// A second enrolled phone in the same family.
	other := h.newDevice(f.parent.Token, f.child.ID, "Second phone")
	_, otherEnroll := h.provision(f.parent.Token, other.ID)
	otherToken := h.enrollDevice(otherEnroll, "Pixel 8", "Android 15", []string{oemDialer}).DeviceToken

	// The generic command endpoint cannot queue one: the relay would not be waiting.
	h.call(http.MethodPost, "/devices/"+f.device.ID+"/commands", f.parent.Token,
		map[string]any{"type": "OPEN_DEBUG_STREAM"}).expectError(http.StatusBadRequest, "invalid_input")

	// Neither leg answers a request that did not ask for the upgrade.
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/debug", f.parent.Token, nil).
		expectError(http.StatusUpgradeRequired, "upgrade_required")

	adbdPort, _ := standInADBD(t)
	relay := startADBRelay(t, env, f.device.ID)
	local, err := net.Dial("tcp", relay.listen)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	cmd := awaitDebugCommand(t, h, f.deviceToken(), 20*time.Second)
	stream, _ := cmd.Params["stream"].(string)

	if _, _, status, _ := dialPhoneLeg(t, h, f.deviceToken(), stream, false); status != http.StatusUpgradeRequired {
		t.Errorf("the phone's leg without an upgrade was answered %d, not 426", status)
	}
	if conn, _, status, body := dialPhoneLeg(t, h, otherToken, stream, true); conn != nil || status != http.StatusNotFound {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("ANOTHER phone claimed this phone's debug stream: answered %d %s", status, body)
	}

	// The right phone still can, which is what shows the refusal above was about the phone and not
	// about a stream the failed attempt had used up.
	adbd, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", adbdPort))
	if err != nil {
		t.Fatal(err)
	}
	defer adbd.Close()
	leg, reader, status, body := dialPhoneLeg(t, h, f.deviceToken(), stream, true)
	if leg == nil {
		t.Fatalf("the phone the stream was opened for was answered %d: %s", status, body)
	}
	defer leg.Close()
	go func() { _, _ = io.Copy(adbd, reader) }()
	go func() { _, _ = io.Copy(leg, adbd) }()
	_ = local.SetReadDeadline(time.Now().Add(15 * time.Second))
	banner := make([]byte, len(adbdBanner))
	if _, err := io.ReadFull(local, banner); err != nil || string(banner) != adbdBanner {
		t.Fatalf("after the refused impostor, the real phone's bytes did not arrive: %q %v", banner, err)
	}

	// And once claimed, the id is spent: a replay of the same dial finds nothing.
	if conn, _, status, _ := dialPhoneLeg(t, h, f.deviceToken(), stream, true); conn != nil || status != http.StatusNotFound {
		if conn != nil {
			conn.Close()
		}
		t.Errorf("a second dial of a claimed stream was answered %d, not 404", status)
	}
}

// TestRemoteADBOutlivesTheRequestTimeout holds a stream idle past the server's ReadTimeout (30 s)
// and then uses it.
//
// The timeout is a deadline set on the CONNECTION while the request is read, and a hijack hands
// over the connection with the deadline still on it. Every real adb session is longer than thirty
// seconds, so a relay that did not clear it would pass every short test and die in real use at the
// same second every time.
func TestRemoteADBOutlivesTheRequestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a stream idle for 35 s")
	}
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"allow_debugging": true})
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb-long")}

	adbdPort, _ := standInADBD(t)
	relay := startADBRelay(t, env, f.device.ID)
	playPhone(t, h, f.deviceToken(), adbdPort)
	local, err := net.Dial("tcp", relay.listen)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	_ = local.SetDeadline(time.Now().Add(60 * time.Second))
	banner := make([]byte, len(adbdBanner))
	if _, err := io.ReadFull(local, banner); err != nil {
		t.Fatalf("the stream never opened: %v (%q)", err, relay.stderr.String())
	}

	time.Sleep(35 * time.Second)

	if _, err := io.WriteString(local, "still-there\n"); err != nil {
		t.Fatalf("writing after 35 s idle: %v", err)
	}
	back := make([]byte, len("still-there\n"))
	if _, err := io.ReadFull(local, back); err != nil || string(back) != "still-there\n" {
		t.Fatalf("after 35 s idle the stream no longer carries bytes (%q, %v) — the request's read "+
			"deadline survived the hijack", back, err)
	}
}
