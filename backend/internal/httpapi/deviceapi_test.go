package httpapi

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The critical whitelist is the one enforcement input the managed device supplies, and an entry in
// it survives bedtime, an exhausted quota and a parent's explicit block (FR-5.5). The cap and the
// shape check are therefore the only thing standing between "this OEM calls its dialer
// com.samsung.android.dialer" and a device writing an arbitrary exemption list into its own row.
func TestCriticalPackagesAreBoundedAndShaped(t *testing.T) {
	t.Run("a real phone's report survives untouched", func(t *testing.T) {
		in := []string{
			"com.samsung.android.dialer",
			"com.samsung.android.messaging",
			"com.samsung.android.app.contacts",
			"com.android.settings",
		}
		kept, dropped := sanitizeCriticalPackages(in)
		if dropped != 0 {
			t.Fatalf("dropped %d of a plausible report: %v", dropped, kept)
		}
		if !slices.Equal(kept, in) {
			t.Fatalf("kept %v, want %v", kept, in)
		}
	})

	t.Run("what cannot be a package name is dropped", func(t *testing.T) {
		for _, bad := range []string{
			"nodots",
			"com..double",
			"com.9leading.digit",
			"com.has space",
			"https://example.com/evil",
			"../../etc/passwd",
			"com.x." + strings.Repeat("y", maxPackageNameLength),
		} {
			kept, dropped := sanitizeCriticalPackages([]string{bad})
			if dropped != 1 || len(kept) != 0 {
				t.Errorf("%q: kept %v, dropped %d — want it refused", bad, kept, dropped)
			}
		}
	})

	t.Run("the cap holds and reports what it refused", func(t *testing.T) {
		in := make([]string, 0, maxCriticalPackages+10)
		for i := range cap(in) {
			in = append(in, fmt.Sprintf("com.example.p%d", i))
		}
		kept, dropped := sanitizeCriticalPackages(in)
		if len(kept) != maxCriticalPackages {
			t.Fatalf("kept %d, want the cap of %d", len(kept), maxCriticalPackages)
		}
		if dropped != 10 {
			t.Fatalf("dropped %d, want the 10 over the cap", dropped)
		}
	})

	t.Run("an empty list is empty and never nil", func(t *testing.T) {
		// nil would reach the store as a NULL and the column is an array; the store already
		// coerces, and this keeps the two agreeing.
		kept, dropped := sanitizeCriticalPackages(nil)
		if kept == nil || len(kept) != 0 || dropped != 0 {
			t.Fatalf("kept %#v dropped %d", kept, dropped)
		}
	})

	t.Run("a blank entry is skipped without being counted as a refusal", func(t *testing.T) {
		kept, dropped := sanitizeCriticalPackages([]string{"", "  ", "com.android.dialer"})
		if dropped != 0 || !slices.Equal(kept, []string{"com.android.dialer"}) {
			t.Fatalf("kept %v dropped %d", kept, dropped)
		}
	})

	t.Run("a repeated entry is stored once", func(t *testing.T) {
		kept, _ := sanitizeCriticalPackages([]string{"com.android.dialer", "com.android.dialer"})
		if !slices.Equal(kept, []string{"com.android.dialer"}) {
			t.Fatalf("kept %v", kept)
		}
	})
}

// The three states the update-failure field has to keep apart (FR-15.7).
//
// This is the same shape as UsageAccess and it fails in the opposite direction, which is why it is
// worth its own test: collapsing nil into "" would let a DPC too old to know about the field clear
// a failure a newer one reported, on every heartbeat, forever — and the fleet this feature exists
// to fix is exactly the fleet running the older DPC.
func TestClampUpdateErrorKeepsAbsentApartFromEmpty(t *testing.T) {
	if got := clampUpdateError(nil); got != nil {
		t.Fatalf("an absent field became %q; an older DPC would erase the failure it cannot report", *got)
	}

	empty := ""
	if got := clampUpdateError(&empty); got == nil || *got != "" {
		t.Fatalf("the phone's own \"nothing to report\" must survive as \"\", got %v", got)
	}

	spaces := "   \n\t "
	if got := clampUpdateError(&spaces); got == nil || *got != "" {
		t.Fatalf("whitespace is nothing to report, got %q", *got)
	}

	real := "  Android refused the update: status=4 INSTALL_FAILED_INSUFFICIENT_STORAGE  "
	got := clampUpdateError(&real)
	if got == nil || *got != strings.TrimSpace(real) {
		t.Fatalf("a real reason was not passed through verbatim: %v", got)
	}
}

