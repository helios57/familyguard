package e2e

// FR-30.1 and FR-30.6 from fgctl and MCP: the learning prefixes are set where the owner sets them
// (MCP, with an API key), and the daily-limit switch is reachable from set_policy.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestFgctlLearningApps(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-learning")}
	parent := h.signIn(primaryParent)

	if r := fgctlRun(t, home, env, "learning-apps"); r.code != 0 || !strings.Contains(r.stdout, "no learning apps") {
		t.Fatalf("a new family: exit %d, %q", r.code, r.stdout+r.stderr)
	}
	if r := fgctlRun(t, home, env, "learning-apps", "--set", learnPrefix+",org.example.other"); r.code != 0 {
		t.Fatalf("--set exited %d: %s", r.code, r.stderr)
	}
	got := h.learningPrefixes(parent.Token)
	if !slices.Equal(got.Prefixes, []string{learnPrefix, "org.example.other"}) {
		t.Fatalf("after --set the server holds %v", got.Prefixes)
	}
	r := fgctlRun(t, home, env, "learning-apps", "--json")
	var out learningPrefixesDTO
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out.Prefixes) != 2 {
		t.Errorf("--json gives %q (%v)", r.stdout, err)
	}
	if human := fgctlRun(t, home, env, "learning-apps"); !strings.Contains(human.stdout, learnPrefix) {
		t.Errorf("the table does not show the prefix: %q", human.stdout)
	}
	if bad := fgctlRun(t, home, env, "learning-apps", "--set", "org..x"); bad.code == 0 || !strings.Contains(bad.stderr, "prefix") {
		t.Errorf("an invalid prefix: exit %d, %q", bad.code, bad.stderr)
	}
	if r := fgctlRun(t, home, env, "learning-apps", "--clear"); r.code != 0 {
		t.Fatalf("--clear exited %d: %s", r.code, r.stderr)
	}
	if got := h.learningPrefixes(parent.Token); len(got.Prefixes) != 0 {
		t.Errorf("after --clear the server holds %v", got.Prefixes)
	}

	// The policy shows a limit that is off as off, and one that is on at 0 as 0 — never the same line.
	child := h.newChild(parent.Token, "Mira")
	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_minutes": 0})
	// Matched on the line itself: "off" alone also matches the bedtime line.
	limitLine := regexp.MustCompile(`daily limit\s+(\S+)`)
	if r := fgctlRun(t, home, env, "policy", child.ID); limitLine.FindStringSubmatch(r.stdout) == nil || limitLine.FindStringSubmatch(r.stdout)[1] != "off" {
		t.Errorf("a limit switched off reads %q", r.stdout)
	}
	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_enabled": true, "daily_limit_minutes": 0})
	if r := fgctlRun(t, home, env, "policy", child.ID); !regexp.MustCompile(`daily limit\s+0 minutes`).MatchString(r.stdout) {
		t.Errorf("a limit on at 0 reads %q", r.stdout)
	}
}

func TestMCPSetLearningApps(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-mcp-learning")}
	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})
	tool := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		out := session.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
		isError, _ := out["isError"].(bool)
		return mcpText(t, out), isError
	}

	if text, isErr := tool("set_learning_apps", map[string]any{"prefixes": []string{learnPrefix}}); isErr {
		t.Fatalf("set_learning_apps: %s", text)
	}
	if got := h.learningPrefixes(parent.Token); !slices.Equal(got.Prefixes, []string{learnPrefix}) {
		t.Fatalf("after set_learning_apps the server holds %v", got.Prefixes)
	}
	if text, isErr := tool("get_learning_apps", map[string]any{}); isErr || !strings.Contains(text, learnPrefix) {
		t.Errorf("get_learning_apps: %s", text)
	}
	if text, isErr := tool("set_learning_apps", map[string]any{"prefixes": []string{"org.*"}}); !isErr || !strings.Contains(text, "invalid_input") {
		t.Errorf("an invalid prefix: want a refusal naming invalid_input, got %s", text)
	}

	child := h.newChild(parent.Token, "Mira")
	if text, isErr := tool("set_policy", map[string]any{"child_id": child.ID, "daily_limit_enabled": true, "daily_limit_minutes": 0}); isErr {
		t.Fatalf("set_policy: %s", text)
	}
	var pol struct {
		Enabled bool `json:"daily_limit_enabled"`
		Minutes int  `json:"daily_limit_minutes"`
	}
	h.call(http.MethodGet, "/children/"+child.ID+"/policy", parent.Token, nil).expect(http.StatusOK).decode(&pol)
	if !pol.Enabled || pol.Minutes != 0 {
		t.Errorf("after set_policy on at 0 the server holds %+v", pol)
	}
}
