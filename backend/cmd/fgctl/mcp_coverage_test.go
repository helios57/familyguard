package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/httpapi"
)

// mcpUncovered is every parent route MCP deliberately has no tool for, with the reason. The owner's
// rule (2026-10-01) is that MCP covers everything the console can; this list is what is left, and
// TestMCPCoversEveryParentRoute fails on a route that is in neither, and on an entry here that has
// gone stale. The routes the server refuses to an API key are not listed: the route table says so
// itself (httpapi.ParentRoute.APIKeyAllowed), and a tool for one could only ever fail.
var mcpUncovered = map[string]string{
	"GET /api/v1/events":                    "a stream of nudges to re-read, held open for the console; every MCP tool reads the current state when called",
	"GET /api/v1/devices/:x/debug":          "a raw adb byte stream, not a request and answer; `fgctl adb` serves it",
	"GET /api/v1/push/key":                  "a browser's own Web Push subscription (FR-28.4): an MCP client is not a browser and has no push service",
	"PUT /api/v1/push/subscription":         "a browser's own Web Push subscription (FR-28.4)",
	"POST /api/v1/push/subscription/status": "a browser's own Web Push subscription (FR-28.4)",
	"DELETE /api/v1/push/subscription":      "a browser's own Web Push subscription (FR-28.4)",
	"DELETE /api/v1/devices/:x":             "not in the console either; `fgctl rm-device --yes`, where a person types --yes",
	"DELETE /api/v1/children/:x":            "not in the console either; `fgctl rm-child --yes`, where a person types --yes",
}

const (
	placeholderID      = "11111111-2222-3333-4444-555555555555"
	placeholderPackage = "com.example.placeholder"
	placeholderDay     = "2026-10-01"
)

