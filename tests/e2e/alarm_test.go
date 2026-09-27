package e2e

// FR-23 against the real server: a profile's alarm clock as a weekly schedule plus changes for one
// date, what the phone is sent so it can ring offline, and the checks on both.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

type alarmDayDTO struct {
	Day  string  `json:"day"`
	Time *string `json:"time"`
}

type alarmDTO struct {
	Weekdays  []string      `json:"weekdays"`
	Overrides []alarmDayDTO `json:"overrides"`
}

func (h *harness) getAlarm(token, childID string) alarmDTO {
	h.t.Helper()
	var out alarmDTO
	h.call(http.MethodGet, "/children/"+childID+"/alarm", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

func zurichDay(offset int) string {
	zurich, _ := time.LoadLocation("Europe/Zurich")
	return time.Now().In(zurich).AddDate(0, 0, offset).Format("2006-01-02")
}

func strp(s string) *string { return &s }

func TestTheAlarmIsAWeekAndChangesForOneDay(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"timezone": "Europe/Zurich"})
	c := "/children/" + f.child.ID

	// Nothing set: seven days off, no changes — never null, so a client can index it.
	if a := h.getAlarm(f.parent.Token, f.child.ID); len(a.Weekdays) != 7 || slices.ContainsFunc(a.Weekdays, func(s string) bool { return s != "" }) || a.Overrides == nil {
		t.Fatalf("a profile with no alarm reads %+v; want seven empty days and an empty list", a)
	}

	week := []string{"06:30", "06:30", "06:30", "06:30", "07:00", "", ""}
	h.call(http.MethodPut, c+"/alarm", f.parent.Token, map[string]any{"weekdays": week}).expect(http.StatusOK)
	if a := h.getAlarm(f.parent.Token, f.child.ID); !slices.Equal(a.Weekdays, week) {
		t.Fatalf("the stored week is %v, want %v", a.Weekdays, week)
	}
	for _, bad := range []any{
		map[string]any{"weekdays": []string{"06:30"}},
		map[string]any{"weekdays": []string{"25:00", "", "", "", "", "", ""}},
		map[string]any{"weekdays": []string{"6:30", "", "", "", "", "", ""}},
	} {
		if r := h.call(http.MethodPut, c+"/alarm", f.parent.Token, bad); r.Status != http.StatusBadRequest {
			t.Errorf("PUT alarm %v answered %d, want 400", bad, r.Status)
		}
	}

	// A change for tomorrow, and "off" the day after; both in the profile's own calendar.
	tomorrow, after := zurichDay(1), zurichDay(2)
	h.call(http.MethodPut, c+"/alarm/days/"+tomorrow, f.parent.Token, map[string]any{"time": "06:00"}).expect(http.StatusOK)
	h.call(http.MethodPut, c+"/alarm/days/"+after, f.parent.Token, map[string]any{"time": nil}).expect(http.StatusOK)
	a := h.getAlarm(f.parent.Token, f.child.ID)
	if len(a.Overrides) != 2 || a.Overrides[0].Day != tomorrow || a.Overrides[0].Time == nil || *a.Overrides[0].Time != "06:00" ||
		a.Overrides[1].Day != after || a.Overrides[1].Time != nil {
		t.Fatalf("the date changes read %+v; want %s 06:00 and %s off, in date order", a.Overrides, tomorrow, after)
	}
	// Replaced, not added: one change per day.
	h.call(http.MethodPut, c+"/alarm/days/"+tomorrow, f.parent.Token, map[string]any{"time": "05:45"}).expect(http.StatusOK)
	if a := h.getAlarm(f.parent.Token, f.child.ID); len(a.Overrides) != 2 || *a.Overrides[0].Time != "05:45" {
		t.Errorf("changing tomorrow again should replace it: %+v", a.Overrides)
	}

	for day, want := range map[string]int{
		zurichDay(-1): http.StatusBadRequest, // the past cannot ring
		zurichDay(61): http.StatusBadRequest, // further than 60 days
		"2026-02-30":  http.StatusBadRequest,
		zurichDay(0):  http.StatusOK, // today still can
		zurichDay(60): http.StatusOK,
	} {
		if r := h.call(http.MethodPut, c+"/alarm/days/"+day, f.parent.Token, map[string]any{"time": "06:00"}); r.Status != want {
			t.Errorf("a change for %s answered %d, want %d", day, r.Status, want)
		}
	}
	if r := h.call(http.MethodPut, c+"/alarm/days/"+tomorrow, f.parent.Token, map[string]any{"time": "6"}); r.Status != http.StatusBadRequest {
		t.Errorf("a malformed time answered %d, want 400", r.Status)
	}
	h.call(http.MethodDelete, c+"/alarm/days/"+after, f.parent.Token, nil).expect(http.StatusNoContent)
	for _, o := range h.getAlarm(f.parent.Token, f.child.ID).Overrides {
		if o.Day == after {
			t.Errorf("the change for %s is still there after DELETE", after)
		}
	}

	// ---- what the phone is sent: the rule, its zone, and the changes from today on ----
	var raw map[string]json.RawMessage
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&raw)
	var block struct {
		Timezone  string        `json:"timezone"`
		Weekdays  []string      `json:"weekdays"`
		Overrides []alarmDayDTO `json:"overrides"`
	}
	if err := json.Unmarshal(raw["alarm"], &block); err != nil {
		t.Fatalf("the device policy carries no readable alarm block (%v): %s", err, raw["alarm"])
	}
	if block.Timezone != "Europe/Zurich" || !slices.Equal(block.Weekdays, week) || len(block.Overrides) != 3 {
		t.Errorf("the phone is sent %+v; want the zone, the week and three changes (today, tomorrow, day 60)", block)
	}

	// ---- audit ----
	var actions []string
	for _, e := range h.readAudit(f.parent.Token) {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"ALARM_UPDATED", "ALARM_DAY_SET", "ALARM_DAY_CLEARED"} {
		if !slices.Contains(actions, want) {
			t.Errorf("the audit log has no %s: %v", want, actions)
		}
	}
}

func TestAGuardianCannotChangeTheAlarm(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	c := "/children/" + child.ID
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, c + "/alarm", nil},
		{http.MethodPut, c + "/alarm", map[string]any{"weekdays": []string{"", "", "", "", "", "", ""}}},
		{http.MethodPut, c + "/alarm/days/" + zurichDay(1), map[string]any{"time": nil}},
		{http.MethodDelete, c + "/alarm/days/" + zurichDay(1), nil},
	} {
		if resp := h.call(r.method, r.path, guardian.Token, r.body); resp.Status != http.StatusForbidden {
			t.Errorf("guardian %s %s: got %d, want 403", r.method, r.path, resp.Status)
		}
	}
	// Control: the same read is not refused to an admin.
	h.call(http.MethodGet, c+"/alarm", primary.Token, nil).expect(http.StatusOK)
}
