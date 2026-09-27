package e2e

// FR-22 from the command line and over MCP (FR-17): the daily plan read and written as one document,
// today's tasks, and a parent's decision on each — the same acts as the console's plan editor and
// the guardian window, read back from the server rather than from what the tool printed.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFgctlDrivesTheDailyPlan(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-plan")}

	// ---- plan --set: a file holding the document the API takes ----
	file := filepath.Join(t.TempDir(), "plan.json")
	doc, _ := json.Marshal(map[string]any{"groups": []planGroupDTO{allDay("Tag", 30, "Katze füttern", "Klavier üben")}})
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := fgctlRun(t, home, env, "plan", f.child.ID, "--set", file); r.code != 0 {
		t.Fatalf("fgctl plan --set exited %d: %q %q", r.code, r.stdout, r.stderr)
	}
	var stored struct {
		Groups []planGroupDTO `json:"groups"`
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/plan", f.parent.Token, nil).expect(http.StatusOK).decode(&stored)
	if len(stored.Groups) != 1 || len(stored.Groups[0].Tasks) != 2 || stored.Groups[0].EarnedMinutes != 30 {
		t.Fatalf("fgctl plan --set answered 0 and the server holds %+v", stored.Groups)
	}
	cat, piano := stored.Groups[0].Tasks[0].ID, stored.Groups[0].Tasks[1].ID

	// ---- plan: read back, as JSON with the ids a later --set must carry, and as a table ----
	r := fgctlRun(t, home, env, "plan", f.child.ID, "--json")
	var listed struct {
		Groups []planGroupDTO `json:"groups"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &listed); err != nil || len(listed.Groups) != 1 || listed.Groups[0].ID != stored.Groups[0].ID {
		t.Fatalf("fgctl plan --json said %q (err %v); want the stored group with its id", r.stdout, err)
	}
	if table := fgctlRun(t, home, env, "plan", f.child.ID); !strings.Contains(table.stdout, "Klavier üben") ||
		!strings.Contains(table.stdout, "every day") || !strings.Contains(table.stdout, "30 min") {
		t.Errorf("the plan table does not show the task, the days and the minutes: %q", table.stdout)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"groups":[{"title":"","weekdays":127,"starts_at":"07:00","ends_at":"08:00","tasks":[{"title":"x"}]}]}`), 0o600)
	if r := fgctlRun(t, home, env, "plan", f.child.ID, "--set", bad); r.code == 0 || !strings.Contains(r.stderr, "title") {
		t.Errorf("a plan the server refuses exited %d with %q; want a refusal naming the field", r.code, r.stderr)
	}

	// ---- today and the decisions ----
	h.call(http.MethodPost, "/device/tasks/"+cat+"/report", f.deviceToken(), nil).expect(http.StatusOK)
	if r := fgctlRun(t, home, env, "today", f.child.ID); !strings.Contains(r.stdout, "Katze füttern") || !strings.Contains(r.stdout, "reported") {
		t.Errorf("fgctl today does not show the reported task: %q %q", r.stdout, r.stderr)
	}
	for _, step := range [][]string{{"reject", cat}, {"confirm", cat}, {"confirm", piano}} {
		if r := fgctlRun(t, home, env, step[0], f.child.ID, step[1]); r.code != 0 {
			t.Fatalf("fgctl %s exited %d: %q", step[0], r.code, r.stderr)
		}
	}
	if d := h.today(f.parent.Token, f.child.ID); d.Earned.Available != 30 {
		t.Fatalf("after fgctl confirmed both tasks the profile has %+v; want 30 minutes earned", d.Earned)
	}
	if r := fgctlRun(t, home, env, "today", f.child.ID); !strings.Contains(r.stdout, "earned time left") || !strings.Contains(r.stdout, "30 min") {
		t.Errorf("fgctl today does not report the earned time: %q", r.stdout)
	}
	if r := fgctlRun(t, home, env, "confirm", f.child.ID, "00000000-0000-0000-0000-000000000000"); r.code == 0 ||
		!strings.Contains(r.stderr, "not in this profile's plan") {
		t.Errorf("confirming a task that is not in the plan exited %d with %q", r.code, r.stderr)
	}

	// ---- MCP: the same, as tools ----
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

	if text, isErr := tool("get_today", map[string]any{"child_id": f.child.ID}); isErr || !strings.Contains(text, `"CONFIRMED"`) {
		t.Errorf("get_today: error=%v %s", isErr, text)
	}
	if text, isErr := tool("decide_task", map[string]any{"child_id": f.child.ID, "task_id": piano, "decision": "undo"}); isErr {
		t.Fatalf("decide_task undo: %s", text)
	}
	if d := h.today(f.parent.Token, f.child.ID); d.Earned.Available != 0 {
		t.Errorf("decide_task undo answered without an error and %+v is still earned", d.Earned)
	}
	if text, isErr := tool("decide_task", map[string]any{"child_id": f.child.ID, "task_id": piano, "decision": "maybe"}); !isErr ||
		!strings.Contains(text, "confirm, reject or undo") {
		t.Errorf("an unknown decision should be an error result naming the three: error=%v %s", isErr, text)
	}

	text, isErr := tool("get_plan", map[string]any{"child_id": f.child.ID})
	if isErr || !strings.Contains(text, stored.Groups[0].ID) {
		t.Fatalf("get_plan: error=%v %s", isErr, text)
	}
	edited := stored.Groups[0]
	edited.Title = "Tagsüber"
	if text, isErr := tool("set_plan", map[string]any{"child_id": f.child.ID, "groups": []planGroupDTO{edited}}); isErr {
		t.Fatalf("set_plan: %s", text)
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/plan", f.parent.Token, nil).expect(http.StatusOK).decode(&stored)
	if len(stored.Groups) != 1 || stored.Groups[0].Title != "Tagsüber" || stored.Groups[0].ID != edited.ID {
		t.Errorf("set_plan with the group's id should rename it in place: %+v", stored.Groups)
	}
}