// mcpSamples are the calls that drive every tool, one argument set per thing the tool can do: a tool
// whose argument chooses the route (a rule of NONE deletes, a calendar of "" removes) has one per
// choice. A tool with no samples fails the test, so a new tool cannot arrive undriven.
func mcpSamples(t *testing.T) map[string][]map[string]any {
	apk := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(apk, []byte("PK not really an apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := map[string]any{"child_id": placeholderID}
	device := map[string]any{"device_id": placeholderID}
	with := func(base map[string]any, kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	none := map[string]any{}
	return map[string][]map[string]any{
		"list_children": {none}, "list_devices": {none}, "get_device": {device}, "get_policy": {child},
		"pause_profile":     {with(child, "paused", true)},
		"adjust_time_today": {with(child, "minutes", 15)},
		"get_plan":          {child},
		"set_plan":          {with(child, "groups", []any{})},
		"get_today":         {child},
		"decide_task":       {with(child, "task_id", placeholderID, "decision", "confirm")},
		"get_alarm":         {child},
		"set_alarm":         {with(child, "weekdays", []string{"06:45", "06:45", "06:45", "06:45", "06:45", "", ""})},
		"set_alarm_day":     {with(child, "day", placeholderDay, "time", "07:00"), with(child, "day", placeholderDay, "time", "clear")},
		"get_agenda":        {child},
		"set_agenda":        {with(child, "entries", []any{})},
		"get_week":          {child},
		"get_holidays":      {none},
		"set_holidays":      {{"holidays": []any{}}},
		"get_calendar":      {child},
		"set_calendar":      {with(child, "url", "https://calendar.example/x.ics"), with(child, "url", "")},
		"list_commands":     {device},
		"send_command":      {with(device, "type", "SYNC_POLICY")},
		"list_apps":         {none}, "list_blocked_packages": {none},
		"get_usage": {device}, "get_locations": {device}, "get_live": {device},
		"start_live": {device}, "stop_live": {device}, "get_energy": {device},
		"list_audit": {none}, "whoami": {none},

		"get_family": {none}, "list_parents": {none}, "list_api_keys": {none}, "get_hosted_dpc": {none},
		"create_child":        {{"name": "Mira"}},
		"update_child":        {with(child, "name", "Mira")},
		"set_policy":          {with(child, "daily_limit_minutes", 90)},
		"answer_time_request": {with(child, "request_id", placeholderID, "decision", "grant")},
		"list_app_rules":      {child},
		"set_app_rule": {with(child, "package_name", placeholderPackage, "rule", "LIMIT", "limit_minutes", 30),
			with(child, "package_name", placeholderPackage, "rule", "NONE")},
		"list_blocked_domains": {child},
		"set_blocked_domain": {with(child, "domain", "example.com", "blocked", true),
			with(child, "domain", "example.com", "blocked", false)},
		"set_family_blocked_package": {{"package_name": placeholderPackage, "blocked": true},
			{"package_name": placeholderPackage, "blocked": false}},
		"add_device":           {with(child, "name", "Mira's phone")},
		"rename_device":        {with(device, "name", "Mira's phone")},
		"get_setup_code":       {device},
		"get_recovery_code":    {device},
		"list_recovery_events": {device},
		"get_desired_state":    {device},
		"list_device_apps":     {with(device, "include_system", true)},
		"forget_device_app":    {with(device, "package_name", placeholderPackage)},
		"get_timeline":         {with(device, "day", placeholderDay)},
		"upload_app":           {{"path": apk, "label": "Test"}},
		"scan_apps":            {none},
		"delete_app":           {{"app_id": placeholderID}},
		"list_managed_apps":    {child},
		"set_managed_app": {with(child, "package_name", placeholderPackage, "managed", true),
			with(child, "package_name", placeholderPackage, "managed", false)},
	}
}

var routeParam = regexp.MustCompile(`:[a-z_]+`)

// TestMCPCoversEveryParentRoute calls every MCP tool against a server that records what it is asked,
// and holds the server's whole parent surface — read from the real router, not restated — to those
// requests: a route is covered by a tool, refused to API keys by the server, or listed in
// mcpUncovered with its reason. A route the console calls and MCP cannot reach fails here.
func TestMCPCoversEveryParentRoute(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]bool{}
	norm := strings.NewReplacer(placeholderID, ":x", placeholderPackage, ":x", placeholderDay, ":x")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.Method+" "+norm.Replace(r.URL.Path)] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "familyguard", Version: "test"}, nil)
	registerTools(server, fgclient.New(srv.URL, "test-token"))
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	samples := mcpSamples(t)
	for _, tool := range tools.Tools {
		calls, ok := samples[tool.Name]
		if !ok {
			t.Errorf("the tool %s has no sample call here, so nothing shows which route it reaches", tool.Name)
			continue
		}
		delete(samples, tool.Name)
		for _, args := range calls {
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
			if err != nil {
				t.Errorf("%s %v: %v", tool.Name, args, err)
				continue
			}
			if res.IsError {
				text := ""
				for _, c := range res.Content {
					if tc, ok := c.(*mcp.TextContent); ok {
						text += tc.Text
					}
				}
				t.Errorf("%s %v answered an error: %s", tool.Name, args, text)
			}
		}
	}
	for name := range samples {
		t.Errorf("a sample for %s, which is not a tool (any more)", name)
	}

	routes, err := httpapi.ParentRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) < 70 {
		t.Fatalf("the route table holds %d parent routes; the server has more than 70 — this would check almost nothing", len(routes))
	}
	known := map[string]bool{}
	refused := 0
	for _, r := range routes {
		key := r.Method + " " + routeParam.ReplaceAllString(r.Path, ":x")
		known[key] = true
		_, exempt := mcpUncovered[key]
		switch {
		case !r.APIKeyAllowed:
			refused++
			if asked[key] {
				t.Errorf("%s is refused to API keys, yet a tool calls it: that tool can only ever fail", key)
			}
		case asked[key] && exempt:
			t.Errorf("%s is covered by a tool and still listed as uncovered: take it off mcpUncovered", key)
		case !asked[key] && !exempt:
			t.Errorf("%s has no MCP tool and no reason in mcpUncovered: the console can do it and MCP cannot", key)
		}
	}
	if refused == 0 {
		t.Error("no route is refused to API keys: the table no longer says which, and this test would wave them through")
	}
	for key := range mcpUncovered {
		if !known[key] {
			t.Errorf("mcpUncovered lists %s, which the server no longer has", key)
		}
	}
	covered := make([]string, 0, len(asked))
	for k := range asked {
		covered = append(covered, k)
	}
	sort.Strings(covered)
	t.Logf("%d parent routes: %d reached by %d tools, %d refused to API keys, %d uncovered with a reason",
		len(routes), len(covered), len(tools.Tools), refused, len(mcpUncovered))
}
