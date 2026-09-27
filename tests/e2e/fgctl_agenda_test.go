package e2e

// FR-24 from the command line and over MCP (FR-17): the agenda and the holidays as documents, the
// week as the server expands it, and the alarm's "not during holidays".

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFgctlKeepsTheAgendaAndHolidays(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.patchPolicy(primary.Token, child.ID, map[string]any{"timezone": "Europe/Zurich"})
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-agenda")}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0o600)
		return p
	}

	agendaFile := write("agenda.json", `{"entries":[
	  {"kind":"RECURRING","title":"Schule","place":"Schulhaus","weekdays":31,"starts_at":"08:00","ends_at":"12:00"},
	  {"kind":"SINGLE","title":"Zahnarzt","day":"2026-10-07","starts_at":"14:00","ends_at":"14:30","optional":true}]}`)
	if r := fgctlRun(t, home, env, "agenda", child.ID, "--set", agendaFile); r.code != 0 {
		t.Fatalf("fgctl agenda --set exited %d: %q", r.code, r.stderr)
	}
	var stored struct {
		Entries []agendaEntryDTO `json:"entries"`
	}
	h.call(http.MethodGet, "/children/"+child.ID+"/agenda", primary.Token, nil).expect(http.StatusOK).decode(&stored)
	if len(stored.Entries) != 2 || stored.Entries[1].Day != "2026-10-07" {
		t.Fatalf("fgctl agenda --set answered 0 and the server holds %+v", stored.Entries)
	}
	if r := fgctlRun(t, home, env, "agenda", child.ID); !strings.Contains(r.stdout, "Mon–Fri") || !strings.Contains(r.stdout, "2026-10-07") {
		t.Errorf("the agenda table does not show the days and the date: %q", r.stdout)
	}

	holFile := write("holidays.json", `{"holidays":[{"title":"Herbstferien","starts_on":"2026-10-06","ends_on":"2026-10-08"}]}`)
	if r := fgctlRun(t, home, env, "holidays", "--set", holFile); r.code != 0 {
		t.Fatalf("fgctl holidays --set exited %d: %q", r.code, r.stderr)
	}
	if r := fgctlRun(t, home, env, "holidays"); !strings.Contains(r.stdout, "Herbstferien") || !strings.Contains(r.stdout, "3 days") {
		t.Errorf("the holidays table does not show the holiday and its length: %q", r.stdout)
	}

	r := fgctlRun(t, home, env, "week", child.ID, "--from", "2026-10-05", "--days", "3")
	if r.code != 0 || !strings.Contains(r.stdout, "2026-10-05") || !strings.Contains(r.stdout, "08:00–12:00 Schule") ||
		!strings.Contains(r.stdout, "Herbstferien") || strings.Contains(r.stdout, "2026-10-08") {
		t.Errorf("fgctl week from Monday for three days said (rc %d): %q", r.code, r.stdout)
	}

	alarmFile := write("alarm.json", `{"weekdays":["06:30","06:30","06:30","06:30","06:30","",""],"skip_holidays":true}`)
	if r := fgctlRun(t, home, env, "alarm", child.ID, "--set", alarmFile); r.code != 0 {
		t.Fatalf("fgctl alarm --set with skip_holidays exited %d: %q", r.code, r.stderr)
	}
	if !h.getAlarmSkip(primary.Token, child.ID) {
		t.Error("fgctl alarm --set did not store skip_holidays")
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
	if text, isErr := tool("get_week", map[string]any{"child_id": child.ID, "from": "2026-10-05", "days": 3}); isErr || !strings.Contains(text, `"Herbstferien"`) {
		t.Errorf("get_week: error=%v %s", isErr, text)
	}
	if text, isErr := tool("get_agenda", map[string]any{"child_id": child.ID}); isErr || !strings.Contains(text, stored.Entries[0].ID) {
		t.Errorf("get_agenda: error=%v %s", isErr, text)
	}
	if text, isErr := tool("set_agenda", map[string]any{"child_id": child.ID, "entries": []map[string]any{
		{"id": stored.Entries[0].ID, "kind": "RECURRING", "title": "Primarschule", "weekdays": 31, "starts_at": "08:00", "ends_at": "12:00"},
	}}); isErr {
		t.Fatalf("set_agenda: %s", text)
	}
	h.call(http.MethodGet, "/children/"+child.ID+"/agenda", primary.Token, nil).expect(http.StatusOK).decode(&stored)
	if len(stored.Entries) != 1 || stored.Entries[0].Title != "Primarschule" {
		t.Errorf("set_agenda answered without an error and the agenda is %+v", stored.Entries)
	}
	if text, isErr := tool("set_holidays", map[string]any{"holidays": []map[string]any{
		{"title": "Winterferien", "starts_on": "2026-12-21", "ends_on": "2027-01-01"},
	}}); isErr {
		t.Fatalf("set_holidays: %s", text)
	}
	if text, isErr := tool("get_holidays", map[string]any{}); isErr || !strings.Contains(text, "Winterferien") || strings.Contains(text, "Herbstferien") {
		t.Errorf("get_holidays after set_holidays: error=%v %s", isErr, text)
	}
	if text, isErr := tool("set_alarm", map[string]any{"child_id": child.ID, "weekdays": []string{"07:00", "", "", "", "", "", ""}, "skip_holidays": false}); isErr {
		t.Fatalf("set_alarm: %s", text)
	}
	if h.getAlarmSkip(primary.Token, child.ID) {
		t.Error("set_alarm with skip_holidays false left it on")
	}
}

func (h *harness) getAlarmSkip(token, childID string) bool {
	h.t.Helper()
	var a struct {
		SkipHolidays bool `json:"skip_holidays"`
	}
	h.call(http.MethodGet, "/children/"+childID+"/alarm", token, nil).expect(http.StatusOK).decode(&a)
	return a.SkipHolidays
}
