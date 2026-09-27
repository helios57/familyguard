package e2e

// FR-22 against the real server: the plan as one document, the child's "Fertig", a parent's
// confirmation earning Bonuszeit, a bonus app running on it, and the phone's reported spending
// taking it away again. Everything the phone must do is read with the device's own credential.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

const pkgMovies = "com.example.movies"

type planTaskDTO struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type planGroupDTO struct {
	ID            string        `json:"id,omitempty"`
	Title         string        `json:"title"`
	Weekdays      int           `json:"weekdays"`
	StartsAt      string        `json:"starts_at"`
	EndsAt        string        `json:"ends_at"`
	EarnedMinutes int           `json:"earned_minutes"`
	Tasks         []planTaskDTO `json:"tasks"`
}

type todayDTO struct {
	Day    string `json:"day"`
	Groups []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Open     bool   `json:"open"`
		Credited int    `json:"credited_minutes"`
		Tasks    []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			State string `json:"state"`
		} `json:"tasks"`
	} `json:"groups"`
	Earned struct {
		Available int `json:"available_minutes"`
		Spent     int `json:"spent_minutes"`
		Left      int `json:"left_minutes"`
	} `json:"earned"`
}

func (h *harness) putPlan(token, childID string, groups []planGroupDTO) []planGroupDTO {
	h.t.Helper()
	var out struct {
		Groups []planGroupDTO `json:"groups"`
	}
	h.call(http.MethodPut, "/children/"+childID+"/plan", token, map[string]any{"groups": groups}).
		expect(http.StatusOK).decode(&out)
	return out.Groups
}

