package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFgctlEveryReadCommandShowsWhatTheServerHolds drives the CLI commands nothing else ran — a
// coverage run of the e2e suite found devices, policy, commands, apps, blocklist, usage, locations,
// audit and keys at 0% — against a family that has something in each, in both renderings. A
// --json-only test passes over a table renderer that prints the wrong column; a human-only one
// passes over JSON nobody can parse. Two of them did: `locations` printed "<nil>" for every time
// (it read recorded_at, the API says captured_at) and `usage` printed Go's map syntax.
func TestFgctlEveryReadCommandShowsWhatTheServerHolds(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)
	home := t.TempDir()
	key := mintKey(t, h, "e2e-every-command")
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": key}

	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 75, "timezone": "Europe/Zurich"})
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{"com.example.game": 45 * 60 * 1000},
	}).expect(http.StatusOK)
	h.call(http.MethodPost, "/device/location", f.deviceToken(), map[string]any{
		"latitude": 47.3769, "longitude": 8.5417, "accuracy_m": 12,
	}).expect(http.StatusOK)
	h.blockForFamily(f.parent.Token, "com.example.casino", "Casino", "gambling")
	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)
	if r := fgctlRun(t, home, env, "send", f.device.ID, "SYNC_POLICY"); r.code != 0 {
		t.Fatalf("send exited %d: %s", r.code, r.stderr)
	}

	for _, tc := range []struct {
		args []string
		// human and json are substrings each rendering must carry.
		human, json []string
	}{
		{[]string{"devices"}, []string{"Mira's phone"}, []string{f.device.ID}},
		{[]string{"policy", f.child.ID}, []string{"75"}, []string{`"daily_limit_minutes": 75`}},
		{[]string{"commands", f.device.ID}, []string{"SYNC_POLICY"}, []string{`"SYNC_POLICY"`}},
		{[]string{"apps"}, []string{fixturePackage}, []string{fixturePackage}},
		{[]string{"blocklist"}, []string{"com.example.casino"}, []string{"com.example.casino"}},
		{[]string{"usage", f.device.ID}, []string{"com.example.game", "45 min"}, []string{`"minutes": 45`, "com.example.game"}},
		{[]string{"locations", f.device.ID}, []string{"47.3769", "8.5417"}, []string{`"captured_at"`}},
		{[]string{"audit", "--limit", "50"}, []string{"API_KEY"}, []string{`"action"`}},
		{[]string{"keys"}, []string{"e2e-every-command"}, []string{"e2e-every-command"}},
		{[]string{"version"}, []string{"fgctl"}, []string{`"version"`}},
	} {
		name := strings.Join(tc.args, " ")
		human := fgctlRun(t, home, env, tc.args...)
		if human.code != 0 {
			t.Errorf("fgctl %s exited %d: %s", name, human.code, human.stderr)
			continue
		}
		for _, want := range tc.human {
			if !strings.Contains(human.stdout, want) {
				t.Errorf("fgctl %s does not show %q:\n%s", name, want, human.stdout)
			}
		}
		// Go's own rendering of a value nobody formatted is a renderer that was never written.
		for _, leak := range []string{"<nil>", "map[", "%!"} {
			if strings.Contains(human.stdout, leak) {
				t.Errorf("fgctl %s prints %q — an unformatted value:\n%s", name, leak, human.stdout)
			}
		}
		js := fgctlRun(t, home, env, append(tc.args, "--json")...)
		var v any
		if js.code != 0 || json.Unmarshal([]byte(js.stdout), &v) != nil {
			t.Errorf("fgctl %s --json exited %d and is not JSON:\n%s%s", name, js.code, js.stdout, js.stderr)
			continue
		}
		for _, want := range tc.json {
			if !strings.Contains(js.stdout, want) {
				t.Errorf("fgctl %s --json does not carry %s:\n%s", name, want, js.stdout)
			}
		}
		if strings.Contains(human.stdout+js.stdout, key) {
			t.Fatalf("fgctl %s printed the API key itself", name)
		}
	}
}

