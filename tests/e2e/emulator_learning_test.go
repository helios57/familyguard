package e2e

// FR-30 on a real Android device: with the daily limit on at 0 a counted app is paused, time in a
// learning app opens it — with the phone offline, so the minutes are the phone's own — and using up
// what was learned pauses it again. Driven by tests/android/learning.sh; run on its own it SKIPS.
//
// The rules are proven on the server and in the two engines' shared vectors, and the counting in
// LearningAccountTest. What only a device can show is the platform's part: that foreground time in
// a learning app is measured by UsageStats, credited, and turned into a suspension change by the
// phone's own re-evaluation with no server to ask — read back from the package manager.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLearningTimeOpensACountedAppOfflineOnARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, learn := managedOnEmulator(t, h, d)

	// The learning app is the preinstalled one managedOnEmulator found (DeskClock), named by its full
	// package as the prefix — the emulator has no ch.* learning games, and a prefix is a prefix. The
	// counted app is Camera2, with its permissions granted so it stays in front by itself. Calendar
	// cannot be either: offline, its first launch hands over to a Google sign-in error screen in
	// another package, and nothing is credited to Calendar at all (measured on API 37, 2026-10-25).
	const target = "com.android.camera2"
	if learn == target || d.suspendedState(target) == "" {
		t.Fatalf("this image has no %s beside %s, and the proof needs both", target, learn)
	}
	// Usage access is the one setup step being device owner does not cover (DEPLOYMENT.md); without
	// it the phone measures nothing, learns nothing and says so — the first run here was exactly that.
	d.mustRun(30*time.Second, "shell", "appops", "set", "io.github.helios57.familyguard", "GET_USAGE_STATS", "allow")
	for _, perm := range []string{"CAMERA", "RECORD_AUDIO", "ACCESS_FINE_LOCATION", "ACCESS_COARSE_LOCATION", "POST_NOTIFICATIONS"} {
		d.run(30*time.Second, "shell", "pm", "grant", target, "android.permission."+perm)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for !h.deviceHasApp(parent.Token, device.ID, target) {
		if time.Now().After(deadline) {
			t.Fatalf("after 2 min %s is not in the phone's inventory", target)
		}
		time.Sleep(2 * time.Second)
	}

	// Positive control: before the limit both apps are usable, so a suspension seen later is the
	// limit's and not one left over from an earlier run.
	d.awaitSuspended(t, target, "false", 2*time.Minute)
	d.awaitSuspended(t, learn, "false", 2*time.Minute)

	h.putLearningPrefixes(parent.Token, learn).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+child.ID+"/app-rules", parent.Token,
		map[string]any{"package_name": target, "action": "LIMIT"}).expect(http.StatusOK)
	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_enabled": true, "daily_limit_minutes": 0})

	// On at 0: the counted app is paused at once, and the learning app — always free — is not.
	d.awaitSuspended(t, target, "true", 2*time.Minute)
	d.awaitSuspended(t, learn, "false", 30*time.Second)

	// Offline from here to the end, screen held on: the phone re-evaluates from its own cache only
	// while the screen is on.
	t.Cleanup(func() {
		d.run(30*time.Second, "shell", "svc", "wifi", "enable")
		d.run(30*time.Second, "shell", "svc", "data", "enable")
		d.run(30*time.Second, "shell", "svc", "power", "stayon", "false")
	})
	d.run(30*time.Second, "shell", "svc", "power", "stayon", "true")
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
	d.run(30*time.Second, "shell", "wm", "dismiss-keyguard")
	d.run(30*time.Second, "shell", "svc", "wifi", "disable")
	d.run(30*time.Second, "shell", "svc", "data", "disable")

	// Two and a half minutes in the learning app: two whole minutes learned, floored as the phone
	// floors them. Home afterwards, so the wait below does not keep learning.
	d.run(30*time.Second, "shell", "monkey", "-p", learn, "-c", "android.intent.category.LAUNCHER", "1")
	learnedFrom := time.Now()
	time.Sleep(150 * time.Second)
	if top, _ := d.run(30*time.Second, "shell", "dumpsys", "activity", "activities"); !containsResumed(top, learn) {
		t.Fatalf("after 150 s the learning app %s is not the resumed activity, so nothing was learned: %s", learn, resumedLine(top))
	}
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_HOME")
	t.Logf("in %s for %s, offline", learn, time.Since(learnedFrom).Round(time.Second))

	// The phone's own poll (every 5 min while the screen is on) turns the learned minutes into Bonuszeit.
	d.awaitSuspended(t, target, "false", 7*time.Minute)
	t.Logf("%s opened %s after learning began, with no network", target, time.Since(learnedFrom).Round(time.Second))

	// Spending it: in the counted app until the learned minutes are gone, then paused again.
	d.run(30*time.Second, "shell", "monkey", "-p", target, "-c", "android.intent.category.LAUNCHER", "1")
	spentFrom := time.Now()
	time.Sleep(5 * time.Second)
	if top, _ := d.run(30*time.Second, "shell", "dumpsys", "activity", "activities"); !containsResumed(top, target) {
		t.Fatalf("the counted app %s did not come to the front, so nothing is being spent: %s", target, resumedLine(top))
	}
	d.awaitSuspended(t, target, "true", 10*time.Minute)
	t.Logf("%s paused again %s after it was opened", target, time.Since(spentFrom).Round(time.Second))
}

// containsResumed is whether `dumpsys activity activities` names pkg as the resumed activity.
func containsResumed(dump, pkg string) bool {
	for _, line := range strings.Split(dump, "\n") {
		if strings.Contains(line, "ResumedActivity") && strings.Contains(line, pkg+"/") {
			return true
		}
	}
	return false
}

// resumedLine is the dump's first ResumedActivity line, for a failure message.
func resumedLine(dump string) string {
	for _, line := range strings.Split(dump, "\n") {
		if strings.Contains(line, "ResumedActivity") {
			return strings.TrimSpace(line)
		}
	}
	return "(no resumed activity)"
}
