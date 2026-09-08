package e2e

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fgctl is driven as a black box over a pipe: the suite is its own module and imports nothing from
// the backend, so it cannot check the client against the client's own constants.

// fgctlRun invokes the binary with an isolated configuration directory.
//
// The isolation is not tidiness. os.UserConfigDir reads XDG_CONFIG_HOME (or AppData on Windows), so
// without it a test that runs `fgctl login` would overwrite the credential of whoever is running
// the suite -- and the test would still pass, which is the worst possible combination.
type fgctlResult struct {
	stdout string
	stderr string
	code   int
}

func fgctlRun(t *testing.T, home string, env map[string]string, args ...string) fgctlResult {
	t.Helper()
	cmd := exec.Command(fgctlBin, args...)
	// A clean environment, never os.Environ(): an ambient FAMILYGUARD_TOKEN on the developer's
	// machine would silently authenticate a test that is meant to be measuring an unauthenticated
	// one, and it would pass.
	environ := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"AppData=" + filepath.Join(home, "AppData"),
	}
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	cmd.Env = environ
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("running fgctl %v: %v", args, err)
		}
	}
	return fgctlResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// mintKey signs a parent in and creates an API key, returning the token that is shown exactly once.
func mintKey(t *testing.T, h *harness, name string) string {
	t.Helper()
	parent := h.signIn(primaryParent)
	var created apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": name}).
		expect(http.StatusCreated).decode(&created)
	if created.Token == "" {
		t.Fatal("the server created a key with no token; nothing can be measured with it")
	}
	return created.Token
}

// TestFgctlLoginRefusesAKeyTheServerDoesNot is the calibration, and it comes first deliberately.
//
// A login that stores an unverified credential and prints success is this project's recurring
// defect shape: the failure surfaces later, on an unrelated command, and reads as a server problem.
// The negative half must be shown to go red before the positive half means anything.
func TestFgctlLoginRefusesAKeyTheServerDoesNot(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()

	bad := fgctlRun(t, home, map[string]string{}, "login", "--url", h.base, "--token-stdin")
	// No stdin was attached, so the key is empty -- an empty key must not be stored either.
	if bad.code == 0 {
		t.Errorf("login with no key exited 0: %q / %q", bad.stdout, bad.stderr)
	}

	withGarbage := fgctlPipe(t, home, "fgk_this_key_was_never_issued", "login", "--url", h.base, "--token-stdin")
	if withGarbage.code == 0 {
		t.Fatalf("login accepted a key the server never issued; that is the false green this test exists for\nstdout=%q stderr=%q",
			withGarbage.stdout, withGarbage.stderr)
	}
	if !strings.Contains(withGarbage.stderr, "did not accept") {
		t.Errorf("the refusal does not say the server refused the key: %q", withGarbage.stderr)
	}

	// And nothing was written. A login that fails but stores anyway leaves a binary that reports a
	// credential it does not have.
	if path := filepath.Join(home, ".config", "familyguard", "config.json"); fileExists(path) {
		t.Errorf("a refused login still wrote %s", path)
	}

	// The positive half, on the same server, with a key it did issue. Without this the negative
	// half above could be passing because the server was simply unreachable.
	good := fgctlPipe(t, home, mintKey(t, h, "e2e"), "login", "--url", h.base, "--token-stdin")
	if good.code != 0 {
		t.Fatalf("login refused a key the server issued: %q / %q", good.stdout, good.stderr)
	}
	if !strings.Contains(good.stdout, primaryParent.Email) {
		t.Errorf("login did not report who the key acts as: %q", good.stdout)
	}
	if !fileExists(filepath.Join(home, ".config", "familyguard", "config.json")) {
		t.Error("a successful login stored nothing")
	}
}

// TestFgctlNeverPrintsTheCredential covers the rule that outranks convenience here.
func TestFgctlNeverPrintsTheCredential(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	token := mintKey(t, h, "e2e-secrecy")

	if r := fgctlPipe(t, home, token, "login", "--url", h.base, "--token-stdin"); r.code != 0 {
		t.Fatalf("login failed: %q", r.stderr)
	}
	for _, args := range [][]string{{"config"}, {"config", "--json"}, {"whoami"}, {"whoami", "--json"}} {
		r := fgctlRun(t, home, map[string]string{}, args...)
		if strings.Contains(r.stdout+r.stderr, token) {
			t.Errorf("fgctl %v printed the credential", args)
		}
		// Not even a prefix: a prefix identifies a key in a leaked recording.
		if len(token) > 12 && strings.Contains(r.stdout+r.stderr, token[:12]) {
			t.Errorf("fgctl %v printed a prefix of the credential", args)
		}
	}
}

