package console

import (
	"regexp"
	"strings"
	"testing"
)

// TestTheAdFilterIsReportedOnlyFromMeasurements pins the three-valued rule on the one thing the
// phone reports about its filter that a parent will act on.
//
// `ad_filter_running` has three states and they mean different things: `true` is a tunnel the phone
// has confirmed is up, `false` is one it says is down, and ABSENT is a phone that has not said —
// an older DPC, or the Play build, which carries no filter at all and must never be drawn as a
// filter that is switched off. A truthy test folds the last two together, which puts a warning on
// every device that cannot have the feature and teaches the parent to ignore the badge.
//
// This is the same rule `TestBothPowerSwitchesAreShownWhenBothAreOff` pins for `power_exempt` and
// `exact_alarms`, and it is here rather than inside that test because the third state has a
// different cause: those two are switches a person flips, this one is a build that has no switch.
//
// The assets come out of the embedded FS, which is the copy that ships, not a file on disk.
func TestTheAdFilterIsReportedOnlyFromMeasurements(t *testing.T) {
	raw, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("could not read the embedded console (%v): a check that scans nothing reports "+
			"clean for the same reason a passing one does", err)
	}
	js := string(raw)

	// Calibration first: an empty result and a search that never ran are the same shape.
	for _, anchor := range []string{"ad_filter_running", "ad_filter_rules", "ad_filter_list_url", "badge warn"} {
		if !strings.Contains(js, anchor) {
			t.Fatalf("the embedded app.js does not contain %q at all: this test is scanning the "+
				"wrong file, and every check below would pass vacuously", anchor)
		}
	}

	// 1. Never truthiness. `=== true` and `=== false` are the only readings that keep "not
	//    reported" apart from "reported off".
	truthy := regexp.MustCompile(`!\s*st\.ad_filter_running\b|st\.ad_filter_running\s*(\?|&&|\|\|)`)
	if m := truthy.FindString(js); m != "" {
		t.Errorf("ad_filter_running is tested for truthiness (%q) rather than `=== true` / `=== false`. "+
			"A phone that has not reported is not a phone with the filter off — the Play build "+
			"has no filter to report on at all.", m)
	}

	// 2. The warning must be conditioned on the parent having asked for the filter. "Not running"
	//    on a child whose filter is switched off is not news, and a card full of warnings about
	//    settings nobody turned on is a card nobody reads.
	//
	//    Read backwards from the badge's own text, because the guard sits BEFORE the state test in
	//    the expression — a forward scan from `ad_filter_running` would sail straight past it and
	//    report a defect that is not there.
	const notRunning = "ad filter not running"
	at := strings.Index(js, notRunning)
	if at < 0 {
		t.Fatalf("the console never says %q, so there is no badge to judge", notRunning)
	}
	before := js[max(0, at-400):at]
	if !strings.Contains(before, "desired.ad_filter") {
		t.Errorf("the %q badge is drawn without checking that the filter was asked for:\n\t%s\n"+
			"A warning about a setting nobody turned on is a warning a parent learns to skip.",
			notRunning, strings.TrimSpace(before))
	}
	if !strings.Contains(before, "st.ad_filter_running === false") {
		t.Errorf("the %q badge is not conditioned on a measured false:\n\t%s", notRunning,
			strings.TrimSpace(before))
	}

	// 3. A parent must be able to switch it on AND give it a list. The switch alone filters
	//    nothing: the engine refuses to run without a list url, so a console offering only the
	//    toggle would show a setting that is on and a phone that is not filtering, with no way to
	//    tell why.
	for _, control := range []string{"'ad_filter'", "ad_filter_list_url:"} {
		if !strings.Contains(js, control) {
			t.Errorf("the console has no control that sets %s. The filter needs both — a "+
				"switch with no list is a switch that does nothing.", control)
		}
	}

	// 4. And it must offer a list, because a parent does not know one by heart. The URL is all
	//    this project ships: the lists themselves are GPL-3.0 and FamilyGuard is MIT.
	if !strings.Contains(js, "https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt") {
		t.Error("the console names no filter list at all, so the field is a blank box with no " +
			"discoverable answer")
	}
}
