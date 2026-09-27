package e2e

// FR-21 from the command line and over MCP: FR-17 promises everything a parent can do in the
// console can be done by something that is not at a browser, and a pause is the thing most likely
// to be wanted from a script at 21:00.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func (h *harness) childPaused(token, childID string) bool {
	h.t.Helper()
	var list struct {
		Children []struct {
			ID     string `json:"id"`
			Paused *bool  `json:"paused"`
		} `json:"children"`
	}
	h.call(http.MethodGet, "/children", token, nil).expect(http.StatusOK).decode(&list)
	for _, c := range list.Children {
		if c.ID == childID && c.Paused != nil {
			return *c.Paused
		}
	}
	h.t.Fatalf("child %s is not listed with a paused field", childID)
	return false
}

func TestFgctlPausesAndAdjustsTime(t *testing.T) {
	h := newHarness(t)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-pause")}
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")
	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})

	if r := fgctlRun(t, home, env, "pause", child.ID); r.code != 0 || !strings.Contains(r.stdout, "paused") {
		t.Fatalf("fgctl pause exited %d: %q %q", r.code, r.stdout, r.stderr)
	}
	if !h.childPaused(parent.Token, child.ID) {
		t.Fatal("fgctl pause answered 0 and the profile is not paused")
	}
	if r := fgctlRun(t, home, env, "unpause", child.ID); r.code != 0 {
		t.Fatalf("fgctl unpause exited %d: %q", r.code, r.stderr)
	}
	if h.childPaused(parent.Token, child.ID) {
		t.Fatal("fgctl unpause answered 0 and the profile is still paused")
	}

	r := fgctlRun(t, home, env, "bonus", child.ID, "-15", "--json")
	if r.code != 0 {
		t.Fatalf("fgctl bonus -15 exited %d: %q", r.code, r.stderr)
	}
	var out struct {
		Bonus      int `json:"bonus_minutes"`
		LimitToday int `json:"limit_today_minutes"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || out.Bonus != -15 || out.LimitToday != 45 {
		t.Fatalf("fgctl bonus -15 --json said %q (err %v); want −15 and 45 minutes today", r.stdout, err)
	}
	if human := fgctlRun(t, home, env, "bonus", child.ID, "5"); !strings.Contains(human.stdout, "50 minutes (60 − 10)") {
		t.Errorf("the table rendering of a reduced day does not say it: %q", human.stdout)
	}
	if bad := fgctlRun(t, home, env, "bonus", child.ID, "0"); bad.code == 0 || !strings.Contains(bad.stderr, "-1440") {
		t.Errorf("fgctl bonus 0 exited %d with %q; want a refusal naming the range", bad.code, bad.stderr)
	}

	// MCP: the same two acts, as tools a model can call.
	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})

	called := session.call(t, "tools/call", map[string]any{
		"name": "pause_profile", "arguments": map[string]any{"child_id": child.ID, "paused": true},
	})
	if isError, _ := called["isError"].(bool); isError {
		t.Fatalf("pause_profile returned an error: %v", called)
	}
	if !h.childPaused(parent.Token, child.ID) {
		t.Error("pause_profile answered without an error and the profile is not paused")
	}
	adjusted := session.call(t, "tools/call", map[string]any{
		"name": "adjust_time_today", "arguments": map[string]any{"child_id": child.ID, "minutes": -20},
	})
	if isError, _ := adjusted["isError"].(bool); isError {
		t.Fatalf("adjust_time_today returned an error: %v", adjusted)
	}
	if text := mcpText(t, adjusted); !strings.Contains(text, `"bonus_minutes": -30`) {
		t.Errorf("adjust_time_today −20 after −10 should leave −30 for today: %s", text)
	}
	zero := session.call(t, "tools/call", map[string]any{
		"name": "adjust_time_today", "arguments": map[string]any{"child_id": child.ID, "minutes": 0},
	})
	if isError, _ := zero["isError"].(bool); !isError {
		t.Errorf("adjust_time_today with 0 minutes should be an error result a model can read: %v", zero)
	}
}
