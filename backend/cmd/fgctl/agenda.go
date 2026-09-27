package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/agenda"
	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
)

// The agenda and the family's holidays (FR-24.6) from the command line: both as documents that the
// --json output of the same command can be edited from, and the week as the server expands it.

// readDoc decodes a document file, refusing unknown fields so a misspelled key is an error and not a
// silently dropped setting.
func readDoc(name string, into any) error {
	raw, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func cmdAgenda(ctx context.Context, env *environment, args []string) error {
	if len(args) != 1 && !(len(args) == 3 && args[1] == "--set") {
		return fmt.Errorf("usage: fgctl agenda <child-id> [--set file.json]  (file: the document `fgctl agenda <child-id> --json` prints)")
	}
	path := "/api/v1/children/" + args[0] + "/agenda"
	var out struct {
		Entries []store.AgendaEntry `json:"entries"`
	}
	if len(args) == 3 {
		var doc struct {
			Entries []store.AgendaEntry `json:"entries"`
		}
		if err := readDoc(args[2], &doc); err != nil {
			return err
		}
		if err := env.client.Do(ctx, http.MethodPut, path, doc, &out); err != nil {
			return err
		}
	} else if err := env.client.Get(ctx, path, &out); err != nil {
		return err
	}
	return env.emit(out, func(w *tabwriter.Writer) {
		if len(out.Entries) == 0 {
			fmt.Fprintln(w, "no agenda")
			return
		}
		for _, e := range out.Entries {
			when := e.Day
			if e.Kind == agenda.Recurring {
				when = weekdayText(e.Weekdays)
			}
			detail := ""
			if e.Place != "" {
				detail += " · " + e.Place
			}
			if e.Optional {
				detail += " (optional)"
			}
			fmt.Fprintf(w, "%s\t%s %s–%s%s\t%s\n", e.Title, when, e.StartsAt, e.EndsAt, detail, e.ID)
		}
	})
}

func cmdHolidays(ctx context.Context, env *environment, args []string) error {
	if len(args) != 0 && !(len(args) == 2 && args[0] == "--set") {
		return fmt.Errorf("usage: fgctl holidays [--set file.json]  (file: the document `fgctl holidays --json` prints)")
	}
	var out struct {
		Holidays []store.Holiday `json:"holidays"`
	}
	if len(args) == 2 {
		var doc struct {
			Holidays []store.Holiday `json:"holidays"`
		}
		if err := readDoc(args[1], &doc); err != nil {
			return err
		}
		if err := env.client.Do(ctx, http.MethodPut, "/api/v1/family/holidays", doc, &out); err != nil {
			return err
		}
	} else if err := env.client.Get(ctx, "/api/v1/family/holidays", &out); err != nil {
		return err
	}
	return env.emit(out, func(w *tabwriter.Writer) {
		if len(out.Holidays) == 0 {
			fmt.Fprintln(w, "no holidays")
			return
		}
		for _, h := range out.Holidays {
			fmt.Fprintf(w, "%s\t%s – %s (%s)\t%s\n", h.Title, h.StartsOn, h.EndsOn, holidayLength(h), h.ID)
		}
	})
}

func holidayLength(h store.Holiday) string {
	a, errA := time.Parse(time.DateOnly, h.StartsOn)
	b, errB := time.Parse(time.DateOnly, h.EndsOn)
	if errA != nil || errB != nil {
		return "?"
	}
	n := int(b.Sub(a).Hours()/24) + 1
	if n == 1 {
		return "1 day"
	}
	return strconv.Itoa(n) + " days"
}

func cmdWeek(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl week <child-id> [--from YYYY-MM-DD] [--days 1-31]")
	}
	days, err := fetchWeek(ctx, env.client, args[0], flagValue(args[1:], "--from"), flagValue(args[1:], "--days"))
	if err != nil {
		return err
	}
	return env.emit(map[string]any{"days": days}, func(w *tabwriter.Writer) {
		for _, d := range days {
			head := d.Day
			if at, err := time.Parse(time.DateOnly, d.Day); err == nil {
				head += " " + at.Weekday().String()[:3]
			}
			if d.Holiday != "" {
				head += " — " + d.Holiday
			}
			fmt.Fprintln(w, head)
			for _, it := range d.Items {
				detail := ""
				if it.Place != "" {
					detail += " · " + it.Place
				}
				if it.Optional {
					detail += " (optional)"
				}
				fmt.Fprintf(w, "  %s–%s %s%s\n", it.StartsAt, it.EndsAt, it.Title, detail)
			}
		}
	})
}

func fetchWeek(ctx context.Context, client *fgclient.Client, childID, from, days string) ([]agenda.Day, error) {
	var out struct {
		Days []agenda.Day `json:"days"`
	}
	path := fgclient.Query("/api/v1/children/"+childID+"/agenda/days", map[string]string{"from": from, "days": days})
	if err := client.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Days, nil
}
