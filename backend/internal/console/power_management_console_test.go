package console

import (
	"regexp"
	"strings"
	"testing"
)

// TestBothPowerSwitchesAreShownWhenBothAreOff pins the console's account of *why* a phone is slow.
//
// The two switches that decide whether Android lets a DPC keep its own schedule are independent and
// have independent remedies: battery optimisation (Settings → Apps → … → Battery → Unrestricted)
// and exact alarms (Settings → Apps → … → Alarms and reminders). A phone can have either off, or
// both, and fixing one does nothing for the other.
//
// This guard exists because the first version of that UI hid the second one. The exact-alarm badge
// was rendered only `&& st.power_exempt !== false`, on the reasoning that a battery-restricted app
// has its alarms deferred anyway, so leading with the smaller switch would misdirect the parent.
// That reasoning is fine for ORDERING and wrong for HIDING, and the pilot phone proved it within
// hours of the release: on 2026-09-07 at 19:00Z it reported `power_exempt=false` AND
// `exact_alarms=false` on the same heartbeat. A parent would have been shown one switch, walked to
// Settings, flipped it, waited for a fresh heartbeat, and only then learned there was a second one.
//
// Nothing was red. Both badges existed, both were individually correct, and the suppression was a
// deliberate line of code with a comment explaining itself — which is exactly the shape that no
// test catches unless a test looks at the combination.
//
// The assets come out of the embedded FS, which is the copy that ships, not a file on disk.
func TestBothPowerSwitchesAreShownWhenBothAreOff(t *testing.T) {
	raw, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("could not read the embedded console (%v): a check that scans nothing reports "+
			"clean for the same reason a passing one does", err)
	}
	js := string(raw)

	// Calibration. Every assertion below is a search, and an empty result and a search that never
	// ran are the same shape — so first prove the haystack is the file we think it is.
	for _, anchor := range []string{"power_exempt", "exact_alarms", "badge warn"} {
		if !strings.Contains(js, anchor) {
			t.Fatalf("the embedded app.js does not contain %q at all: this test is scanning the "+
				"wrong file, and every check below would pass vacuously", anchor)
		}
	}

	// 1. Neither switch's badge may be conditioned on the other. This is the defect itself: any
	//    occurrence of one field's name inside the other's render condition re-creates it.
	badge := regexp.MustCompile(`st\.(power_exempt|exact_alarms) === false\s*\n?\s*&&[^;,]*`)
	for _, m := range badge.FindAllString(js, -1) {
		field := "exact_alarms"
		other := "power_exempt"
		if strings.Contains(m, "st.power_exempt === false") {
			field, other = other, field
		}
		if strings.Contains(m, "st."+other) {
			t.Errorf("the %s badge is suppressed by %s:\n\t%s\n"+
				"The two switches are independent and have independent remedies. Ordering them is "+
				"fine; hiding one means the parent fixes the first, waits for a heartbeat, and only "+
				"then discovers the second.", field, other, strings.TrimSpace(m))
		}
	}

	// 2. Both remedies must be reachable in the text. A badge that names a problem without naming
	//    the setting that fixes it sends the parent to a search engine.
	for _, remedy := range []string{
		`Battery \u2192 Unrestricted`,
		`Alarms and reminders`,
	} {
		if !strings.Contains(js, remedy) {
			t.Errorf("the console never tells a parent where to find %q. FamilyGuard cannot grant "+
				"either switch itself \u2014 there is no device-owner API for them \u2014 so the "+
				"instruction is the entire remedy.", remedy)
		}
	}

	// 3. The three-valued rule. `null` is a phone that has not said, and an older DPC sends neither
	//    field; a truthy test would put a warning on every such device the day this ships. Only an
	//    explicit `=== false` distinguishes "measured restricted" from "not reported".
	for _, field := range []string{"power_exempt", "exact_alarms"} {
		truthy := regexp.MustCompile(`!\s*st\.` + field + `\b|st\.` + field + `\s*(\?|&&|\|\|)`)
		if m := truthy.FindString(js); m != "" {
			t.Errorf("%s is tested for truthiness (%q) rather than `=== false`. nil means the phone "+
				"has not said, and warning there is an alarm about a device nothing is wrong with "+
				"\u2014 the kind that teaches a parent to ignore the badge.", field, m)
		}
	}
}
