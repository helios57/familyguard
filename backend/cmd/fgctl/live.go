package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
)

// Live (FR-27) from the command line: read it, start or extend it, stop it.

type liveBody struct {
	Active    bool             `json:"active"`
	LiveUntil *time.Time       `json:"live_until"`
	LiveSince *time.Time       `json:"live_since"`
	Locations []store.Location `json:"locations"`
}

func liveCall(ctx context.Context, c *fgclient.Client, deviceID, action string, minutes int) (liveBody, error) {
	var body liveBody
	path := "/api/v1/devices/" + deviceID + "/live"
	var err error
	switch action {
	case "start":
		req := map[string]any{}
		if minutes > 0 {
			req["minutes"] = minutes
		}
		err = c.Do(ctx, http.MethodPost, path, req, &body)
	case "stop":
		err = c.Do(ctx, http.MethodDelete, path, nil, &body)
	default:
		err = c.Get(ctx, path, &body)
	}
	return body, err
}

func cmdLive(ctx context.Context, env *environment, args []string) error {
	usage := fmt.Errorf("usage: fgctl live <device-id> [--start [--minutes N] | --stop]")
	if len(args) < 1 {
		return usage
	}
	action := ""
	minutes := 0
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--start":
			action = "start"
		case "--stop":
			action = "stop"
		case "--minutes":
			if i+1 >= len(args) {
				return usage
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("--minutes must be a number: %q", args[i+1])
			}
			minutes = n
			i++
		default:
			return usage
		}
	}
	if minutes != 0 && action != "start" {
		return fmt.Errorf("--minutes goes with --start")
	}
	body, err := liveCall(ctx, env.client, args[0], action, minutes)
	if err != nil {
		return err
	}
	return env.emit(body, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, liveSummary(body))
		for _, l := range body.Locations {
			acc := ""
			if l.AccuracyM != nil {
				acc = fmt.Sprintf("±%.0f m", *l.AccuracyM)
			}
			fmt.Fprintf(w, "%s\t%.5f, %.5f\t%s\n", l.CapturedAt.Local().Format("15:04:05"), l.Latitude, l.Longitude, acc)
		}
	})
}

func liveSummary(b liveBody) string {
	if b.Active && b.LiveUntil != nil {
		return fmt.Sprintf("live until %s — %d positions this session", b.LiveUntil.Local().Format("15:04"), len(b.Locations))
	}
	return fmt.Sprintf("not live (%d positions from the last session)", len(b.Locations))
}