// TestFgctlUnconfiguredReachesNoServer covers the public-repo rule: there is no compiled-in host,
// so an unconfigured binary must say how to configure itself rather than contact anything.
func TestFgctlUnconfiguredReachesNoServer(t *testing.T) {
	home := t.TempDir()
	r := fgctlRun(t, home, map[string]string{}, "children")
	if r.code != 3 {
		t.Errorf("unconfigured fgctl exited %d, want 3: %q / %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "fgctl login") {
		t.Errorf("the error does not say how to fix it: %q", r.stderr)
	}
}

// TestFgctlReadsWhatTheAPIReturns drives the read commands against a real server.
func TestFgctlReadsWhatTheAPIReturns(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-read")}

	// A child, created over the API, must appear in the CLI's own listing.
	parent := h.signIn(primaryParent)
	var child struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	h.call(http.MethodPost, "/children", parent.Token, map[string]any{"name": "Test Child"}).
		expect(http.StatusCreated).decode(&child)

	r := fgctlRun(t, home, env, "children", "--json")
	if r.code != 0 {
		t.Fatalf("children exited %d: %q", r.code, r.stderr)
	}
	var children []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &children); err != nil {
		t.Fatalf("children --json is not JSON: %v\n%s", err, r.stdout)
	}
	if !containsChild(children, child.ID) {
		t.Fatalf("the child created over the API is missing from fgctl children: %s", r.stdout)
	}

	// The human rendering must carry the same fact. A --json-only test would pass over a table
	// renderer that prints nothing at all.
	human := fgctlRun(t, home, env, "children")
	if !strings.Contains(human.stdout, "Test Child") {
		t.Errorf("the table rendering does not show the child: %q", human.stdout)
	}

	if whoami := fgctlRun(t, home, env, "whoami"); !strings.Contains(whoami.stdout, primaryParent.Email) {
		t.Errorf("whoami does not report the parent: %q", whoami.stdout)
	}
}

// TestFgctlRejectsAnUnknownCommandTypeLocally checks the closed set is enforced before the request,
// so a typo produces a message naming the alternatives rather than a 400.
func TestFgctlRejectsAnUnknownCommandTypeLocally(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-cmd")}

	r := fgctlRun(t, home, env, "send", "00000000-0000-0000-0000-000000000000", "MAKE_TEA")
	if r.code == 0 {
		t.Fatal("fgctl accepted a command type the server does not define")
	}
	if !strings.Contains(r.stderr, "TRIGGER_ALARM") {
		t.Errorf("the error does not name the commands that do exist: %q", r.stderr)
	}
}

// ---- MCP ------------------------------------------------------------------

