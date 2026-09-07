package e2e

// Whether Android is letting a phone's DPC keep its own schedule, and whether the console can tell.
//
// This exists because of a failure with no error in it. Measured on the pilot phone 2026-09-07: the
// DPC's 15-minute update check fired 6m51s, 21m44s and 8m20s late; and the event stream's
// one-second reconnect took 83 s to 495 s over five sleeping cycles, against 1.5 s while it was
// awake. A parent pressed Ring and waited two minutes. Nothing was red on either side — a deferred
// alarm is not an error, it just happens later, and from the server a battery-restricted phone and
// a phone with no signal are the same shape.
//
// So the flag is three-valued, exactly like usage_access, and this test is about all three: nil is
// a phone that has not said (every older DPC), false is a measured restriction, true is a phone
// that is free to run. It also asserts the two fields stay independent, because they are two
// different switches with two different remedies — and only one of them exists at the API 29 floor.

import (
	"net/http"
	"testing"
)

func TestAPhoneWhoseSchedulingIsRestrictedSaysSo(t *testing.T) {
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

	// Every heartbeat an older DPC sends. Neither field may be invented from silence: doing so
	// would put a "battery restricted" warning on every phone in the fleet the day this ships.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if st := state(); st.PowerExempt != nil || st.ExactAlarms != nil {
		t.Fatalf("a heartbeat that said nothing was recorded as power_exempt=%v exact_alarms=%v",
			st.PowerExempt, st.ExactAlarms)
	}

	// The phone reports the restriction that explains a late Ring. This is what the console draws.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, map[string]any{
		"connectivity": "wifi", "power_exempt": false, "exact_alarms": true,
	}).expect(http.StatusOK)
	st := state()
	if st.PowerExempt == nil || *st.PowerExempt {
		t.Fatalf("the phone reported battery restriction and the server holds %v", st.PowerExempt)
	}
	// Independent, not one flag wearing two names. An app can hold exact alarms and still have
	// every one of them deferred by Doze, which is precisely the pilot phone's state.
	if st.ExactAlarms == nil || !*st.ExactAlarms {
		t.Fatalf("exact_alarms was reported true and the server holds %v", st.ExactAlarms)
	}

	// An older DPC heartbeating in between must not erase it — the fleet this feature exists to
	// diagnose is exactly the fleet that will also be running builds too old to report.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken,
		map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if got := state().PowerExempt; got == nil || *got {
		t.Fatalf("a heartbeat that omitted the field cleared it: %v", got)
	}

	// And it clears when the setting is changed, because a warning that cannot go away is one a
	// parent learns to skip past. This is also the assertion that makes the A/B measurable: the
	// console has to follow the switch in both directions or it cannot show that flipping it worked.
	h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, map[string]any{
		"connectivity": "wifi", "power_exempt": true, "exact_alarms": false,
	}).expect(http.StatusOK)
	st = state()
	if st.PowerExempt == nil || !*st.PowerExempt {
		t.Fatalf("the phone reported the exemption and the server holds %v", st.PowerExempt)
	}
	if st.ExactAlarms == nil || *st.ExactAlarms {
		t.Fatalf("the phone reported exact alarms denied and the server holds %v", st.ExactAlarms)
	}

	// The list endpoint is a different query from the one above, and it is the one the console
	// actually reads for the device cards. A field carried by only one of them renders nowhere.
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
		if d.State.PowerExempt == nil || !*d.State.PowerExempt {
			t.Fatalf("the device list does not carry power_exempt: %v", d.State.PowerExempt)
		}
		if d.State.ExactAlarms == nil || *d.State.ExactAlarms {
			t.Fatalf("the device list does not carry exact_alarms: %v", d.State.ExactAlarms)
		}
	}
	if !found {
		t.Fatalf("the device list does not contain %s", f.device.ID)
	}
}
