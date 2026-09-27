package e2e

// FR-23 from the command line and over MCP (FR-17): the alarm week and a change for one date, read
// back from the server rather than from what the tool printed.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFgctlSetsTheAlarm(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.patchPolicy(primary.Token, child.ID, map[string]any{"timezone": "Europe/Zurich"})
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-alarm")}

	week := []string{"06:30", "06:30", "06:30", "06:30", "07:00", "", ""}
	file := filepath.Join(t.TempDir(), "alarm.json")
	_ = os.WriteFile(file, []byte(`{"weekdays":["06:30","06:30","06:30","06:30","07:00","",""]}`), 0o600)
	if r := fgctlRun(t, home, env, "alarm", child.ID, "--set", file); r.code != 0 {
		t.Fatalf("fgctl alarm --set exited %d: %q", r.code, r.stderr)
	}
	if got := h.getAlarm(primary.Token, child.ID).Weekdays; !slices.Equal(got, week) {
		t.Fatalf("fgctl alarm --set answered 0 and the server holds %v", got)
	}
	if r := fgctlRun(t, home, env, "alarm", child.ID); !strings.Contains(r.stdout, "Fri") || !strings.Contains(r.stdout, "07:00") ||
		!strings.Contains(r.stdout, "off") {
		t.Errorf("the alarm table does not show Friday at 07:00 and a day off: %q", r.stdout)
	}

	// "tomorrow" is the profile's tomorrow, not the machine's.
	if r := fgctlRun(t, home, env, "alarm-day", child.ID, "tomorrow", "off"); r.code != 0 {
		t.Fatalf("fgctl alarm-day tomorrow off exited %d: %q", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, env, "alarm-day", child.ID, zurichDay(2), "05:45"); r.code != 0 {
		t.Fatalf("fgctl alarm-day <date> 05:45 exited %d: %q", r.code, r.stderr)
	}
	o := h.getAlarm(primary.Token, child.ID).Overrides
	if len(o) != 2 || o[0].Day != zurichDay(1) || o[0].Time != nil || o[1].Time == nil || *o[1].Time != "05:45" {
		t.Fatalf("after alarm-day the changes are %+v", o)
	}
	if r := fgctlRun(t, home, env, "alarm-day", child.ID, "tomorrow", "clear"); r.code != 0 {
		t.Fatalf("fgctl alarm-day tomorrow clear exited %d: %q", r.code, r.stderr)
	}
	if o := h.getAlarm(primary.Token, child.ID).Overrides; len(o) != 1 {
		t.Errorf("clear left %+v", o)
	}
	if r := fgctlRun(t, home, env, "alarm-day", child.ID, "tomorrow", "6"); r.code == 0 || !strings.Contains(r.stderr, "HH:MM") {
		t.Errorf("a malformed time exited %d with %q; want a refusal naming HH:MM", r.code, r.stderr)
	}

	// ---- MCP ----
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
	if text, isErr := tool("get_alarm", map[string]any{"child_id": child.ID}); isErr || !strings.Contains(text, `"07:00"`) {
		t.Errorf("get_alarm: error=%v %s", isErr, text)
	}
	if text, isErr := tool("set_alarm", map[string]any{"child_id": child.ID, "weekdays": []string{"", "", "", "", "", "09:00", "09:00"}}); isErr {
		t.Fatalf("set_alarm: %s", text)
	}
	if got := h.getAlarm(primary.Token, child.ID).Weekdays; got[5] != "09:00" || got[0] != "" {
		t.Errorf("set_alarm answered without an error and the week is %v", got)
	}
	if text, isErr := tool("set_alarm_day", map[string]any{"child_id": child.ID, "day": zurichDay(3), "time": "off"}); isErr {
		t.Fatalf("set_alarm_day off: %s", text)
	}
	found := false
	for _, x := range h.getAlarm(primary.Token, child.ID).Overrides {
		found = found || (x.Day == zurichDay(3) && x.Time == nil)
	}
	if !found {
		t.Error("set_alarm_day off answered without an error and the day has no change")
	}
	if text, isErr := tool("set_alarm_day", map[string]any{"child_id": child.ID, "day": zurichDay(3), "time": "noon"}); !isErr ||
		!strings.Contains(text, "HH:MM") {
		t.Errorf("an unreadable time should be an error result naming the form: error=%v %s", isErr, text)
	}
}
