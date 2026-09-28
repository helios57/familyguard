// Package energy turns a phone's cumulative energy reports into what it spent per hour (FR-26.5).
//
// The phone reports counters that only grow while its process lives, stamped with when that process
// started. The spend over an interval is therefore the difference of two reports from the same
// process, and nothing else: a report from a new process starts a new run, because subtracting
// across a restart turns "the counters began again" into a large negative number.
package energy

import (
	"sort"
	"time"
)

// Sample is one heartbeat's report. Counters are cumulative since Since, when the process started.
type Sample struct {
	At           time.Time
	Since        time.Time
	BatteryLevel *int
	Charging     *bool

	CPUMs, RxBytes, TxBytes                        int64
	StreamOpens, Events, Polls, Pushes, OtherSyncs int64

	// Nil from a build that does not report them: not measured, never zero.
	ActiveMs, PassiveMs, RouteFullMs, RouteDNSMs *int64
}

// Totals is what was spent over an hour, or over the whole window.
//
// BatteryUsed is percentage points dropped over intervals that were unplugged at both ends, and
// UnpluggedMinutes is how long those intervals were — the rate a parent wants is their quotient.
type Totals struct {
	Hour             time.Time `json:"hour"`
	Minutes          float64   `json:"minutes"`
	UnpluggedMinutes float64   `json:"unplugged_minutes"`
	BatteryUsed      int       `json:"battery_used"`

	CPUMs       int64 `json:"cpu_ms"`
	RxBytes     int64 `json:"rx_bytes"`
	TxBytes     int64 `json:"tx_bytes"`
	StreamOpens int64 `json:"stream_opens"`
	Events      int64 `json:"events"`
	Polls       int64 `json:"polls"`
	Pushes      int64 `json:"pushes"`
	OtherSyncs  int64 `json:"other_syncs"`

	ActiveMs    *int64 `json:"active_ms"`
	PassiveMs   *int64 `json:"passive_ms"`
	RouteFullMs *int64 `json:"route_full_ms"`
	RouteDNSMs  *int64 `json:"route_dns_ms"`
}

// Hourly pairs consecutive samples of one run and sums the differences per hour, attributing each
// interval to the hour its end falls in. Hours come back in order, only those with an interval.
func Hourly(samples []Sample) ([]Totals, Totals) {
	sorted := append([]Sample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })

	byHour := map[time.Time]*Totals{}
	var total Totals
	for i := 1; i < len(sorted); i++ {
		a, b := sorted[i-1], sorted[i]
		d, ok := difference(a, b)
		if !ok {
			continue
		}
		hour := b.At.UTC().Truncate(time.Hour)
		h := byHour[hour]
		if h == nil {
			h = &Totals{Hour: hour}
			byHour[hour] = h
		}
		h.add(d)
		total.add(d)
	}

	hours := make([]Totals, 0, len(byHour))
	for _, h := range byHour {
		hours = append(hours, *h)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Hour.Before(hours[j].Hour) })
	return hours, total
}

// difference is what was spent between a and b, or false when the two are not one run — a restart
// between them, or a counter that went backwards, which is not a number that can be trusted.
func difference(a, b Sample) (Totals, bool) {
	if !a.Since.Equal(b.Since) || !b.At.After(a.At) {
		return Totals{}, false
	}
	d := Totals{
		Minutes:     b.At.Sub(a.At).Minutes(),
		CPUMs:       b.CPUMs - a.CPUMs,
		RxBytes:     b.RxBytes - a.RxBytes,
		TxBytes:     b.TxBytes - a.TxBytes,
		StreamOpens: b.StreamOpens - a.StreamOpens,
		Events:      b.Events - a.Events,
		Polls:       b.Polls - a.Polls,
		Pushes:      b.Pushes - a.Pushes,
		OtherSyncs:  b.OtherSyncs - a.OtherSyncs,
	}
	for _, v := range []int64{d.CPUMs, d.RxBytes, d.TxBytes, d.StreamOpens, d.Events, d.Polls, d.Pushes, d.OtherSyncs} {
		if v < 0 {
			return Totals{}, false
		}
	}
	var backwards bool
	d.ActiveMs = optional(a.ActiveMs, b.ActiveMs, &backwards)
	d.PassiveMs = optional(a.PassiveMs, b.PassiveMs, &backwards)
	d.RouteFullMs = optional(a.RouteFullMs, b.RouteFullMs, &backwards)
	d.RouteDNSMs = optional(a.RouteDNSMs, b.RouteDNSMs, &backwards)
	if backwards {
		return Totals{}, false
	}
	if unplugged(a) && unplugged(b) {
		d.UnpluggedMinutes = d.Minutes
		if drop := *a.BatteryLevel - *b.BatteryLevel; drop > 0 {
			d.BatteryUsed = drop
		}
	}
	return d, true
}

func unplugged(s Sample) bool {
	return s.Charging != nil && !*s.Charging && s.BatteryLevel != nil
}

// optional is b−a when both ends measured it, nil otherwise.
func optional(a, b *int64, backwards *bool) *int64 {
	if a == nil || b == nil {
		return nil
	}
	v := *b - *a
	if v < 0 {
		*backwards = true
	}
	return &v
}

// Sum is a and b added, the way Hourly adds intervals: Hour is left zero.
func Sum(a, b Totals) Totals {
	b.Hour = time.Time{}
	a.Hour = time.Time{}
	a.add(b)
	return a
}

func (t *Totals) add(d Totals) {
	t.Minutes += d.Minutes
	t.UnpluggedMinutes += d.UnpluggedMinutes
	t.BatteryUsed += d.BatteryUsed
	t.CPUMs += d.CPUMs
	t.RxBytes += d.RxBytes
	t.TxBytes += d.TxBytes
	t.StreamOpens += d.StreamOpens
	t.Events += d.Events
	t.Polls += d.Polls
	t.Pushes += d.Pushes
	t.OtherSyncs += d.OtherSyncs
	t.ActiveMs = sum(t.ActiveMs, d.ActiveMs)
	t.PassiveMs = sum(t.PassiveMs, d.PassiveMs)
	t.RouteFullMs = sum(t.RouteFullMs, d.RouteFullMs)
	t.RouteDNSMs = sum(t.RouteDNSMs, d.RouteDNSMs)
}

// sum keeps "not measured" as nil until some interval measured it.
func sum(acc, v *int64) *int64 {
	if v == nil {
		return acc
	}
	if acc == nil {
		x := *v
		return &x
	}
	x := *acc + *v
	return &x
}
