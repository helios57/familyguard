package httpapi

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
