package e2e

// MCP covers everything the console can (owner, 2026-10-01). The unit ratchet in cmd/fgctl holds
// every parent route to a tool; this drives the tools that came with it through the real `fgctl mcp`
// with an API key, against the real server, and reads each effect back from the API — the way the
// console's own tests read back what a button did.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPDoesWhatTheConsoleDoes(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)
	key := mintKey(t, h, "e2e-mcp-console")
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": key}
	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})
	tool := func(name string, args map[string]any) map[string]any {
		t.Helper()
		out := session.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
		text := mcpText(t, out)
		if isError, _ := out["isError"].(bool); isError {
			t.Fatalf("%s %v: %s", name, args, text)
		}
		var decoded map[string]any
		_ = json.Unmarshal([]byte(text), &decoded)
		return decoded
	}
	refused := func(name string, args map[string]any, want string) {
		t.Helper()
		out := session.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
		if isError, _ := out["isError"].(bool); !isError || !strings.Contains(mcpText(t, out), want) {
			t.Errorf("%s %v: want a refusal naming %q, got %s", name, args, want, mcpText(t, out))
		}
	}

	// ---- a child, its rules ----
	child := tool("create_child", map[string]any{"name": "Mira", "birth_year": 2015})
	childID, _ := child["id"].(string)
	if childID == "" {
		t.Fatalf("create_child answered %v", child)
	}
	tool("update_child", map[string]any{"child_id": childID, "name": "Mira B."})
	tool("set_policy", map[string]any{"child_id": childID, "daily_limit_minutes": 60, "timezone": "Europe/Zurich",
		"bedtime_enabled": true, "bedtime_start": "21:30", "bedtime_end": "06:30", "youtube_blocked": true})
	refused("set_policy", map[string]any{"child_id": childID}, "nothing to change")
	var pol policyDTO
	h.call(http.MethodGet, "/children/"+childID+"/policy", parent.Token, nil).expect(http.StatusOK).decode(&pol)
	if pol.DailyLimitMinutes != 60 || !pol.BedtimeEnabled || pol.BedtimeStart != "21:30" || !pol.YouTubeBlocked {
		t.Errorf("after set_policy the server holds %+v", pol)
	}
	var children struct {
		Children []childDTO `json:"children"`
	}
	h.call(http.MethodGet, "/children", parent.Token, nil).expect(http.StatusOK).decode(&children)
	if len(children.Children) != 1 || children.Children[0].Name != "Mira B." {
		t.Errorf("after update_child the children are %+v", children.Children)
	}

	// ---- a phone, set up with the code the tool hands out ----
	dev := tool("add_device", map[string]any{"child_id": childID, "name": "Miras Handy"})
	deviceID, _ := dev["id"].(string)
	code := tool("get_setup_code", map[string]any{"device_id": deviceID})
	if svg, _ := code["svg"].(string); !strings.HasPrefix(strings.TrimSpace(svg), "<") {
		t.Errorf("get_setup_code carries no QR picture: %.80q", svg)
	}
	admin, _ := code["payload"].(map[string]any)[extraAdminExtras].(map[string]any)
	enrollToken, _ := admin["enrollment_token"].(string)
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})
	if enrolled.DeviceID != deviceID {
		t.Fatalf("the code get_setup_code handed out enrolled %s, not %s", enrolled.DeviceID, deviceID)
	}
	refused("get_setup_code", map[string]any{"device_id": deviceID}, "replace")
	tool("rename_device", map[string]any{"device_id": deviceID, "name": "Miras Pixel"})
	if v := h.deviceView(parent.Token, deviceID); v.Device.Name != "Miras Pixel" {
		t.Errorf("after rename_device the phone is called %q", v.Device.Name)
	}
	if rc := tool("get_recovery_code", map[string]any{"device_id": deviceID}); rc["recovery_code"] == "" || rc["recovery_code"] == nil {
		t.Errorf("get_recovery_code answered %v", rc)
	}
	tool("list_recovery_events", map[string]any{"device_id": deviceID})

	// ---- apps on the phone, and the rules for them ----
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken, map[string]any{"apps": []map[string]any{
		{"package_name": pkgGame, "label": "Brawl Stars", "launchable": true},
	}}).expect(http.StatusOK)
	if apps := tool("list_device_apps", map[string]any{"device_id": deviceID}); !strings.Contains(asJSON(apps), "Brawl Stars") {
		t.Errorf("list_device_apps answered %v", apps)
	}
	tool("set_app_rule", map[string]any{"child_id": childID, "package_name": pkgGame, "rule": "LIMIT", "limit_minutes": 20})
	ruleOf := func() (string, int) {
		var body struct {
			Rules []struct {
				PackageName  string `json:"package_name"`
				Action       string `json:"action"`
				LimitMinutes int    `json:"limit_minutes"`
			} `json:"rules"`
		}
		h.call(http.MethodGet, "/children/"+childID+"/app-rules", parent.Token, nil).expect(http.StatusOK).decode(&body)
		for _, r := range body.Rules {
			if r.PackageName == pkgGame {
				return r.Action, r.LimitMinutes
			}
		}
		return "", 0
	}
	if action, limit := ruleOf(); action != "LIMIT" || limit != 20 {
		t.Errorf("after set_app_rule LIMIT 20 the rule is %s %d", action, limit)
	}
	if rules := tool("list_app_rules", map[string]any{"child_id": childID}); !strings.Contains(asJSON(rules), pkgGame) {
		t.Errorf("list_app_rules answered %v", rules)
	}
	tool("set_app_rule", map[string]any{"child_id": childID, "package_name": pkgGame, "rule": "NONE"})
	if action, _ := ruleOf(); action != "" {
		t.Errorf("after set_app_rule NONE the rule is still %s", action)
	}
	refused("set_app_rule", map[string]any{"child_id": childID, "package_name": pkgGame, "rule": "SOMETIMES"}, "ALLOW, LIMIT")
	// Only an app the phone no longer has can be taken off its list: the phone reports it gone first.
	refused("forget_device_app", map[string]any{"device_id": deviceID, "package_name": pkgGame}, "still_installed")
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken, map[string]any{"apps": []map[string]any{}}).expect(http.StatusOK)
	tool("forget_device_app", map[string]any{"device_id": deviceID, "package_name": pkgGame})
	if apps := tool("list_device_apps", map[string]any{"device_id": deviceID}); strings.Contains(asJSON(apps), pkgGame) {
		t.Errorf("after forget_device_app the phone's list still holds it: %v", apps)
	}

	tool("set_blocked_domain", map[string]any{"child_id": childID, "domain": "example.com", "blocked": true})
	if d := tool("list_blocked_domains", map[string]any{"child_id": childID}); !strings.Contains(asJSON(d), "example.com") {
		t.Errorf("list_blocked_domains after blocking answered %v", d)
	}
	tool("set_blocked_domain", map[string]any{"child_id": childID, "domain": "example.com", "blocked": false})
	if d := tool("list_blocked_domains", map[string]any{"child_id": childID}); strings.Contains(asJSON(d), "example.com") {
		t.Errorf("list_blocked_domains after lifting the block answered %v", d)
	}

	tool("set_family_blocked_package", map[string]any{"package_name": "com.example.casino", "blocked": true, "reason": "Glücksspiel"})
	blocked := func() string {
		var body map[string]any
		h.call(http.MethodGet, "/family/blocked-packages", parent.Token, nil).expect(http.StatusOK).decode(&body)
		return asJSON(body)
	}
	if !strings.Contains(blocked(), "com.example.casino") {
		t.Error("set_family_blocked_package did not put the app on the family's list")
	}
	tool("set_family_blocked_package", map[string]any{"package_name": "com.example.casino", "blocked": false})
	if strings.Contains(blocked(), "com.example.casino") {
		t.Error("set_family_blocked_package blocked=false left the app on the list")
	}

	// ---- the catalog and managed apps ----
	apk := filepath.Join(t.TempDir(), "fixture.apk")
	if err := os.WriteFile(apk, fixtureAPK(t, "fixture-v1.apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploaded := tool("upload_app", map[string]any{"path": apk, "label": "Fixture"})
	appID, _ := uploaded["id"].(string)
	if appID == "" || uploaded["package_name"] != fixturePackage {
		t.Fatalf("upload_app answered %v", uploaded)
	}
	refused("upload_app", map[string]any{"path": "fixture.apk"}, "absolute")
	tool("set_managed_app", map[string]any{"child_id": childID, "package_name": fixturePackage, "managed": true})
	if m := tool("list_managed_apps", map[string]any{"child_id": childID}); !strings.Contains(asJSON(m), fixturePackage) {
		t.Errorf("list_managed_apps after set_managed_app answered %v", m)
	}
	tool("set_managed_app", map[string]any{"child_id": childID, "package_name": fixturePackage, "managed": false})
	if m := tool("list_managed_apps", map[string]any{"child_id": childID}); strings.Contains(asJSON(m), fixturePackage) {
		t.Errorf("list_managed_apps after managed=false answered %v", m)
	}
	tool("scan_apps", map[string]any{})
	tool("delete_app", map[string]any{"app_id": appID})
	var catalog struct {
		Apps []appDTO `json:"apps"`
	}
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&catalog)
	for _, a := range catalog.Apps {
		if a.ID == appID {
			t.Error("delete_app left the APK in the catalog")
		}
	}

	// ---- what the phone does, and a request for more time answered ----
	if ds := tool("get_desired_state", map[string]any{"device_id": deviceID}); !strings.Contains(asJSON(ds), `"daily_limit_minutes":60`) {
		t.Errorf("get_desired_state answered %.300s", asJSON(ds))
	}
	if tl := tool("get_timeline", map[string]any{"device_id": deviceID}); tl["screen_time"] == nil {
		t.Errorf("get_timeline answered %v", tl)
	}
	h.call(http.MethodPost, "/device/time-requests", enrolled.DeviceToken, map[string]any{"minutes": 30}).expect(http.StatusOK)
	var day struct {
		TimeRequests []timeRequestDTO `json:"time_requests"`
	}
	h.call(http.MethodGet, "/children/"+childID+"/today", parent.Token, nil).expect(http.StatusOK).decode(&day)
	if today := tool("get_today", map[string]any{"child_id": childID}); !strings.Contains(asJSON(today), day.TimeRequests[0].ID) {
		t.Errorf("get_today does not carry the request: %.300s", asJSON(today))
	}
	tool("answer_time_request", map[string]any{"child_id": childID, "request_id": day.TimeRequests[0].ID, "decision": "grant", "minutes": 20})
	if d := h.desiredState(parent.Token, deviceID, ""); d.Desired.BonusMinutes != 20 {
		t.Errorf("after answer_time_request grant 20 the day's Extrazeit is %d", d.Desired.BonusMinutes)
	}
	refused("answer_time_request", map[string]any{"child_id": childID, "request_id": day.TimeRequests[0].ID, "decision": "grant"}, "already_decided")
	refused("answer_time_request", map[string]any{"child_id": childID, "request_id": day.TimeRequests[0].ID, "decision": "perhaps"}, "grant or decline")

	// ---- the family ----
	if p := tool("list_parents", map[string]any{}); !strings.Contains(asJSON(p), primaryParent.Email) {
		t.Errorf("list_parents answered %v", p)
	}
	if k := tool("list_api_keys", map[string]any{}); !strings.Contains(asJSON(k), "e2e-mcp-console") || strings.Contains(asJSON(k), key) {
		t.Errorf("list_api_keys answered %v (it must name the key and never carry a secret)", k)
	}
	tool("get_family", map[string]any{})
	tool("get_hosted_dpc", map[string]any{})
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