func (h *harness) today(token, childID string) todayDTO {
	h.t.Helper()
	var out todayDTO
	h.call(http.MethodGet, "/children/"+childID+"/today", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

const everyDay = 127

// allDay is a window that contains every minute a test can run in but the last.
func allDay(title string, minutes int, tasks ...string) planGroupDTO {
	g := planGroupDTO{Title: title, Weekdays: everyDay, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: minutes}
	for _, t := range tasks {
		g.Tasks = append(g.Tasks, planTaskDTO{Title: t})
	}
	return g
}

func TestThePlanIsOneDocumentThatKeepsItsIDs(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")

	// A profile with no plan and no credits answers empty lists, never null: the console and the
	// phone iterate both.
	var fresh struct {
		Groups json.RawMessage `json:"groups"`
		Earned struct {
			Credits json.RawMessage `json:"credits"`
		} `json:"earned"`
	}
	h.call(http.MethodGet, "/children/"+child.ID+"/today", parent.Token, nil).expect(http.StatusOK).decode(&fresh)
	if string(fresh.Groups) != "[]" || string(fresh.Earned.Credits) != "[]" {
		t.Fatalf("a fresh profile's day: groups %s, credits %s; want [] and []", fresh.Groups, fresh.Earned.Credits)
	}

	first := h.putPlan(parent.Token, child.ID, []planGroupDTO{
		{Title: "Tag", Weekdays: everyDay, StartsAt: "07:00", EndsAt: "20:00", EarnedMinutes: 30,
			Tasks: []planTaskDTO{{Title: "Katze füttern"}, {Title: "Klavier üben", Note: "10 min"}, {Title: "Aufgaben", Note: "15 min+"}}},
		{Title: "Abend", Weekdays: everyDay, StartsAt: "20:00", EndsAt: "21:30", EarnedMinutes: 15,
			Tasks: []planTaskDTO{{Title: "Zähne putzen"}, {Title: "Pyjama"}}},
	})
	if len(first) != 2 || len(first[0].Tasks) != 3 || first[0].ID == "" || first[0].Tasks[1].Note != "10 min" {
		t.Fatalf("the plan came back as %+v", first)
	}

	// Edit the first group in place, add a task, drop the second group.
	edited := first[0]
	edited.Title = "Tagsüber"
	edited.Tasks = append(edited.Tasks, planTaskDTO{Title: "Zimmer aufräumen"})
	second := h.putPlan(parent.Token, child.ID, []planGroupDTO{edited})
	if len(second) != 1 || second[0].ID != first[0].ID || second[0].Title != "Tagsüber" || len(second[0].Tasks) != 4 {
		t.Fatalf("an edited plan came back as %+v", second)
	}
	if second[0].Tasks[0].ID != first[0].Tasks[0].ID {
		t.Error("a task kept by id got a new id, so its history would detach")
	}

	h.mustRefuse(t, []refusal{
		{what: "a group without a title", method: http.MethodPut, path: "/children/" + child.ID + "/plan",
			token: parent.Token, body: map[string]any{"groups": []planGroupDTO{allDay("", 10, "x")}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"title"}},
		{what: "a group that ends before it starts", method: http.MethodPut, path: "/children/" + child.ID + "/plan",
			token: parent.Token, body: map[string]any{"groups": []planGroupDTO{{Title: "x", Weekdays: 1, StartsAt: "20:00", EndsAt: "07:00", Tasks: []planTaskDTO{{Title: "y"}}}}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"ends after it starts"}},
		{what: "a group with no days", method: http.MethodPut, path: "/children/" + child.ID + "/plan",
			token: parent.Token, body: map[string]any{"groups": []planGroupDTO{{Title: "x", Weekdays: 0, StartsAt: "07:00", EndsAt: "08:00", Tasks: []planTaskDTO{{Title: "y"}}}}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"weekdays"}},
		{what: "a group with no tasks", method: http.MethodPut, path: "/children/" + child.ID + "/plan",
			token: parent.Token, body: map[string]any{"groups": []planGroupDTO{allDay("x", 10)}},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"at least one task"}},
		{what: "an id from nowhere", method: http.MethodPut, path: "/children/" + child.ID + "/plan",
			token: parent.Token, body: map[string]any{"groups": []planGroupDTO{{ID: "11111111-2222-3333-4444-555555555555", Title: "x", Weekdays: 1, StartsAt: "07:00", EndsAt: "08:00", Tasks: []planTaskDTO{{Title: "y"}}}}},
			status: http.StatusNotFound, code: "not_found"},
	})
}

// The whole of FR-22 in one day: report, confirm, earn, run a bonus app on it, carry a spent limit,
// spend it, and undo.
func TestAConfirmedGroupEarnsTimeThatCarriesTheDay(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": []map[string]any{
		{"package_name": pkgGame, "label": "Brawl Stars", "launchable": true},
		{"package_name": pkgMovies, "label": "Filme", "launchable": true},
	}}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgMovies, "action": "BONUS"}).expect(http.StatusOK)
	plan := h.putPlan(f.parent.Token, f.child.ID, []planGroupDTO{allDay("Tag", 30, "Katze füttern", "Klavier üben")})
	cat, piano := plan[0].Tasks[0].ID, plan[0].Tasks[1].ID

	// Without earned time a bonus app is paused, in the middle of the day.
	if d, _ := h.phoneSees(f.deviceToken()); !slices.Contains(d.SuspendedPackages, pkgMovies) {
		t.Fatalf("a bonus app runs with no earned time: %v", d.SuspendedPackages)
	}

	// The child reports one task; the phone's own answer says so.
	var reported todayDTO
	h.call(http.MethodPost, "/device/tasks/"+cat+"/report", f.deviceToken(), nil).expect(http.StatusOK).decode(&reported)
	if reported.Groups[0].Tasks[0].State != "REPORTED" {
		t.Fatalf("after Fertig the task is %q, want REPORTED", reported.Groups[0].Tasks[0].State)
	}
	// The policy the phone fetches carries the day too.
	var raw map[string]json.RawMessage
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&raw)
	if _, ok := raw["today"]; !ok {
		t.Fatal("the device policy carries no today block, so the phone cannot show its tasks")
	}

	decide := func(task, decision string) {
		t.Helper()
		h.call(http.MethodPost, "/children/"+f.child.ID+"/tasks/"+task+"/decision", f.parent.Token,
			map[string]any{"decision": decision}).expect(http.StatusOK)
	}
	decide(cat, "confirm")
	if d := h.today(f.parent.Token, f.child.ID); d.Earned.Available != 0 || d.Groups[0].Credited != 0 {
		t.Fatalf("one task of two confirmed earned something: %+v", d.Earned)
	}
	// Confirming a task nobody reported is allowed: the child forgot to tap.
	decide(piano, "confirm")
	d := h.today(f.parent.Token, f.child.ID)
	if d.Earned.Available != 30 || d.Groups[0].Credited != 30 {
		t.Fatalf("a complete group earned %+v / credited %d, want 30", d.Earned, d.Groups[0].Credited)
	}
	phone, settings := h.phoneSees(f.deviceToken())
	if string(settings["earned_available_minutes"]) != "30" {
		t.Errorf("the phone's input carries earned_available_minutes=%s, want 30", settings["earned_available_minutes"])
	}
	if slices.Contains(phone.SuspendedPackages, pkgMovies) {
		t.Error("with 30 minutes earned the bonus app is still paused")
	}

	// The budget spent: earned time carries it.
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{pkgGame: 60 * 60000},
	}).expect(http.StatusOK)
	phone, _ = h.phoneSees(f.deviceToken())
	if phone.SuspendReason != "" || slices.Contains(phone.SuspendedPackages, pkgGame) {
		t.Errorf("at the limit with 30 earned minutes: reason %q, game paused %v; earned time should carry it",
			phone.SuspendReason, slices.Contains(phone.SuspendedPackages, pkgGame))
	}

	// The phone reports 30 minutes more, all of it paid from earned time: the gold is gone.
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"samples": map[string]int64{pkgGame: 90 * 60000},
		"earned":  map[string]int64{pkgGame: 30 * 60000},
	}).expect(http.StatusOK)
	phone, _ = h.phoneSees(f.deviceToken())
	if phone.SuspendReason != "QUOTA" || !slices.Contains(phone.SuspendedPackages, pkgMovies) {
		t.Errorf("earned time spent: reason %q, movies paused %v; want QUOTA and the bonus app paused",
			phone.SuspendReason, slices.Contains(phone.SuspendedPackages, pkgMovies))
	}
	if phone.UsedMinutes != 60 {
		t.Errorf("the phone is told %d budget minutes; the 30 paid in gold must not also count (want 60)", phone.UsedMinutes)
	}
	if d := h.today(f.parent.Token, f.child.ID); d.Earned.Spent != 30 || d.Earned.Left != 0 {
		t.Errorf("today's earned: %+v, want 30 spent, 0 left", d.Earned)
	}

	// Undo takes the credit back; the spending stays, as a debt.
	decide(piano, "undo")
	if d := h.today(f.parent.Token, f.child.ID); d.Earned.Available != 0 || d.Groups[0].Credited != 0 || d.Earned.Left != -30 {
		t.Errorf("after undo: %+v, credited %d; want nothing available and a 30-minute debt", d.Earned, d.Groups[0].Credited)
	}
}

