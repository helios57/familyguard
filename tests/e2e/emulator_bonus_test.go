package e2e

// FR-22.6 on a real Android device: a bonus app is suspended while there is no earned time and opens
// once a parent's confirmation earns some — read back from the package manager, which is what the
// launcher obeys. Driven by tests/android/bonus.sh; run on its own it SKIPS.
//
// Everything else about earned time is proven on the server and in the two engines' shared vectors.
// What only a device can show is that the phone, told a new balance by the server, actually changes
// the platform's suspension — with no screen tap in between.

import (
	"net/http"
	"testing"
	"time"
)

func TestABonusAppOpensOnlyWithEarnedTimeOnARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, _, target := managedOnEmulator(t, h, d)

	// Positive control: a preinstalled app with a launcher entry is free by default, so it is usable
	// before anything is decided. Without this, an app suspended for another reason would make the
	// bonus rule look applied.
	d.awaitSuspended(t, target, "false", 2*time.Minute)

	// Marked a bonus app with nothing earned: paused in the middle of the day.
	h.call(http.MethodPut, "/children/"+child.ID+"/app-rules", parent.Token,
		map[string]any{"package_name": target, "action": "BONUS"}).expect(http.StatusOK)
	d.awaitSuspended(t, target, "true", 2*time.Minute)

	// A one-task group, confirmed by a parent: 30 minutes earned, and the app opens.
	plan := h.putPlan(parent.Token, child.ID, []planGroupDTO{allDay("Tag", 30, "Katze füttern")})
	task := plan[0].Tasks[0].ID
	decide := func(decision string) {
		t.Helper()
		h.call(http.MethodPost, "/children/"+child.ID+"/tasks/"+task+"/decision", parent.Token,
			map[string]any{"decision": decision}).expect(http.StatusOK)
	}
	decide("confirm")
	d.awaitSuspended(t, target, "false", 2*time.Minute)

	// Undone: the credit is withdrawn and the app is paused again.
	decide("undo")
	d.awaitSuspended(t, target, "true", 2*time.Minute)
}
