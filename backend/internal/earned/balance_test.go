package earned

import (
	"reflect"
	"testing"
)

// FR-22: the Bonuszeit balance. Earned time is valid for seven days counting the day it was earned
// (earned Monday, last usable Sunday), spent oldest first, and an overdraft is a debt the next credit
// settles.

func TestTodaysCreditsAreAvailableToday(t *testing.T) {
	b := Compute([]Credit{{Day: "2026-09-28", Minutes: 30}}, nil, "2026-09-28")
	if b.AvailableToday != 30 {
		t.Fatalf("a credit earned today: available %d, want 30", b.AvailableToday)
	}
	want := []Remaining{{EarnedOn: "2026-09-28", ExpiresOn: "2026-10-04", Minutes: 30}}
	if !reflect.DeepEqual(b.Credits, want) {
		t.Fatalf("credits %+v, want %+v", b.Credits, want)
	}
}

func TestACreditLastsSevenDaysCountingItsOwn(t *testing.T) {
	credits := []Credit{{Day: "2026-09-21", Minutes: 30}} // a Monday
	if b := Compute(credits, nil, "2026-09-27"); b.AvailableToday != 30 {
		t.Errorf("on the Sunday after, the credit is still there: %d, want 30", b.AvailableToday)
	}
	if b := Compute(credits, nil, "2026-09-28"); b.AvailableToday != 0 || len(b.Credits) != 0 {
		t.Errorf("on the next Monday it has expired: %+v", b)
	}
}

func TestSpendingTakesTheOldestCreditFirst(t *testing.T) {
	credits := []Credit{{Day: "2026-09-22", Minutes: 30}, {Day: "2026-09-24", Minutes: 15}}
	spent := map[string]int{"2026-09-25": 20}
	b := Compute(credits, spent, "2026-09-26")
	want := []Remaining{
		{EarnedOn: "2026-09-22", ExpiresOn: "2026-09-28", Minutes: 10},
		{EarnedOn: "2026-09-24", ExpiresOn: "2026-09-30", Minutes: 15},
	}
	if b.AvailableToday != 25 || !reflect.DeepEqual(b.Credits, want) {
		t.Fatalf("after 20 spent: %+v, want 25 = %+v", b, want)
	}
	// Two days later the older credit expires with its 10 minutes unspent; the newer is intact.
	if b := Compute(credits, spent, "2026-09-29"); b.AvailableToday != 15 {
		t.Errorf("after the older credit expired: %d, want 15", b.AvailableToday)
	}
}

func TestAnOverdraftIsADebtTheNextCreditSettles(t *testing.T) {
	credits := []Credit{{Day: "2026-09-22", Minutes: 10}, {Day: "2026-09-24", Minutes: 30}}
	spent := map[string]int{"2026-09-22": 15} // two phones spent the same ten minutes
	b := Compute(credits, spent, "2026-09-25")
	if b.AvailableToday != 25 {
		t.Fatalf("a 5-minute overdraft settled by the next credit: %d, want 25", b.AvailableToday)
	}
	if b := Compute(credits[:1], spent, "2026-09-23"); b.AvailableToday != -5 {
		t.Errorf("an unsettled overdraft shows as negative: %d, want -5", b.AvailableToday)
	}
}

func TestTodaysSpendingIsNotSubtracted(t *testing.T) {
	// The phone subtracts its own spending today; the balance is as of the start of the day.
	b := Compute([]Credit{{Day: "2026-09-27", Minutes: 30}}, map[string]int{"2026-09-28": 12}, "2026-09-28")
	if b.AvailableToday != 30 {
		t.Fatalf("today's spending must not be subtracted here: %d, want 30", b.AvailableToday)
	}
}

func TestSpendingBeforeACreditWasEarnedCannotUseIt(t *testing.T) {
	credits := []Credit{{Day: "2026-09-25", Minutes: 30}}
	b := Compute(credits, map[string]int{"2026-09-23": 10}, "2026-09-26")
	if b.AvailableToday != 20 {
		t.Fatalf("a 10-minute debt from before is settled by the credit: %d, want 20", b.AvailableToday)
	}
}

// The look-back is two validity periods: a debt from exactly 14 days ago is still owed, one from 15
// days ago is forgiven. Pinned at the edge, because an off-by-one there forgives a week early or
// carries a fortnight-old overdraft forever, and neither shows up anywhere but here.
func TestAnOverdraftIsForgivenAfterTwoValidityPeriods(t *testing.T) {
	if b := Compute(nil, map[string]int{"2026-10-01": 10}, "2026-10-15"); b.AvailableToday != -10 {
		t.Errorf("a debt from 14 days ago: available %d, want -10", b.AvailableToday)
	}
	if b := Compute(nil, map[string]int{"2026-09-30": 10}, "2026-10-15"); b.AvailableToday != 0 {
		t.Errorf("a debt from 15 days ago: available %d, want 0 (forgiven)", b.AvailableToday)
	}
}

// A week that contains the end of summer time (Europe, 2026-10-25, a 25-hour day) is still seven
// calendar days: the balance counts days, never hours.
func TestACreditSpanningTheClockChangeLastsSevenDays(t *testing.T) {
	credits := []Credit{{Day: "2026-10-21", Minutes: 30}}
	if b := Compute(credits, nil, "2026-10-27"); b.AvailableToday != 30 || b.Credits[0].ExpiresOn != "2026-10-27" {
		t.Errorf("on its seventh day, across the clock change: %+v", b)
	}
	if b := Compute(credits, nil, "2026-10-28"); b.AvailableToday != 0 {
		t.Errorf("on the eighth day it has expired: %+v", b)
	}
}
