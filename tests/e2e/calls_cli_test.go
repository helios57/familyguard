package e2e

// FR-31 from fgctl and MCP: the family numbers set and read where the owner sets them, the rejected
// calls listed, and the switch reachable from set_policy.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFgctlFamilyNumbersAndRejectedCalls(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	home := t.TempDir()
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-calls")}

	if r := fgctlRun(t, home, env, "family-numbers"); r.code != 0 || !strings.Contains(r.stdout, "no family numbers") {
		t.Fatalf("a new family: exit %d, %q", r.code, r.stdout+r.stderr)
	}
	if r := fgctlRun(t, home, env, "family-numbers", "--set", "+1 555 0101,+1 (555) 0102"); r.code != 0 {
		t.Fatalf("--set exited %d: %s", r.code, r.stderr)
	}
	if got := h.familyNumbers(f.parent.Token).Numbers; !slices.Equal(got, []string{"+15550101", "+15550102"}) {
		t.Fatalf("after --set the server holds %v", got)
	}
	var out familyNumbersDTO
	if r := fgctlRun(t, home, env, "family-numbers", "--json"); json.Unmarshal([]byte(r.stdout), &out) != nil || len(out.Numbers) != 2 {
		t.Errorf("--json gives %q", r.stdout)
	}
	if bad := fgctlRun(t, home, env, "family-numbers", "--set", "12"); bad.code == 0 || !strings.Contains(bad.stderr, "invalid_input") {
		t.Errorf("an invalid number: exit %d, %q", bad.code, bad.stderr)
	}
	if r := fgctlRun(t, home, env, "family-numbers", "--clear"); r.code != 0 || len(h.familyNumbers(f.parent.Token).Numbers) != 0 {
		t.Errorf("--clear: exit %d, server holds %v", r.code, h.familyNumbers(f.parent.Token).Numbers)
	}

	if r := fgctlRun(t, home, env, "rejected-calls", f.device.ID); r.code != 0 || !strings.Contains(r.stdout, "no rejected calls") {
		t.Errorf("nothing rejected: exit %d, %q", r.code, r.stdout+r.stderr)
	}
	h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), map[string]any{
		"calls": []map[string]any{{"id": "c-1", "number": "+15550199", "at": time.Now().UTC().Format(time.RFC3339)}},
	}).expect(http.StatusOK)
	if r := fgctlRun(t, home, env, "rejected-calls", f.device.ID, "30"); r.code != 0 || !strings.Contains(r.stdout, "+15550199") {
		t.Errorf("one rejected: exit %d, %q", r.code, r.stdout+r.stderr)
	}
	if bad := fgctlRun(t, home, env, "rejected-calls", f.device.ID, "0"); bad.code == 0 || !strings.Contains(bad.stderr, "1 to 365") {
		t.Errorf("days 0: exit %d, %q", bad.code, bad.stderr)
	}

	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"calls_contacts_only": true})
	if r := fgctlRun(t, home, env, "policy", f.child.ID); !strings.Contains(r.stdout, "only contacts may call") || !strings.Contains(r.stdout, "true") {
		t.Errorf("the policy does not show the switch: %q", r.stdout)
	}
}

func TestMCPContactsOnlyCalls(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-mcp-calls")}
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

	if text, isErr := tool("set_family_numbers", map[string]any{"numbers": []string{"+1 555 0101"}}); isErr {
		t.Fatalf("set_family_numbers: %s", text)
	}
	if got := h.familyNumbers(f.parent.Token).Numbers; !slices.Equal(got, []string{"+15550101"}) {
		t.Fatalf("after set_family_numbers the server holds %v", got)
	}
	if text, isErr := tool("get_family_numbers", map[string]any{}); isErr || !strings.Contains(text, "+15550101") {
		t.Errorf("get_family_numbers: %s", text)
	}
	if text, isErr := tool("set_family_numbers", map[string]any{"numbers": []string{"12"}}); !isErr || !strings.Contains(text, "invalid_input") {
		t.Errorf("an invalid number: want a refusal naming invalid_input, got %s", text)
	}

	if text, isErr := tool("set_policy", map[string]any{"child_id": f.child.ID, "calls_contacts_only": true}); isErr {
		t.Fatalf("set_policy: %s", text)
	}
	if on, _ := h.phoneCallSettings(f.deviceToken()); !on {
		t.Error("set_policy calls_contacts_only=true did not reach the phone")
	}

	h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), map[string]any{
		"calls": []map[string]any{{"id": "c-1", "number": "+15550199", "at": time.Now().UTC().Format(time.RFC3339)}},
	}).expect(http.StatusOK)
	if text, isErr := tool("get_rejected_calls", map[string]any{"device_id": f.device.ID}); isErr || !strings.Contains(text, "+15550199") {
		t.Errorf("get_rejected_calls: %s", text)
	}
}
