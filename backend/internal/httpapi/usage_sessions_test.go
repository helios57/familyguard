package httpapi

import (
	"testing"
	"time"

	"github.com/helios57/familyguard/backend/internal/store"
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

// ---- the hour-by-hour chart -------------------------------------------------

// A sitting is stored whole, so every question the chart asks is an intersection.
//
// The property that matters most is not any single bucket: it is that the buckets SUM to the time
// actually spent inside the day. A split that loses a second per boundary loses twenty-three
// seconds a day and nothing looks wrong; a split that double-counts one makes the chart disagree
// with the table directly beneath it, which is the thing a parent would notice and could not
// explain.
func TestTheHourChartSplitsSittingsAcrossHours(t *testing.T) {
	zurich, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Fatalf("Europe/Zurich: %v", err)
	}
	at := func(text string) time.Time {
		ts, err := time.ParseInLocation("2006-01-02 15:04:05", text, zurich)
		if err != nil {
			t.Fatalf("parse %q: %v", text, err)
		}
		return ts
	}
	sitting := func(from, to string) store.UsageSession {
		return store.UsageSession{PackageName: "com.example.game", StartedAt: at(from), EndedAt: at(to)}
	}
	// secondsAt reads the bucket whose local hour is `hour`, so a test never has to know which
	// index that is on a day where an hour was added or taken away.
	secondsAt := func(buckets []hourBucket, hour int) int {
		t.Helper()
		for _, b := range buckets {
			if b.Start.In(zurich).Hour() == hour {
				return b.Seconds
			}
		}
		t.Fatalf("no bucket for local hour %02d among %d", hour, len(buckets))
		return 0
	}
	total := func(buckets []hourBucket) int {
		sum := 0
		for _, b := range buckets {
			sum += b.Seconds
		}
		return sum
	}

	t.Run("an ordinary day has 24 buckets, one per hour, even with nothing in it", func(t *testing.T) {
		from, to, err := localDay("Europe/Zurich", "2026-09-20")
		if err != nil {
			t.Fatal(err)
		}
		got := hourlyScreenTime(nil, from, to)
		if len(got) != 24 {
			t.Errorf("an ordinary day produced %d buckets, want 24", len(got))
		}
		if total(got) != 0 {
			t.Errorf("a day with no sittings totals %d seconds, want 0", total(got))
		}
		// An empty day must still draw as a day. Returning no buckets would make the console render
		// nothing at all, which is the picture of a broken tab rather than of a quiet Sunday.
		if got == nil {
			t.Error("a day with no sittings returned a nil slice; the chart needs its empty hours")
		}
	})

	t.Run("a sitting inside one hour lands entirely in it", func(t *testing.T) {
		from, to, _ := localDay("Europe/Zurich", "2026-09-20")
		got := hourlyScreenTime([]store.UsageSession{
			sitting("2026-09-20 14:10:00", "2026-09-20 14:40:00"),
		}, from, to)
		if s := secondsAt(got, 14); s != 30*60 {
			t.Errorf("14:00 holds %d seconds, want %d", s, 30*60)
		}
		if s := secondsAt(got, 13); s != 0 {
			t.Errorf("13:00 holds %d seconds for a sitting that began at 14:10", s)
		}
		if total(got) != 30*60 {
			t.Errorf("the day totals %d seconds, want %d", total(got), 30*60)
		}
	})

	t.Run("a sitting across a boundary is split, and the halves still add up", func(t *testing.T) {
		from, to, _ := localDay("Europe/Zurich", "2026-09-20")
		got := hourlyScreenTime([]store.UsageSession{
			sitting("2026-09-20 19:40:00", "2026-09-20 21:10:00"),
		}, from, to)
		for hour, want := range map[int]int{19: 20 * 60, 20: 60 * 60, 21: 10 * 60} {
			if s := secondsAt(got, hour); s != want {
				t.Errorf("%02d:00 holds %d seconds, want %d", hour, s, want)
			}
		}
		if total(got) != 90*60 {
			t.Errorf("a 90-minute film totals %d seconds across the chart, want %d",
				total(got), 90*60)
		}
	})

	t.Run("a sitting that began yesterday is counted only from midnight", func(t *testing.T) {
		from, to, _ := localDay("Europe/Zurich", "2026-09-20")
		got := hourlyScreenTime([]store.UsageSession{
			sitting("2026-09-19 23:30:00", "2026-09-20 00:20:00"),
		}, from, to)
		if s := secondsAt(got, 0); s != 20*60 {
			t.Errorf("00:00 holds %d seconds, want the %d that fell after midnight", s, 20*60)
		}
		if total(got) != 20*60 {
			t.Errorf("the day totals %d seconds; the half before midnight belongs to the 19th",
				total(got))
		}
	})

	t.Run("a sitting still running at midnight is counted only until it", func(t *testing.T) {
		from, to, _ := localDay("Europe/Zurich", "2026-09-20")
		got := hourlyScreenTime([]store.UsageSession{
			sitting("2026-09-20 23:45:00", "2026-09-21 00:30:00"),
		}, from, to)
		if s := secondsAt(got, 23); s != 15*60 {
			t.Errorf("23:00 holds %d seconds, want %d", s, 15*60)
		}
		if total(got) != 15*60 {
			t.Errorf("the day totals %d seconds; the rest belongs to the 21st", total(got))
		}
	})

	// The two days a year a fixed 0–23 axis is wrong. Walking by adding an hour to an INSTANT is
	// what makes these right: a loop over clock-hour numbers would invent 02:00 in March and draw
	// October's repeated 02:00 once.
	t.Run("the morning the clocks go forward has 23 buckets", func(t *testing.T) {
		from, to, err := localDay("Europe/Zurich", "2026-03-29")
		if err != nil {
			t.Fatal(err)
		}
		got := hourlyScreenTime(nil, from, to)
		if len(got) != 23 {
			t.Errorf("2026-03-29 produced %d buckets, want 23 — that day is 23 hours long",
				len(got))
		}
	})

	t.Run("the morning the clocks go back has 25 buckets", func(t *testing.T) {
		from, to, err := localDay("Europe/Zurich", "2026-10-25")
		if err != nil {
			t.Fatal(err)
		}
		got := hourlyScreenTime(nil, from, to)
		if len(got) != 25 {
			t.Errorf("2026-10-25 produced %d buckets, want 25 — that day is 25 hours long",
				len(got))
		}
	})

	t.Run("two apps in one hour are added, not overwritten", func(t *testing.T) {
		from, to, _ := localDay("Europe/Zurich", "2026-09-20")
		got := hourlyScreenTime([]store.UsageSession{
			sitting("2026-09-20 16:00:00", "2026-09-20 16:20:00"),
			{PackageName: "com.example.book",
				StartedAt: at("2026-09-20 16:30:00"), EndedAt: at("2026-09-20 16:45:00")},
		}, from, to)
		if s := secondsAt(got, 16); s != 35*60 {
			t.Errorf("16:00 holds %d seconds for two sittings of 20 and 15 minutes, want %d",
				s, 35*60)
		}
	})
}
