package e2e

// FR-27 (the API is FR-27.3): Live. A parent or guardian keeps a phone connected and reporting its position for a while;
// the phone learns of it from its policy, and a guardian reads the session's positions and never the
// history before it.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type liveDTO struct {
	Active    bool          `json:"active"`
	LiveUntil *time.Time    `json:"live_until"`
	LiveSince *time.Time    `json:"live_since"`
	Locations []locationDTO `json:"locations"`
}

type locationDTO struct {
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	CapturedAt time.Time `json:"captured_at"`
}

func TestLiveKeepsAPhoneReportingForAWhileAndAGuardianSeesOnlyTheSession(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	dev := "/devices/" + f.device.ID
	phoneLive := func() string {
		t.Helper()
		var raw struct {
			LiveUntil *string `json:"live_until"`
		}
		h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&raw)
		if raw.LiveUntil == nil {
			t.Fatal("the policy carries no live_until at all; an older reader could not tell off from absent")
		}
		return *raw.LiveUntil
	}
	report := func(lat float64) {
		h.call(http.MethodPost, "/device/location", f.deviceToken(), map[string]any{
			"latitude": lat, "longitude": 8.5, "accuracy_m": 8,
		}).expect(http.StatusOK)
	}

	if got := phoneLive(); got != "" {
		t.Fatalf("a phone nobody put in Live is told live_until=%q", got)
	}
	// History from before any session: a guardian must never see it.
	report(47.1)
	time.Sleep(1100 * time.Millisecond)

	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	var started liveDTO
	h.call(http.MethodPost, dev+"/live", guardian.Token, map[string]any{}).expect(http.StatusOK).decode(&started)
	if !started.Active || started.LiveUntil == nil || started.LiveSince == nil {
		t.Fatalf("a guardian started Live and got %+v", started)
	}
	if d := time.Until(*started.LiveUntil); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("Live without minutes lasts %v; the default is 30 minutes", d)
	}
	until := phoneLive()
	if at, err := time.Parse(time.RFC3339, until); err != nil || at.Sub(*started.LiveUntil).Abs() > time.Second {
		t.Errorf("the phone is told live_until=%q, the session ends %v", until, started.LiveUntil)
	}

	report(47.2)
	report(47.3)
	var seen liveDTO
	h.call(http.MethodGet, dev+"/live", guardian.Token, nil).expect(http.StatusOK).decode(&seen)
	if len(seen.Locations) != 2 || seen.Locations[0].Latitude != 47.3 {
		t.Errorf("the guardian sees %+v; want the session's two positions, newest first, and not the one before it", seen.Locations)
	}
	// The history itself stays an admin's.
	h.call(http.MethodGet, dev+"/locations", guardian.Token, nil).expect(http.StatusForbidden)

	// Extending keeps the session's start, so the walk home is not cut in two.
	var extended liveDTO
	h.call(http.MethodPost, dev+"/live", f.parent.Token, map[string]any{"minutes": 60}).expect(http.StatusOK).decode(&extended)
	if extended.LiveSince == nil || !extended.LiveSince.Equal(*started.LiveSince) {
		t.Errorf("extending moved the session's start from %v to %v", started.LiveSince, extended.LiveSince)
	}

	for _, bad := range []int{0, 121, -5} {
		h.call(http.MethodPost, dev+"/live", f.parent.Token, map[string]any{"minutes": bad}).
			expectError(http.StatusBadRequest, "invalid_input")
	}

	var stopped liveDTO
	h.call(http.MethodDelete, dev+"/live", guardian.Token, nil).expect(http.StatusOK).decode(&stopped)
	if stopped.Active {
		t.Errorf("stopped Live is still active: %+v", stopped)
	}
	if got := phoneLive(); got != "" {
		t.Errorf("after Stop the phone is still told live_until=%q", got)
	}
	// The session just ended stays readable until the next one begins.
	h.call(http.MethodGet, dev+"/live", guardian.Token, nil).expect(http.StatusOK).decode(&seen)
	if len(seen.Locations) != 2 {
		t.Errorf("after Stop the guardian sees %d of the session's positions, want 2", len(seen.Locations))
	}

	h.call(http.MethodPost, "/devices/00000000-0000-0000-0000-000000000001/live", f.parent.Token, map[string]any{}).
		expect(http.StatusNotFound)
}

