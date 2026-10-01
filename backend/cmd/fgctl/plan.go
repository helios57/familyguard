package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/helios57/familyguard/backend/internal/store"
)

// The daily plan (FR-22.7) from the command line: the plan as one document, today's tasks, and a
// parent's decision on each. The document `fgctl plan --json` prints is the one `--set` takes, so a
// plan is edited by printing it, changing the file, and setting it back — with the ids it carries,
// which is what keeps an edited group's history attached to it.

type planDoc struct {
	Groups []store.PlanGroup `json:"groups"`
}

// todayDoc is the server's day view. Declared here rather than imported: the handler's types are
// unexported, and the CLI reads only what it prints.
type todayDoc struct {
	Day    string `json:"day"`
	Groups []struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		StartsAt      string `json:"starts_at"`
		EndsAt        string `json:"ends_at"`
		EarnedMinutes int    `json:"earned_minutes"`
		Open          bool   `json:"open"`
		Credited      int    `json:"credited_minutes"`
		Tasks         []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Note  string `json:"note"`
			State string `json:"state"`
		} `json:"tasks"`
	} `json:"groups"`
	Earned struct {
		Available int `json:"available_minutes"`
		Spent     int `json:"spent_minutes"`
		Left      int `json:"left_minutes"`
		Credits   []struct {
			EarnedOn  string `json:"earned_on"`
			ExpiresOn string `json:"expires_on"`
			Minutes   int    `json:"minutes"`
		} `json:"credits"`
	} `json:"earned"`
	// FR-28.5: today's "Mehr Zeit erbitten", newest first, for `fgctl today` and get_today.
	TimeRequests []struct {
		ID             string `json:"id"`
		Minutes        int    `json:"minutes"`
		Note           string `json:"note"`
		State          string `json:"state"`
		GrantedMinutes int    `json:"granted_minutes"`
		RequestedAt    string `json:"requested_at"`
	} `json:"time_requests"`
	TimeRequestsLeft int `json:"time_requests_left"`
}

func cmdPlan(ctx context.Context, env *environment, args []string) error {
	if len(args) != 1 && !(len(args) == 3 && args[1] == "--set") {
		return fmt.Errorf("usage: fgctl plan <child-id> [--set file.json]  (file: the document `fgctl plan <child-id> --json` prints; - reads stdin)")
	}
	path := "/api/v1/children/" + args[0] + "/plan"
	var plan planDoc
	if len(args) == 3 {
		doc, err := readPlanFile(args[2])
		if err != nil {
			return err
		}
		if err := env.client.Do(ctx, http.MethodPut, path, doc, &plan); err != nil {
			return err
		}
	} else if err := env.client.Get(ctx, path, &plan); err != nil {
		return err
	}
	return env.emit(plan, func(w *tabwriter.Writer) {
		if len(plan.Groups) == 0 {
			fmt.Fprintln(w, "no plan")
			return
		}
		for i, g := range plan.Groups {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s\t%s, %s–%s, earns %d min\t%s\n", g.Title, weekdayText(g.Weekdays), g.StartsAt, g.EndsAt, g.EarnedMinutes, g.ID)
			for _, t := range g.Tasks {
				title := t.Title
				if t.Note != "" {
					title += " (" + t.Note + ")"
				}
				fmt.Fprintf(w, "  %s\t\t%s\n", title, t.ID)
			}
		}
	})
}

// readPlanFile reads the document to set. Unknown fields are refused here rather than dropped: a
// misspelled "earned_minute" would otherwise set a group that earns nothing, silently.
func readPlanFile(name string) (*planDoc, error) {
	var r io.Reader = os.Stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var doc planDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s is not a plan document ({\"groups\": [...]}, as `fgctl plan --json` prints): %w", name, err)
	}
	return &doc, nil
}

// weekdayText says a weekday bit set (Monday 1 … Sunday 64) the way a person would.
func weekdayText(bits int) string {
	switch bits {
	case 127:
		return "every day"
	case 31:
		return "Mon–Fri"
	case 96:
		return "Sat, Sun"
	}
	names := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	var on []string
	for i, n := range names {
		if bits&(1<<i) != 0 {
			on = append(on, n)
		}
	}
	return strings.Join(on, ", ")
}

func cmdToday(ctx context.Context, env *environment, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: fgctl today <child-id>")
	}
	var day todayDoc
	if err := env.client.Get(ctx, "/api/v1/children/"+args[0]+"/today", &day); err != nil {
		return err
	}
	return env.emit(day, func(w *tabwriter.Writer) { renderToday(w, &day) })
}

func renderToday(w *tabwriter.Writer, day *todayDoc) {
	fmt.Fprintf(w, "day\t%s\n", day.Day)
	left := fmt.Sprintf("%d min", day.Earned.Left)
	if day.Earned.Left < 0 {
		left = fmt.Sprintf("%d min (a debt the next earned time settles)", day.Earned.Left)
	}
	fmt.Fprintf(w, "earned time left\t%s (available %d, spent today %d)\n", left, day.Earned.Available, day.Earned.Spent)
	for _, c := range day.Earned.Credits {
		fmt.Fprintf(w, "  credit\t%d min earned %s, usable until %s\n", c.Minutes, c.EarnedOn, c.ExpiresOn)
	}
	for _, r := range day.TimeRequests {
		state := strings.ToLower(r.State)
		if r.State == "GRANTED" {
			state = fmt.Sprintf("granted %d min", r.GrantedMinutes)
		}
		note := ""
		if r.Note != "" {
			note = " «" + r.Note + "»"
		}
		fmt.Fprintf(w, "time request	%d min%s, %s	%s\n", r.Minutes, note, state, r.ID)
	}
	if len(day.Groups) == 0 {
		fmt.Fprintln(w, "no tasks today")
		return
	}
	for _, g := range day.Groups {
		earns := fmt.Sprintf("earns %d min", g.EarnedMinutes)
		if g.Credited > 0 {
			earns = fmt.Sprintf("earned %d min", g.Credited)
		}
		window := "closed"
		if g.Open {
			window = "open now"
		}
		fmt.Fprintf(w, "\n%s\t%s–%s (%s), %s\n", g.Title, g.StartsAt, g.EndsAt, window, earns)
		for _, t := range g.Tasks {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", t.Title, strings.ToLower(t.State), t.ID)
		}
	}
}

// cmdDecide is the guardian window's Bestätigen, Nicht erledigt and Rückgängig, by the verb it was
// called as: confirm, reject or undo.
func cmdDecide(ctx context.Context, env *environment, args []string) error {
	verb := env.verb
	if len(args) != 2 {
		return fmt.Errorf("usage: fgctl %s <child-id> <task-id>", verb)
	}
	var out struct {
		Today  todayDoc           `json:"today"`
		Credit store.CreditChange `json:"credit"`
	}
	if err := env.client.Do(ctx, http.MethodPost, "/api/v1/children/"+args[0]+"/tasks/"+args[1]+"/decision",
		map[string]string{"decision": verb}, &out); err != nil {
		return err
	}
	return env.emit(out, func(w *tabwriter.Writer) {
		if out.Credit.Credited > 0 {
			fmt.Fprintf(w, "earned\t+%d min: the group is complete\n", out.Credit.Credited)
		}
		if out.Credit.Withdrawn {
			fmt.Fprintf(w, "withdrawn\tthe group is no longer complete, so its earned time is taken back\n")
		}
		renderToday(w, &out.Today)
	})
}