// TestFgctlRemovesOnlyWhenTold: rm-device and rm-child are the two commands MCP deliberately does
// not have (mcp_coverage_test.go), because deleting is something a person types. Without --yes they
// must change nothing, and say how.
func TestFgctlRemovesOnlyWhenTold(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-rm")}

	// Read back from the lists the console reads: there is no GET of a single child.
	present := func(list, id string) bool {
		return strings.Contains(string(h.call(http.MethodGet, list, f.parent.Token, nil).expect(http.StatusOK).Body), id)
	}
	for _, tc := range []struct{ verb, id, list string }{
		{"rm-device", f.device.ID, "/devices"},
		{"rm-child", f.child.ID, "/children"},
	} {
		if !present(tc.list, tc.id) {
			t.Fatalf("%s is not in %s before anything was removed", tc.id, tc.list)
		}
		r := fgctlRun(t, home, env, tc.verb, tc.id)
		if r.code == 0 || !strings.Contains(r.stderr, "--yes") {
			t.Fatalf("%s without --yes exited %d and said %q: it must refuse and name --yes", tc.verb, r.code, r.stderr)
		}
		if !present(tc.list, tc.id) {
			t.Fatalf("%s without --yes removed %s anyway", tc.verb, tc.id)
		}
		if r := fgctlRun(t, home, env, tc.verb, tc.id, "--yes"); r.code != 0 {
			t.Fatalf("%s --yes exited %d: %s", tc.verb, r.code, r.stderr)
		}
		if present(tc.list, tc.id) {
			t.Fatalf("%s --yes exited 0 and %s is still in %s", tc.verb, tc.id, tc.list)
		}
	}
}

// TestFgctlLogoutForgetsTheCredential: after logout the CLI has nothing to send, and says so with
// the exit code a script reads (3), not with a 401 from the server.
func TestFgctlLogoutForgetsTheCredential(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	key := mintKey(t, h, "e2e-logout")
	if r := fgctlPipe(t, home, key, "login", "--url", h.base, "--token-stdin"); r.code != 0 {
		t.Fatalf("login exited %d: %s", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, nil, "children"); r.code != 0 {
		t.Fatalf("children after login exited %d: %s", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, nil, "logout"); r.code != 0 {
		t.Fatalf("logout exited %d: %s", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, nil, "children"); r.code != 3 {
		t.Fatalf("children after logout exited %d (%s), want 3: the credential is still stored", r.code, r.stderr)
	}
	// The usage contract: help is a success, nothing at all is not.
	if r := fgctlRun(t, home, nil, "help"); r.code != 0 || !strings.Contains(r.stderr, "rm-child") {
		t.Errorf("help exited %d and listed %q", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, nil); r.code != 2 {
		t.Errorf("fgctl with no command exited %d, want 2", r.code)
	}
}

// TestFgctlNeverSendsTheKeyInCleartext: an http:// server address that is not this machine is
// refused — at login before the key is even read, and on every command for FAMILYGUARD_URL, which
// overrides the saved file. Until 0.6.37 `fgctl login --url http://…` was accepted and every later
// command, self-update included, ran in cleartext while looking entirely normal.
func TestFgctlNeverSendsTheKeyInCleartext(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	key := mintKey(t, h, "e2e-cleartext")

	r := fgctlPipe(t, home, key, "login", "--url", "http://guard.example.test", "--token-stdin")
	if r.code == 0 || !strings.Contains(r.stderr, "cleartext") {
		t.Fatalf("login to an http:// address exited %d and said %q", r.code, r.stderr)
	}
	if fileExists(filepath.Join(home, ".config", "familyguard", "config.json")) {
		t.Fatal("the refused login still stored a credential")
	}

	// 192.0.2.0/24 is TEST-NET-1: nothing answers there, so a request that was sent would hang
	// or fail on the network rather than come back with this refusal.
	r = fgctlRun(t, home, map[string]string{"FAMILYGUARD_URL": "http://192.0.2.1", "FAMILYGUARD_TOKEN": key}, "children")
	if r.code == 0 || !strings.Contains(r.stderr, "cleartext") {
		t.Fatalf("children against an http:// FAMILYGUARD_URL exited %d and said %q", r.code, r.stderr)
	}

	// The positive control: loopback http is this suite's own server, and works.
	if r := fgctlPipe(t, home, key, "login", "--url", h.base, "--token-stdin"); r.code != 0 {
		t.Fatalf("login to the loopback server exited %d: %s", r.code, r.stderr)
	}
	// And the file that now holds the key is readable by its owner only.
	info, err := os.Stat(filepath.Join(home, ".config", "familyguard", "config.json"))
	if err != nil {
		t.Fatalf("login succeeded and stored nothing: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the stored credential is mode %o: other users on this machine can read the API key", mode)
	}
}
