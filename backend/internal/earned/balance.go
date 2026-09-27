// Package earned computes a profile's earned-time (Bonuszeit) balance (FR-22).
//
// Pure: credits and each day's spending in, the balance out. The spending comes from the phones —
// each attributes every measured window when it measures it — so this package never decides what a
// minute was, only what the minutes add up to.
package earned

import (
	"sort"
	"time"
)

// ValidDays is how long a credit lasts, counting the day it was earned: earned Monday, last usable
// Sunday.
const ValidDays = 7

// lookback bounds the simulation. Two validity periods, so a credit that expired before the window a
// screen cares about still absorbed the spending it really absorbed, and an older debt drops out.
const lookback = 2 * ValidDays

// Credit is one earned amount on the day it was earned (YYYY-MM-DD). Withdrawn credits are not passed.
type Credit struct {
	Day     string
	Minutes int
}

// Remaining is what is left of one credit at the start of today.
type Remaining struct {
	EarnedOn  string `json:"earned_on"`
	ExpiresOn string `json:"expires_on"`
	Minutes   int    `json:"minutes"`
}

// Balance is the start-of-day balance: AvailableToday is what the phone may spend today before
// subtracting its own spending, negative for an unsettled overdraft; Credits is what makes it up,
// oldest first.
type Balance struct {
	AvailableToday int         `json:"available_minutes"`
	Credits        []Remaining `json:"credits"`
}

// Compute replays the days before today: each day's credits join the queue and settle any debt,
// each day's spending takes the oldest credit first, and a credit leaves the queue at the end of its
// last day. Today's credits count; today's spending does not — the phone subtracts its own.
func Compute(credits []Credit, spentByDay map[string]int, today string) Balance {
	day, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return Balance{Credits: []Remaining{}}
	}
	byDay := map[string][]int{}
	for _, c := range credits {
		if c.Minutes > 0 {
			byDay[c.Day] = append(byDay[c.Day], c.Minutes)
		}
	}

	var queue []Remaining
	debt := 0
	earn := func(d string) {
		for _, m := range byDay[d] {
			if settle := min(debt, m); settle > 0 {
				debt -= settle
				m -= settle
			}
			if m > 0 {
				expires, _ := time.Parse(time.DateOnly, d)
				queue = append(queue, Remaining{
					EarnedOn: d, ExpiresOn: expires.AddDate(0, 0, ValidDays-1).Format(time.DateOnly), Minutes: m,
				})
			}
		}
	}

	for i := lookback; i >= 1; i-- {
		d := day.AddDate(0, 0, -i).Format(time.DateOnly)
		earn(d)
		spend := spentByDay[d]
		for spend > 0 && len(queue) > 0 {
			take := min(spend, queue[0].Minutes)
			queue[0].Minutes -= take
			spend -= take
			if queue[0].Minutes == 0 {
				queue = queue[1:]
			}
		}
		debt += spend
		kept := queue[:0]
		for _, r := range queue {
			if r.ExpiresOn > d {
				kept = append(kept, r)
			}
		}
		queue = kept
	}
	earn(today)

	sort.SliceStable(queue, func(i, j int) bool { return queue[i].EarnedOn < queue[j].EarnedOn })
	total := 0
	for _, r := range queue {
		total += r.Minutes
	}
	if queue == nil {
		queue = []Remaining{}
	}
	return Balance{AvailableToday: total - debt, Credits: queue}
}
