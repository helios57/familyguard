package e2e

// FR-9.4 on a real Android device: a ringing siren keeps ringing untouched and through a display
// timeout while another screen covers it, and stops within seconds
// of a volume key, of the side key, and — from API 36 — of a volume key while the Stopp screen does
// not hold the focus. Driven by tests/android/siren.sh; run on its own it SKIPS.
//
// The press rules are unit-tested in SirenKeysTest. What only a device can show is the platform's
// part: that the Stopp screen comes up, that an injected key reaches it, that the side key shows up
// as the display going off, and that the tone actually ends — read from dumpsys audio, never from
// the app's own claim.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAnyButtonStopsTheSirenOnARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, _, device, _ := managedOnEmulator(t, h, d)
	t.Cleanup(func() { d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP") })
	// The emulator's software-rendered launcher and System UI raise "Application Not Responding" often
	// enough to hold the focus at a press (2 of 6 runs measured), which is a question about this host,
	// not about the phone. The cases below are about a key reaching the Stopp screen; the one where
	// something else holds the focus is the shade case, pulled down on purpose. So no error dialogs
	// while this runs, and the previous value back afterwards.
	hide := strings.TrimSpace(d.mustRun(30*time.Second, "shell", "settings", "get", "global", "hide_error_dialogs"))
	d.mustRun(30*time.Second, "shell", "settings", "put", "global", "hide_error_dialogs", "1")
	t.Cleanup(func() {
		if hide == "null" || hide == "" {
			d.run(30*time.Second, "shell", "settings", "delete", "global", "hide_error_dialogs")
		} else {
			d.run(30*time.Second, "shell", "settings", "put", "global", "hide_error_dialogs", hide)
		}
	})

	ringing := func() bool {
		audio, _ := d.run(30*time.Second, "shell", "dumpsys", "audio")
		for _, line := range strings.Split(audio, "\n") {
			if strings.Contains(line, "USAGE_ALARM") && strings.Contains(line, "state:started") {
				return true
			}
		}
		return false
	}
	stopScreenUp := func() bool {
		top, _ := d.run(30*time.Second, "shell", "dumpsys", "activity", "activities")
		return containsResumed(top, "io.github.helios57.familyguard") && strings.Contains(top, ".commands.SirenActivity")
	}
	ring := func(what string) {
		t.Helper()
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
		d.run(30*time.Second, "shell", "wm", "dismiss-keyguard")
		h.issueCommand(parent.Token, device.ID, "TRIGGER_ALARM", map[string]any{"seconds": 120})
		deadline := time.Now().Add(60 * time.Second)
		for !(ringing() && stopScreenUp()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: 60 s after TRIGGER_ALARM the alarm player is started=%v and the Stopp screen is up=%v", what, ringing(), stopScreenUp())
			}
			time.Sleep(time.Second)
		}
	}
	// focus is the window that receives a key: the Stopp screen sees a volume key only while it holds
	// it, so a press that missed says where it went.
	focus := func() string {
		win, _ := d.run(30*time.Second, "shell", "dumpsys", "window")
		for _, line := range strings.Split(win, "\n") {
			if strings.Contains(line, "mCurrentFocus") {
				return strings.TrimSpace(line)
			}
		}
		return "(no mCurrentFocus)"
	}
	press := func(key string) string {
		t.Helper()
		before := focus()
		d.run(30*time.Second, "shell", "input", "keyevent", key)
		return before
	}
	awaitSilence := func(what, focused string, within time.Duration) {
		t.Helper()
		pressed := time.Now()
		for ringing() || stopScreenUp() {
			if time.Since(pressed) > within {
				t.Fatalf("%s: %s after the press the alarm player is started=%v and the Stopp screen is up=%v; focus at the press: %s",
					what, within, ringing(), stopScreenUp(), focused)
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("%s: silent %s after the press; focus at the press: %s", what, time.Since(pressed).Round(100*time.Millisecond), focused)
	}

	// Negative control: nobody touches it, and ten seconds later it is still ringing. Without this,
	// a siren that ended by itself would read as stopped by a key.
	ring("untouched")
	time.Sleep(10 * time.Second)
	if !ringing() || !stopScreenUp() {
		t.Fatalf("untouched, 10 s in: the alarm player is started=%v and the Stopp screen is up=%v", ringing(), stopScreenUp())
	}

	// Volume down, three seconds in — past the grace that only the display-on rule has.
	time.Sleep(3 * time.Second)
	awaitSilence("volume down", press("KEYCODE_VOLUME_DOWN"), 5*time.Second)

	// The side key, with the Stopp screen holding the display on: it can only arrive as display-off.
	ring("side key")
	time.Sleep(3 * time.Second)
	awaitSilence("side key", press("KEYCODE_POWER"), 5*time.Second)

	// Nobody presses anything: another screen comes up over the Stopp screen without a user's leave
	// hint (FLAG_ACTIVITY_NO_USER_ACTION, as an incoming call's screen can), the display then times
	// out on its own, and the siren must ring on — a display going off counts only while the Stopp
	// screen holds it on. Then the side key, which turns the display on, stops it.
	// The emulator stays awake while plugged in and never times out, so both are lowered for this case
	// and put back afterwards.
	timeout := strings.TrimSpace(d.mustRun(30*time.Second, "shell", "settings", "get", "system", "screen_off_timeout"))
	plugged := strings.TrimSpace(d.mustRun(30*time.Second, "shell", "settings", "get", "global", "stay_on_while_plugged_in"))
	d.mustRun(30*time.Second, "shell", "settings", "put", "system", "screen_off_timeout", "15000")
	d.mustRun(30*time.Second, "shell", "settings", "put", "global", "stay_on_while_plugged_in", "0")
	t.Cleanup(func() {
		d.run(30*time.Second, "shell", "settings", "put", "system", "screen_off_timeout", timeout)
		d.run(30*time.Second, "shell", "settings", "put", "global", "stay_on_while_plugged_in", plugged)
	})
	// The power manager's own word for it; a missing line is "on", so a format this does not know
	// keeps the wait below waiting and fails it, never passes it.
	displayOn := func() bool {
		power, _ := d.run(30*time.Second, "shell", "dumpsys", "power")
		for _, line := range strings.Split(power, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "mWakefulness="); ok {
				return strings.TrimSpace(v) == "Awake"
			}
		}
		return true
	}
	ring("covered, display times out")
	time.Sleep(3 * time.Second)
	d.mustRun(30*time.Second, "shell", "am", "start", "-f", "0x00040000", "-a", "android.settings.SETTINGS")
	time.Sleep(2 * time.Second)
	if f := focus(); strings.Contains(f, ".commands.SirenActivity") {
		t.Fatalf("covered: the Stopp screen still holds the focus (%s), so this case would prove nothing", f)
	}
	if !ringing() {
		t.Fatalf("covered: the siren stopped when another screen came up over it, with no key pressed")
	}
	deadline := time.Now().Add(60 * time.Second)
	for displayOn() {
		if time.Now().After(deadline) {
			t.Fatalf("covered: the display was still on 60 s after a 15 s timeout, so this case would prove nothing")
		}
		time.Sleep(time.Second)
	}
	time.Sleep(3 * time.Second)
	if !ringing() {
		t.Fatalf("covered: the siren stopped when the display timed out on its own — no key was pressed")
	}
	t.Logf("covered, display timed out: still ringing 3 s later")
	awaitSilence("covered, side key", press("KEYCODE_POWER"), 5*time.Second)

	// A volume key while something else holds the focus — here the notification shade, pulled down
	// over the Stopp screen. The key never reaches the screen then; it must stop the siren anyway.
	// Measured: the emulator launcher's ANR dialog took the focus this way in 2 of 6 runs, and the
	// key went to the dialog.
	// Only from API 36: below it the platform hands a key to a remote-volume session only when the
	// maker enabled config_volumeAdjustmentForRemoteGroupSessions or the app owns a MediaRouter2
	// routing session (MediaSessionRecord.canHandleVolumeKey, android13…15-release), and the stock
	// image does neither — measured on API 33: "No routing session for io.github.helios57.familyguard".
	sdk := strings.TrimSpace(d.mustRun(30*time.Second, "shell", "getprop", "ro.build.version.sdk"))
	if level, err := strconv.Atoi(sdk); err != nil || level < 36 {
		t.Logf("NOT MEASURED: volume, shade down — API %s; the platform gives a volume key to a remote-volume session only from API 36", sdk)
		return
	}
	ring("volume, shade down")
	time.Sleep(3 * time.Second)
	d.run(30*time.Second, "shell", "cmd", "statusbar", "expand-notifications")
	time.Sleep(2 * time.Second)
	focused := focus()
	if strings.Contains(focused, ".commands.SirenActivity") {
		t.Fatalf("volume, shade down: the Stopp screen still holds the focus (%s), so this case would prove nothing", focused)
	}
	awaitSilence("volume, shade down", press("KEYCODE_VOLUME_UP"), 5*time.Second)
	d.run(30*time.Second, "shell", "cmd", "statusbar", "collapse")
}
