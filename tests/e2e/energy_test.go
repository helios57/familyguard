package e2e

// FR-26.5: a phone reports what FamilyGuard spends on it, and the server answers what that was per
// hour. The numbers are the phone's own cumulative counters; everything this test asserts is about
// turning two of them into one honest difference — and refusing to turn two that are not one run
// into a dishonest one.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type energyTotalsDTO struct {
	Minutes          float64 `json:"minutes"`
	UnpluggedMinutes float64 `json:"unplugged_minutes"`
	BatteryUsed      int     `json:"battery_used"`
	CPUMs            int64   `json:"cpu_ms"`
	RxBytes          int64   `json:"rx_bytes"`
	TxBytes          int64   `json:"tx_bytes"`
	StreamOpens      int64   `json:"stream_opens"`
	Events           int64   `json:"events"`
	Polls            int64   `json:"polls"`
	Pushes           int64   `json:"pushes"`
	OtherSyncs       int64   `json:"other_syncs"`
	ActiveMs         *int64  `json:"active_ms"`
	PassiveMs        *int64  `json:"passive_ms"`
	RouteFullMs      *int64  `json:"route_full_ms"`
	RouteDNSMs       *int64  `json:"route_dns_ms"`
}

type energyDTO struct {
	Hours   []energyTotalsDTO `json:"hours"`
	Total   energyTotalsDTO   `json:"total"`
	Samples int               `json:"samples"`
}

func TestAPhoneReportsWhatFamilyGuardSpendsAndTheServerAnswersPerHour(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	read := func() energyDTO {
		t.Helper()
		var e energyDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/energy?hours=24", f.parent.Token, nil).
			expect(http.StatusOK).decode(&e)
		return e
	}
	beat := func(body map[string]any) apiResponse {
		t.Helper()
		body["connectivity"] = "wifi"
		return h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, body)
	}
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	report := func(cpu, rx, streams, events int64) map[string]any {
		return map[string]any{
			"since": since, "cpu_ms": cpu, "rx_bytes": rx, "tx_bytes": rx / 10,
			"stream_opens": streams, "events": events, "polls": 0, "pushes": 0, "other_syncs": 1,
		}
	}

	// An older DPC: no report, no sample. A row of zeros would read as a phone that spends nothing.
	beat(map[string]any{"battery_level": 80, "charging": false}).expect(http.StatusOK)
	if e := read(); e.Samples != 0 {
		t.Fatalf("a heartbeat without energy recorded %d samples", e.Samples)
	}

	beat(map[string]any{"battery_level": 80, "charging": false, "energy": report(1000, 20_000, 1, 0)}).
		expect(http.StatusOK)
	if e := read(); e.Samples != 1 || len(e.Hours) != 0 {
		t.Fatalf("one report is no interval yet: samples %d hours %d", e.Samples, len(e.Hours))
	}

	time.Sleep(1100 * time.Millisecond)
	beat(map[string]any{"battery_level": 79, "charging": false, "energy": report(4000, 25_000, 3, 2)}).
		expect(http.StatusOK)
	e := read()
	if e.Samples != 2 || len(e.Hours) != 1 {
		t.Fatalf("two reports of one run: samples %d hours %d, want 2 and 1", e.Samples, len(e.Hours))
	}
	tot := e.Total
	if tot.CPUMs != 3000 || tot.RxBytes != 5000 || tot.TxBytes != 500 || tot.StreamOpens != 2 || tot.Events != 2 || tot.OtherSyncs != 0 {
		t.Errorf("the difference is wrong: %+v", tot)
	}
	if tot.BatteryUsed != 1 || tot.Minutes <= 0 || tot.UnpluggedMinutes != tot.Minutes {
		t.Errorf("battery %d over %v min (unplugged %v): want 1 over the whole unplugged interval",
			tot.BatteryUsed, tot.Minutes, tot.UnpluggedMinutes)
	}
	if tot.ActiveMs != nil || tot.PassiveMs != nil {
		t.Errorf("a build without modes reported active=%v passive=%v; not measured must stay null", tot.ActiveMs, tot.PassiveMs)
	}

	// The process restarted: its counters start again. Subtracting across it would be −3.9 s of CPU.
	since = time.Now().UTC().Format(time.RFC3339)
	time.Sleep(1100 * time.Millisecond)
	beat(map[string]any{"battery_level": 79, "charging": false, "energy": report(100, 0, 1, 0)}).
		expect(http.StatusOK)
	if got := read().Total; got.CPUMs != 3000 || got.StreamOpens != 2 {
		t.Errorf("a restart changed the totals to cpu %d streams %d; nothing may be subtracted across it", got.CPUMs, got.StreamOpens)
	}

	// Malformed reports are refused, and a refused heartbeat records nothing.
	before := read().Samples
	bad := report(1, 1, 1, 1)
	bad["since"] = "yesterday"
	beat(map[string]any{"energy": bad}).expectError(http.StatusBadRequest, "invalid_input")
	bad = report(-1, 1, 1, 1)
	beat(map[string]any{"energy": bad}).expectError(http.StatusBadRequest, "invalid_input")
	if after := read().Samples; after != before {
		t.Errorf("refused heartbeats recorded samples: %d -> %d", before, after)
	}

	// Admins only: the energy report is a phone's diagnostic, not today's state.
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/energy", guardian.Token, nil).expect(http.StatusForbidden)
}

