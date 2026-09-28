package main

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/helios57/familyguard/backend/internal/energy"
	"github.com/helios57/familyguard/backend/internal/fgclient"
)

// What FamilyGuard spends on a phone, per hour, as the phone measured it (FR-26.5).

type energyBody struct {
	Hours   []energy.Totals `json:"hours"`
	Total   energy.Totals   `json:"total"`
	Samples int             `json:"samples"`
}

func getEnergy(ctx context.Context, c *fgclient.Client, deviceID, hours string) (energyBody, error) {
	var body energyBody
	err := c.Get(ctx, fgclient.Query("/api/v1/devices/"+deviceID+"/energy", map[string]string{"hours": hours}), &body)
	return body, err
}

func cmdEnergy(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl energy <device-id> [--hours N]")
	}
	body, err := getEnergy(ctx, env.client, args[0], flagValue(args, "--hours"))
	if err != nil {
		return err
	}
	return env.emit(body, func(w *tabwriter.Writer) {
		if len(body.Hours) == 0 {
			fmt.Fprintf(w, "not reported (%d samples: a phone reports on each heartbeat once it runs 0.6.26 or later)\n", body.Samples)
			return
		}
		fmt.Fprintln(w, "HOUR (UTC)\tMIN\tBATTERY\tCPU\tSTREAMS\tEVENTS\tPOLLS\tPUSHES\tOTHER\tDATA")
		for _, h := range body.Hours {
			fmt.Fprintf(w, "%s\t%.0f\t%s\t%.1f s\t%d\t%d\t%d\t%d\t%d\t%s\n", h.Hour.Format("01-02 15:00"),
				h.Minutes, battery(h), float64(h.CPUMs)/1000, h.StreamOpens, h.Events, h.Polls, h.Pushes,
				h.OtherSyncs, kib(h.RxBytes+h.TxBytes))
		}
		fmt.Fprintln(w, energySummary(body.Total))
	})
}

// energySummary is the one line a parent reads: the battery's own rate and FamilyGuard's share.
func energySummary(t energy.Totals) string {
	if t.Minutes <= 0 {
		return "not reported"
	}
	hours := t.Minutes / 60
	line := fmt.Sprintf("over %.1f h: FamilyGuard CPU %.1f s/h, %.1f wake-ups/h", hours,
		float64(t.CPUMs)/1000/hours, float64(t.StreamOpens+t.Events+t.Polls+t.Pushes+t.OtherSyncs)/hours)
	if t.UnpluggedMinutes > 0 {
		line = fmt.Sprintf("battery −%.1f %% per unplugged hour; ", float64(t.BatteryUsed)/(t.UnpluggedMinutes/60)) + line
	} else {
		line = "battery not measured (charging throughout); " + line
	}
	return line
}

func battery(h energy.Totals) string {
	if h.UnpluggedMinutes <= 0 {
		return "charging"
	}
	return fmt.Sprintf("−%d %%", h.BatteryUsed)
}

func kib(n int64) string {
	return fmt.Sprintf("%.0f KiB", float64(n)/1024)
}
