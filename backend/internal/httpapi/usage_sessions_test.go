package httpapi

import (
	"testing"
	"time"
)

// What a phone may file as a sitting (FR-3.7).
//
// Every rule here has the same shape of consequence: a session that gets through is drawn on a
// parent's timeline as a fact about their child's day. A zero-length one draws a sliver nobody can
// read, an inverted one draws backwards, and one parked in next week sits at the top of every
// timeline until the calendar reaches it.
func TestAcceptableSessions(t *testing.T) {
	now := time.Date(2026, 9, 20, 18, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 9, 20, h, m, 0, 0, time.UTC) }
	sess := func(pkg string, from, to time.Time) usageSession {
		return usageSession{PackageName: pkg, StartedAt: from, EndedAt: to}
	}

	t.Run("a plausible afternoon survives untouched", func(t *testing.T) {
		in := []usageSession{
			sess("com.supercell.brawlstars", at(16, 10), at(16, 35)),
			sess("com.whatsapp", at(16, 35), at(16, 37)),
		}
		out := acceptableSessions(in, now)
		if len(out) != 2 {
			t.Fatalf("kept %d of 2: %+v", len(out), out)
		}
		if out[0].PackageName != "com.supercell.brawlstars" || !out[0].StartedAt.Equal(at(16, 10)) {
			t.Fatalf("the first session came back changed: %+v", out[0])
		}
	})

	t.Run("a transition the platform reported twice is not a sitting", func(t *testing.T) {
		out := acceptableSessions([]usageSession{sess("com.whatsapp", at(16, 10), at(16, 10))}, now)
		if len(out) != 0 {
			t.Fatalf("a zero-length session was stored: %+v", out)
		}
	})

	t.Run("a session that ends before it begins is refused", func(t *testing.T) {
		out := acceptableSessions([]usageSession{sess("com.whatsapp", at(16, 35), at(16, 10))}, now)
		if len(out) != 0 {
			t.Fatalf("an inverted session was stored: %+v", out)
		}
	})

	t.Run("a package the platform reported empty is dropped, and the rest still land", func(t *testing.T) {
		out := acceptableSessions([]usageSession{
			sess("  ", at(16, 10), at(16, 20)),
			sess("com.whatsapp", at(16, 20), at(16, 30)),
		}, now)
		// Dropped rather than failing the request: the phone re-sends what it could not deliver, so
		// one malformed row would otherwise block every good session behind it forever.
		if len(out) != 1 || out[0].PackageName != "com.whatsapp" {
			t.Fatalf("want only the good session, got %+v", out)
		}
	})

	t.Run("a clock drifting a minute ahead of the server still files", func(t *testing.T) {
		out := acceptableSessions([]usageSession{
			sess("com.whatsapp", now.Add(-5*time.Minute), now.Add(time.Minute)),
		}, now)
		if len(out) != 1 {
			t.Fatalf("a session one minute ahead was refused; phones drift and this is real usage: %+v", out)
		}
	})

	t.Run("a session parked in the future is refused", func(t *testing.T) {
		out := acceptableSessions([]usageSession{
			sess("com.whatsapp", now.Add(time.Hour), now.Add(2*time.Hour)),
		}, now)
		if len(out) != 0 {
			t.Fatalf("a session an hour in the future was stored: %+v", out)
		}
	})

	t.Run("a session older than the backfill window is refused", func(t *testing.T) {
		old := now.AddDate(0, 0, -usageBackfillDays-1)
		out := acceptableSessions([]usageSession{sess("com.whatsapp", old, old.Add(time.Minute))}, now)
		if len(out) != 0 {
			t.Fatalf("a session older than the day totals may be filed for was stored: %+v", out)
		}
	})

	t.Run("one report cannot be unbounded", func(t *testing.T) {
		in := make([]usageSession, maxUsageSessionsPerReport+50)
		for i := range in {
			start := now.Add(-time.Duration(i+1) * time.Minute)
			in[i] = sess("com.whatsapp", start, start.Add(30*time.Second))
		}
		out := acceptableSessions(in, now)
		if len(out) != maxUsageSessionsPerReport {
			t.Fatalf("stored %d, want the cap of %d", len(out), maxUsageSessionsPerReport)
		}
	})
}

// The window a day's timeline is read over.
//
// A local day is not 24 hours twice a year, and a timeline is the one view where that is visible:
// clipping an hour would hide a real evening, and overshooting would draw the next morning's first
// app onto tonight.
func TestLocalDaySpansTheRealDay(t *testing.T) {
	t.Run("an ordinary day is midnight to midnight, local", func(t *testing.T) {
		from, to, err := localDay("Europe/Zurich", "2026-09-20")
		if err != nil {
			t.Fatal(err)
		}
		if h := to.Sub(from).Hours(); h != 24 {
			t.Fatalf("the day was %v hours long", h)
		}
		if from.Format("2006-01-02T15:04:05-07:00") != "2026-09-20T00:00:00+02:00" {
			t.Fatalf("the day began at %s", from.Format(time.RFC3339))
		}
	})

	t.Run("the day the clocks go back is twenty five hours", func(t *testing.T) {
		// Europe/Zurich leaves summer time on the last Sunday of October.
		from, to, err := localDay("Europe/Zurich", "2026-10-25")
		if err != nil {
			t.Fatal(err)
		}
		if h := to.Sub(from).Hours(); h != 25 {
			t.Fatalf("the day was %v hours long; a fixed 24 would have clipped the evening", h)
		}
	})

	t.Run("a timezone that does not exist is refused, not guessed", func(t *testing.T) {
		if _, _, err := localDay("Mars/Olympus", "2026-09-20"); err == nil {
			t.Fatal("an unknown timezone produced a window")
		}
	})
}