func TestAReportOutsideItsDaysIsRefusedAndDecisionsAreChecked(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	zurich, _ := time.LoadLocation("Europe/Zurich")
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"timezone": "Europe/Zurich"})
	// Every day but today, so "now" is never in it.
	notToday := everyDay &^ (1 << ((int(time.Now().In(zurich).Weekday()) + 6) % 7))
	plan := h.putPlan(f.parent.Token, f.child.ID, []planGroupDTO{
		{Title: "Nicht heute", Weekdays: notToday, StartsAt: "00:00", EndsAt: "23:59", EarnedMinutes: 10,
			Tasks: []planTaskDTO{{Title: "x"}}},
	})
	h.call(http.MethodPost, "/device/tasks/"+plan[0].Tasks[0].ID+"/report", f.deviceToken(), nil).
		expectError(http.StatusConflict, "not_now")
	if d := h.today(f.parent.Token, f.child.ID); len(d.Groups) != 0 {
		t.Errorf("a group that does not run today is listed today: %+v", d.Groups)
	}

	other := h.newChild(f.parent.Token, "Nils")
	h.mustRefuse(t, []refusal{
		{what: "a decision that is not one", method: http.MethodPost,
			path: "/children/" + f.child.ID + "/tasks/" + plan[0].Tasks[0].ID + "/decision", token: f.parent.Token,
			body: map[string]any{"decision": "maybe"}, status: http.StatusBadRequest, code: "invalid_input", says: []string{"confirm"}},
		{what: "another profile's task", method: http.MethodPost,
			path: "/children/" + other.ID + "/tasks/" + plan[0].Tasks[0].ID + "/decision", token: f.parent.Token,
			body: map[string]any{"decision": "confirm"}, status: http.StatusNotFound, code: "not_found"},
		{what: "a task that does not exist, reported", method: http.MethodPost,
			path: "/device/tasks/11111111-2222-3333-4444-555555555555/report", token: f.deviceToken(),
			status: http.StatusNotFound, code: "not_found"},
	})

	// A guardian confirms; a guardian does not edit the plan.
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	h.call(http.MethodPost, "/children/"+f.child.ID+"/tasks/"+plan[0].Tasks[0].ID+"/decision", guardian.Token,
		map[string]any{"decision": "confirm"}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/plan", guardian.Token, map[string]any{"groups": []planGroupDTO{}}).
		expectError(http.StatusForbidden, "forbidden")
}