// A phone does not get to write an essay into the console, and it does not get its heartbeat
// refused for trying either — the heartbeat carries battery, connectivity and the policy version,
// and dropping all of that over a long string would turn a cosmetic problem into a device that
// falls out of sync.
func TestClampUpdateErrorTruncatesByRunesNotBytes(t *testing.T) {
	// Multi-byte on purpose: a byte-slice truncation would both cut at the wrong place and split a
	// rune, and a split rune reaches the parent's browser as U+FFFD.
	long := strings.Repeat("é", 5000)
	got := clampUpdateError(&long)
	if got == nil {
		t.Fatal("an overlong reason was dropped entirely")
	}
	if n := len([]rune(*got)); n != maxUpdateErrorRunes {
		t.Fatalf("kept %d runes, want %d", n, maxUpdateErrorRunes)
	}
	if strings.ContainsRune(*got, '�') {
		t.Fatal("the truncation split a rune")
	}

	// The negative control: something shorter than the bound is not touched at all, so the test
	// above is measuring a clamp rather than a constant.
	short := strings.Repeat("é", maxUpdateErrorRunes-1)
	if got := clampUpdateError(&short); got == nil || *got != short {
		t.Fatal("a reason inside the bound was altered")
	}
}

// TestClampRuleCountKeepsNothingApartFromZero is the same distinction as clampUpdateError's, on the
// number a parent reads as "the filter is working".
//
// Three inputs, three meanings: nil is a phone that has not said (an older DPC, or a build with no
// filter at all), a real count is a measurement, and a negative is a phone contradicting itself.
// Only the first two may reach the column. A negative stored would render as a number — "-1 rules"
// reads as a bug in the console, and "0 rules" would read as a filter that fetched an empty list,
// which is a state the device's own store refuses to reach.
func TestClampRuleCountKeepsNothingApartFromZero(t *testing.T) {
	if got := clampRuleCount(nil); got != nil {
		t.Fatalf("an absent count became %d; a phone that has not measured must not report one", *got)
	}

	negative := -1
	if got := clampRuleCount(&negative); got != nil {
		t.Fatalf("a negative count survived as %d", *got)
	}

	zero := 0
	if got := clampRuleCount(&zero); got == nil || *got != 0 {
		t.Fatalf("zero is a number the phone can legitimately send and must survive, got %v", got)
	}

	real := 181117
	if got := clampRuleCount(&real); got == nil || *got != real {
		t.Fatalf("a real count was not passed through: %v", got)
	}
}

// TestParseReportedTimeDropsWhatItCannotRead pins that an unreadable stamp becomes nothing rather
// than the zero time.
//
// The zero time is the trap: it is a valid `time.Time`, it stores without complaint, and it reaches
// the parent as "1 January year one", which reads as a defect in this console rather than as a
// phone that sent something unusable. Nil is the honest answer and the column already means
// "nothing reported".
func TestParseReportedTimeDropsWhatItCannotRead(t *testing.T) {
	for _, bad := range []*string{nil, ptr(""), ptr("   "), ptr("yesterday"), ptr("2026-09-20"), ptr("1758326400")} {
		shown := "<nil>"
		if bad != nil {
			shown = *bad
		}
		if got := parseReportedTime(bad); got != nil {
			t.Errorf("%q parsed to %s; an unreadable stamp must be dropped, not stored", shown, got)
		}
	}

	// The positive control: the format the DPC actually sends. Without it the loop above would pass
	// on a function that returned nil for everything.
	good := "2026-09-20T12:26:40Z"
	got := parseReportedTime(&good)
	if got == nil {
		t.Fatal("the format the phone sends did not parse; every assertion above is then vacuous")
	}
	if !got.Equal(time.Date(2026, 9, 20, 12, 26, 40, 0, time.UTC)) {
		t.Fatalf("parsed to %s, want 2026-09-20T12:26:40Z", got)
	}
	if surrounded := parseReportedTime(ptr("  " + good + "  ")); surrounded == nil || !surrounded.Equal(*got) {
		t.Fatalf("a stamp with whitespace around it was dropped: %v", surrounded)
	}
}
