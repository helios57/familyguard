package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
)

// The alarm clock (FR-23.6) from the command line: the week as one document, and a change for one
// date. The document `fgctl alarm --json` prints is the one `--set` takes.

var clockText = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

var weekdayNames = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func cmdAlarm(ctx context.Context, env *environment, args []string) error {
	if len(args) != 1 && !(len(args) == 3 && args[1] == "--set") {
		return fmt.Errorf("usage: fgctl alarm <child-id> [--set file.json]  (file: {\"weekdays\": [7 × \"HH:MM\" or \"\"]}, Monday first)")
	}
	path := "/api/v1/children/" + args[0] + "/alarm"
	var alarm store.Alarm
	if len(args) == 3 {
		raw, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		var doc struct {
			Weekdays []string `json:"weekdays"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return fmt.Errorf("%s is not an alarm document ({\"weekdays\": [...]}): %w", args[2], err)
		}
		if err := env.client.Do(ctx, http.MethodPut, path, doc, &alarm); err != nil {
			return err
		}
	} else if err := env.client.Get(ctx, path, &alarm); err != nil {
		return err
	}
	return env.emit(alarm, func(w *tabwriter.Writer) {
		for i, at := range alarm.Weekdays {
			if at == "" {
				at = "off"
			}
			fmt.Fprintf(w, "%s\t%s\n", weekdayNames[i], at)
		}
		for _, o := range alarm.Overrides {
			at := "no alarm"
			if o.Time != nil {
				at = *o.Time
			}
			fmt.Fprintf(w, "%s\t%s (date change)\n", o.Day, at)
		}
	})
}

// cmdAlarmDay sets, turns off or clears the alarm for one date. "today" and "tomorrow" are the
// profile's own, read from its timezone, not the machine's.
func cmdAlarmDay(ctx context.Context, env *environment, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: fgctl alarm-day <child-id> <YYYY-MM-DD|today|tomorrow> <HH:MM|off|clear>")
	}
	day, err := resolveProfileDay(ctx, env.client, args[0], args[1])
	if err != nil {
		return err
	}
	path := "/api/v1/children/" + args[0] + "/alarm/days/" + day
	switch at := args[2]; {
	case at == "clear":
		if err := env.client.Do(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		return env.emit(map[string]any{"day": day, "cleared": true}, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "%s\tfollows the week again\n", day)
		})
	case at == "off" || clockText.MatchString(at):
		body := map[string]any{"time": nil}
		if at != "off" {
			body["time"] = at
		}
		var out store.AlarmDay
		if err := env.client.Do(ctx, http.MethodPut, path, body, &out); err != nil {
			return err
		}
		return env.emit(out, func(w *tabwriter.Writer) {
			if out.Time == nil {
				fmt.Fprintf(w, "%s\tno alarm\n", day)
			} else {
				fmt.Fprintf(w, "%s\trings at %s\n", day, *out.Time)
			}
		})
	default:
		return fmt.Errorf("the alarm time is HH:MM, off or clear, not %q", at)
	}
}

// resolveProfileDay turns today/tomorrow into the profile's date; a date is passed through.
func resolveProfileDay(ctx context.Context, client *fgclient.Client, childID, day string) (string, error) {
	offset := 0
	switch day {
	case "today":
	case "tomorrow":
		offset = 1
	default:
		return day, nil
	}
	var pol store.Policy
	if err := client.Get(ctx, "/api/v1/children/"+childID+"/policy", &pol); err != nil {
		return "", err
	}
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		return "", fmt.Errorf("the profile's timezone %q cannot be read here: %w", pol.Timezone, err)
	}
	return time.Now().In(loc).AddDate(0, 0, offset).Format(time.DateOnly), nil
}
