package energy

import (
	"testing"
	"time"
)

var base = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// sample is one heartbeat's report, minutes after base, from a process started at since.
func sample(minutes int, since time.Time, battery int, charging bool, cpu int64, streams int64) Sample {
	return Sample{
		At:           base.Add(time.Duration(minutes) * time.Minute),
		Since:        since,
		BatteryLevel: ptr(battery),
		Charging:     ptr(charging),
		CPUMs:        cpu,
		StreamOpens:  streams,
	}
}

func TestTwoSamplesOfOneRunGiveTheirDifference(t *testing.T) {
	hours, total := Hourly([]Sample{
		sample(5, base, 80, false, 1000, 2),
		sample(35, base, 77, false, 4000, 4),
	})
	if len(hours) != 1 {
		t.Fatalf("want one hour, got %d", len(hours))
	}
	h := hours[0]
	if !h.Hour.Equal(base) {
		t.Errorf("the interval ends at 10:35 and was put in the hour %v", h.Hour)
	}
	if h.Minutes != 30 || h.UnpluggedMinutes != 30 {
		t.Errorf("minutes %v unplugged %v, want 30 and 30", h.Minutes, h.UnpluggedMinutes)
	}
	if h.BatteryUsed != 3 || h.CPUMs != 3000 || h.StreamOpens != 2 {
		t.Errorf("battery %d cpu %d streams %d, want 3, 3000, 2", h.BatteryUsed, h.CPUMs, h.StreamOpens)
	}
	if total.CPUMs != 3000 || total.BatteryUsed != 3 || total.Minutes != 30 {
		t.Errorf("the total is not the one hour: %+v", total)
	}
}

func TestARestartStartsANewRunAndNeverSubtractsAcrossIt(t *testing.T) {
	restarted := base.Add(40 * time.Minute)
	_, total := Hourly([]Sample{
		sample(0, base, 90, false, 50_000, 10),
		sample(30, base, 89, false, 51_000, 11),
		// The process restarted: its counters begin again near zero. Subtracting across the restart
		// would be -50 s of CPU.
		sample(45, restarted, 88, false, 200, 1),
		sample(60, restarted, 87, false, 700, 1),
	})
	if total.CPUMs != 1000+500 {
		t.Errorf("cpu %d, want 1500 (1000 before the restart, 500 after, nothing across it)", total.CPUMs)
	}
	if total.Minutes != 30+15 {
		t.Errorf("minutes %v, want 45: the 15 minutes spanning the restart are not measured", total.Minutes)
	}
	if total.BatteryUsed != 2 {
		t.Errorf("battery %d, want 2 — the drop across the restart is not attributed", total.BatteryUsed)
	}
}

func TestChargingIsNotBatteryUsed(t *testing.T) {
	_, total := Hourly([]Sample{
		sample(0, base, 50, false, 0, 0),
		sample(20, base, 49, false, 0, 0),
		sample(40, base, 70, true, 0, 0), // plugged in: the level rose
		sample(60, base, 75, true, 0, 0),
		sample(80, base, 74, false, 0, 0), // unplugged somewhere in between: unknown when
		sample(100, base, 72, false, 0, 0),
	})
	if total.BatteryUsed != 1+2 {
		t.Errorf("battery %d, want 3: only intervals unplugged at both ends count", total.BatteryUsed)
	}
	if total.UnpluggedMinutes != 40 {
		t.Errorf("unplugged %v, want 40", total.UnpluggedMinutes)
	}
	if total.Minutes != 100 {
		t.Errorf("minutes %v, want 100: CPU is spent plugged in too", total.Minutes)
	}
}

func TestIntervalsLandInTheHourTheyEnd(t *testing.T) {
	hours, _ := Hourly([]Sample{
		sample(50, base, 80, false, 0, 0),
		sample(70, base, 80, false, 100, 0),
		sample(130, base, 80, false, 300, 0),
	})
	if len(hours) != 2 {
		t.Fatalf("want two hours, got %d: %+v", len(hours), hours)
	}
	if !hours[0].Hour.Equal(base.Add(time.Hour)) || hours[0].CPUMs != 100 {
		t.Errorf("first hour %v cpu %d, want 11:00 and 100", hours[0].Hour, hours[0].CPUMs)
	}
	if !hours[1].Hour.Equal(base.Add(2*time.Hour)) || hours[1].CPUMs != 200 {
		t.Errorf("second hour %v cpu %d, want 12:00 and 200", hours[1].Hour, hours[1].CPUMs)
	}
}

func TestSamplesOutOfOrderAreSortedFirst(t *testing.T) {
	_, total := Hourly([]Sample{
		sample(30, base, 79, false, 2000, 0),
		sample(0, base, 80, false, 1000, 0),
	})
	if total.CPUMs != 1000 || total.BatteryUsed != 1 {
		t.Errorf("cpu %d battery %d, want 1000 and 1", total.CPUMs, total.BatteryUsed)
	}
}

func TestModeTimeIsNotMeasuredUntilAPhoneReportsIt(t *testing.T) {
	a := sample(0, base, 80, false, 0, 0)
	b := sample(10, base, 80, false, 0, 0)
	_, total := Hourly([]Sample{a, b})
	if total.ActiveMs != nil || total.RouteDNSMs != nil {
		t.Errorf("a build that does not report modes produced active=%v dns=%v; not measured is nil",
			total.ActiveMs, total.RouteDNSMs)
	}
	a.ActiveMs, b.ActiveMs = ptr(int64(1000)), ptr(int64(61_000))
	a.PassiveMs, b.PassiveMs = ptr(int64(0)), ptr(int64(0))
	_, total = Hourly([]Sample{a, b})
	if total.ActiveMs == nil || *total.ActiveMs != 60_000 {
		t.Errorf("active %v, want 60000", total.ActiveMs)
	}
	if total.PassiveMs == nil || *total.PassiveMs != 0 {
		t.Errorf("passive %v, want a measured 0", total.PassiveMs)
	}
}

func TestACounterThatWentBackwardsDropsTheInterval(t *testing.T) {
	// Same since, counter lower: not a restart we can see, so not a number we can trust.
	_, total := Hourly([]Sample{
		sample(0, base, 80, false, 5000, 3),
		sample(10, base, 80, false, 4000, 3),
		sample(20, base, 80, false, 4500, 3),
	})
	if total.CPUMs != 500 || total.Minutes != 10 {
		t.Errorf("cpu %d minutes %v, want 500 and 10 — the backwards interval dropped", total.CPUMs, total.Minutes)
	}
}

func TestARestartIsANewRunEvenWhenItsCountersHappenToBeHigher(t *testing.T) {
	// A young process restarted by an old one's successor: the new counters are larger, so no
	// difference goes negative, and only `since` tells the two runs apart.
	restarted := base.Add(20 * time.Minute)
	_, total := Hourly([]Sample{
		sample(0, base, 80, false, 100, 0),
		sample(10, base, 80, false, 200, 0),
		sample(30, restarted, 80, false, 5000, 0),
		sample(40, restarted, 80, false, 5500, 0),
	})
	if total.CPUMs != 100+500 {
		t.Errorf("cpu %d, want 600: the 4800 between the two runs is not a difference of one process", total.CPUMs)
	}
}
