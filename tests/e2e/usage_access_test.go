package e2e

// FR-3.6: a phone that cannot measure screen time says so, and the console can tell.
//
// This is the most expensive silent failure in the product. PACKAGE_USAGE_STATS is an appop, not a
// runtime permission: no device-owner API grants it, so it is turned on by hand in Settings — and
// until it is, every usage query on the phone returns nothing. Every app reads zero minutes, no
// daily limit is ever reached, and the console shows a child who spent the day off their phone. A
// parent has no way to tell that from the real thing.
//
// So the flag is three-valued and the test is about all three: nil is a phone that has not said,
// false is a measured "this device can see nothing", true is a working one. Collapsing nil into
// false would put a warning on every phone running an older DPC; collapsing it into true would
// hide the failure this exists to surface.

import (
	"net/http"
	"testing"
)

func TestAPhoneThatCannotMeasureScreenTimeSaysSo(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	state := func() deviceStateDTO {
		t.Helper()
		var view deviceViewDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).
			expect(http.StatusOK).decode(&view)
		if view.State == nil {
			t.Fatal("the device has no state row")
		}
		return *view.State
	}

	// A heartbeat that omits the field, which is every heartbeat an older DPC sends.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if got := state().UsageAccess; got != nil {
		t.Fatalf("a heartbeat that said nothing about usage access was recorded as %v", *got)
	}

	// Now the phone says it cannot see usage. This is the one the console draws a warning from.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi", "usage_access": false}).expect(http.StatusOK)
	got := state().UsageAccess
	if got == nil || *got {
		t.Fatalf("the phone reported no usage access and the server holds %v", got)
	}

	// An older DPC heartbeating in between must not erase it. Absence is "did not say", and
	// treating it as "no longer reporting a problem" would clear the warning on the one phone that
	// still has the problem.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if got := state().UsageAccess; got == nil || *got {
		t.Fatalf("a heartbeat that omitted the field cleared it: %v", got)
	}

	// And it clears when the grant arrives, because a warning that cannot go away is one a parent
	// learns to skip past.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi", "usage_access": true}).expect(http.StatusOK)
	if got := state().UsageAccess; got == nil || !*got {
		t.Fatalf("the phone reported the grant and the server holds %v", got)
	}

	// The list endpoint is what the console actually reads for the device cards, and it is a
	// different query from the one above. A field that only the single-device endpoint carried
	// would render nowhere.
	var list struct {
		Devices []struct {
			ID    string         `json:"id"`
			State deviceStateDTO `json:"state"`
		} `json:"devices"`
	}
	h.call(http.MethodGet, "/devices?child_id="+f.child.ID, f.parent.Token, nil).
		expect(http.StatusOK).decode(&list)
	found := false
	for _, d := range list.Devices {
		if d.ID != f.device.ID {
			continue
		}
		found = true
		if d.State.UsageAccess == nil || !*d.State.UsageAccess {
			t.Fatalf("the device list does not carry usage access: %v", d.State.UsageAccess)
		}
	}
	if !found {
		t.Fatalf("the device list does not contain %s", f.device.ID)
	}
}