func TestFgctlAndMCPStartAndStopLive(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-live")}
	home := t.TempDir()
	read := func() liveDTO {
		var l liveDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/live", f.parent.Token, nil).expect(http.StatusOK).decode(&l)
		return l
	}

	if r := fgctlRun(t, home, env, "live", f.device.ID); r.code != 0 || !strings.Contains(r.stdout, "not live") {
		t.Errorf("fgctl live on a phone not in Live: exit %d %q", r.code, r.stdout)
	}
	if r := fgctlRun(t, home, env, "live", f.device.ID, "--start", "--minutes", "15"); r.code != 0 || !strings.Contains(r.stdout, "live until") {
		t.Fatalf("fgctl live --start: exit %d %q %q", r.code, r.stdout, r.stderr)
	}
	if l := read(); !l.Active || l.LiveUntil == nil || time.Until(*l.LiveUntil) > 16*time.Minute || time.Until(*l.LiveUntil) < 14*time.Minute {
		t.Errorf("fgctl live --start --minutes 15 answered 0 and the server holds %+v", l)
	}
	if r := fgctlRun(t, home, env, "live", f.device.ID, "--minutes", "15"); r.code == 0 {
		t.Errorf("--minutes without --start was accepted: %q", r.stdout)
	}
	if r := fgctlRun(t, home, env, "live", f.device.ID, "--stop"); r.code != 0 {
		t.Fatalf("fgctl live --stop: exit %d %q", r.code, r.stderr)
	}
	if l := read(); l.Active {
		t.Errorf("fgctl live --stop answered 0 and Live still runs: %+v", l)
	}

	session := startMCP(t, t.TempDir(), env)
	defer session.close()
	session.call(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "1"},
	})
	session.notify(t, "notifications/initialized", map[string]any{})
	tool := func(name string, args map[string]any) (string, bool) {
		out := session.call(t, "tools/call", map[string]any{"name": name, "arguments": args})
		isError, _ := out["isError"].(bool)
		return mcpText(t, out), isError
	}
	if text, isErr := tool("start_live", map[string]any{"device_id": f.device.ID}); isErr {
		t.Fatalf("start_live: %s", text)
	}
	if l := read(); !l.Active || time.Until(*l.LiveUntil) < 29*time.Minute {
		t.Errorf("start_live without minutes: the server holds %+v; want 30 minutes", l)
	}
	if text, isErr := tool("get_live", map[string]any{"device_id": f.device.ID}); isErr || !strings.Contains(text, `"active": true`) {
		t.Errorf("get_live: error=%v %s", isErr, text)
	}
	if text, isErr := tool("stop_live", map[string]any{"device_id": f.device.ID}); isErr {
		t.Fatalf("stop_live: %s", text)
	}
	if l := read(); l.Active {
		t.Errorf("stop_live answered and Live still runs")
	}
}

// FR-26.1: the server says whether a phone holds its stream right now, which is how the console tells
// a phone that is asleep between polls from one that is listening, and how the device test measures
// the modes without trusting the phone's own log.
func TestTheDeviceViewSaysWhetherThePhoneHoldsItsStream(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	open := func() bool {
		return h.deviceView(f.parent.Token, f.device.ID).StreamOpen
	}
	listed := func() bool {
		var list struct {
			Devices []struct {
				StreamOpen bool `json:"stream_open"`
			} `json:"devices"`
		}
		h.call(http.MethodGet, "/devices?child_id="+f.child.ID, f.parent.Token, nil).expect(http.StatusOK).decode(&list)
		return len(list.Devices) == 1 && list.Devices[0].StreamOpen
	}
	if open() || listed() {
		t.Fatal("a phone with no stream is reported as holding one")
	}
	stream := h.openStream("/device/stream", f.deviceToken())
	deadline := time.Now().Add(5 * time.Second)
	for !open() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !open() || !listed() {
		t.Errorf("the phone holds its stream and the views say open=%v listed=%v", open(), listed())
	}
	stream.Close()
	deadline = time.Now().Add(5 * time.Second)
	for open() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if open() {
		t.Error("the phone let go of its stream and the view still says it holds one")
	}
	// Online is the stream OR a heartbeat within two passive polls and a margin (FR-26.2): a phone
	// that last checked in 5 minutes ago is resting, one silent for 12 is offline.
	online := func() bool { return h.deviceView(f.parent.Token, f.device.ID).State.Online }
	h.fixture("UPDATE device_state SET last_seen_at = NOW() - interval '5 minutes' WHERE device_id = '" + f.device.ID + "'")
	if !online() {
		t.Error("a phone that checked in 5 minutes ago, between two polls, is reported offline")
	}
	h.fixture("UPDATE device_state SET last_seen_at = NOW() - interval '12 minutes' WHERE device_id = '" + f.device.ID + "'")
	if online() {
		t.Error("a phone silent for 12 minutes — two missed polls — is reported online")
	}
}