// TestFgctlMCPServesTheTools speaks JSON-RPC to `fgctl mcp` over a pipe: initialize, tools/list,
// and a real tool call whose result must match what the API returns.
//
// Hand-rolled rather than using the SDK's client, because this module has no dependencies -- which
// also means the wire format is asserted rather than assumed.
func TestFgctlMCPServesTheTools(t *testing.T) {
	h := newHarness(t)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-mcp")}

	parent := h.signIn(primaryParent)
	var child struct {
		ID string `json:"id"`
	}
	h.call(http.MethodPost, "/children", parent.Token, map[string]any{"name": "MCP Child"}).
		expect(http.StatusCreated).decode(&child)

	session := startMCP(t, t.TempDir(), env)
	defer session.close()

	initResult := session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "1"},
	})
	if serverInfo, ok := initResult["serverInfo"].(map[string]any); !ok || serverInfo["name"] != "familyguard" {
		t.Fatalf("initialize did not identify the server: %v", initResult)
	}
	session.notify(t, "notifications/initialized", map[string]any{})

	listed := session.call(t, "tools/list", map[string]any{})
	tools, _ := listed["tools"].([]any)
	names := map[string]bool{}
	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok {
			names[fmt.Sprint(tool["name"])] = true
		}
	}
	for _, want := range []string{"list_children", "list_devices", "get_device", "send_command", "list_commands", "whoami"} {
		if !names[want] {
			t.Errorf("tools/list is missing %q; it has %v", want, sortedNames(names))
		}
	}
	// The four deletes are deliberately absent. Asserted, because "we chose not to expose it" is
	// worth exactly nothing if a later refactor quietly adds them back.
	for _, forbidden := range []string{"delete_child", "delete_device", "delete_parent", "delete_app"} {
		if names[forbidden] {
			t.Errorf("%q is exposed over MCP; destructive deletes are CLI-only by design", forbidden)
		}
	}

	called := session.call(t, "tools/call", map[string]any{"name": "list_children", "arguments": map[string]any{}})
	if isError, _ := called["isError"].(bool); isError {
		t.Fatalf("list_children returned an error result: %v", called)
	}
	text := mcpText(t, called)
	if !strings.Contains(text, child.ID) {
		t.Errorf("list_children did not return the child created over the API:\n%s", text)
	}

	// An error must come back as an error RESULT, not as a transport failure: a model has to be
	// able to read what went wrong and correct itself.
	bad := session.call(t, "tools/call", map[string]any{
		"name":      "send_command",
		"arguments": map[string]any{"device_id": "00000000-0000-0000-0000-000000000000", "type": "MAKE_TEA"},
	})
	if isError, _ := bad["isError"].(bool); !isError {
		t.Errorf("an invalid command type did not produce an error result: %v", bad)
	}
	if body := mcpText(t, bad); !strings.Contains(body, "TRIGGER_ALARM") {
		t.Errorf("the error result does not name the valid commands: %q", body)
	}
}

// ---- plumbing -------------------------------------------------------------

type mcpSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	nextID int
}

func startMCP(t *testing.T, home string, env map[string]string) *mcpSession {
	t.Helper()
	cmd := exec.Command(fgctlBin, "mcp")
	environ := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"AppData=" + filepath.Join(home, "AppData"),
	}
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	cmd.Env = environ
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting `fgctl mcp`: %v", err)
	}
	return &mcpSession{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout)}
}

func (s *mcpSession) close() {
	s.stdin.Close()
	// The server exits when stdin closes; kill only if it does not.
	done := make(chan struct{})
	go func() { s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
	}
}

func (s *mcpSession) write(t *testing.T, payload map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding %v: %v", payload, err)
	}
	if _, err := s.stdin.Write(append(encoded, '\n')); err != nil {
		t.Fatalf("writing to fgctl mcp: %v", err)
	}
}

func (s *mcpSession) notify(t *testing.T, method string, params map[string]any) {
	t.Helper()
	s.write(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *mcpSession) call(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()
	s.nextID++
	id := s.nextID
	s.write(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	// Skip any notification the server sends; only a response carries our id.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("reading the response to %s: %v", method, err)
		}
		var message map[string]any
		if json.Unmarshal(line, &message) != nil {
			continue
		}
		raw, present := message["id"]
		if !present {
			continue
		}
		if int(toFloat(raw)) != id {
			continue
		}
		if errBody, failed := message["error"]; failed {
			t.Fatalf("%s returned a protocol error: %v", method, errBody)
		}
		result, _ := message["result"].(map[string]any)
		return result
	}
	t.Fatalf("no response to %s within the deadline", method)
	return nil
}

func mcpText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	var parts []string
	for _, entry := range content {
		if block, ok := entry.(map[string]any); ok && block["type"] == "text" {
			parts = append(parts, fmt.Sprint(block["text"]))
		}
	}
	if len(parts) == 0 {
		t.Fatalf("the tool result carries no text content: %v", result)
	}
	return strings.Join(parts, "\n")
}

func fgctlPipe(t *testing.T, home, stdinText string, args ...string) fgctlResult {
	t.Helper()
	cmd := exec.Command(fgctlBin, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"AppData=" + filepath.Join(home, "AppData"),
	}
	cmd.Stdin = strings.NewReader(stdinText + "\n")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running fgctl %v: %v", args, err)
	}
	return fgctlResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func containsChild(children []map[string]any, id string) bool {
	for _, c := range children {
		if fmt.Sprint(c["id"]) == id {
			return true
		}
	}
	return false
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	return names
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

var _ = runtime.GOOS
