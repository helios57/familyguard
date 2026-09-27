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