// fgctl energy and MCP get_energy read the same totals, and say "not reported" for a phone that has
// not reported rather than a table of zeros.
func TestFgctlAndMCPShowTheEnergyReport(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-energy")}
	home := t.TempDir()

	if r := fgctlRun(t, home, env, "energy", f.device.ID); r.code != 0 || !strings.Contains(r.stdout, "not reported (0 samples") {
		t.Errorf("a phone with no reports: exit %d, %q; want \"not reported\"", r.code, r.stdout)
	}

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for i, cpu := range []int64{1000, 4600} {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, map[string]any{
			"connectivity": "wifi", "battery_level": 80 - i, "charging": false,
			"energy": map[string]any{"since": since, "cpu_ms": cpu, "rx_bytes": 0, "tx_bytes": 0,
				"stream_opens": 1 + 2*i, "events": 0, "polls": 0, "pushes": 0, "other_syncs": 0,
				"active_ms": 1000 * i, "passive_ms": 3000 * i, "route_full_ms": 2000 * i, "route_dns_ms": 2000 * i},
		}).expect(http.StatusOK)
	}
	r := fgctlRun(t, home, env, "energy", f.device.ID)
	if r.code != 0 || !strings.Contains(r.stdout, "3.6 s") || !strings.Contains(r.stdout, "per unplugged hour") {
		t.Errorf("fgctl energy: exit %d, %q; want the hour's 3.6 s of CPU and the battery rate", r.code, r.stdout)
	}
	if !strings.Contains(r.stdout, "resting 75 % of the time") || !strings.Contains(r.stdout, "ad filter DNS only 50 % of its time") {
		t.Errorf("fgctl energy does not say how the time was spent (75 %% resting, 50 %% DNS only): %q", r.stdout)
	}
	if r := fgctlRun(t, home, env, "energy", f.device.ID, "--json"); !strings.Contains(r.stdout, `"cpu_ms": 3600`) {
		t.Errorf("fgctl energy --json does not carry cpu_ms 3600: %q", r.stdout)
	}

	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})
	out := session.call(t, "tools/call", map[string]any{"name": "get_energy", "arguments": map[string]any{"device_id": f.device.ID}})
	if isErr, _ := out["isError"].(bool); isErr || !strings.Contains(mcpText(t, out), `"stream_opens": 2`) {
		t.Errorf("get_energy: error=%v %s", isErr, mcpText(t, out))
	}
}
