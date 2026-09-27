package e2e

// FR-22 in a real browser: an admin builds the daily plan, a guardian confirms tasks and sees the
// Bonuszeit they earned, and an app becomes a bonus app. Every effect is read back from the server.

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func signInBrowser(t *testing.T, h *harness, who identity) *browser {
	t.Helper()
	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(who)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	return b
}

func TestTheAdminBuildsADailyPlanInTheConsole(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .plan-card button[data-plan="add-group"]')`, 15*time.Second, "the plan card")

	b.eval(`document.querySelector('#view button[data-plan="add-group"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .plan-group input[data-field="title"]')`, 5*time.Second, "a new group")
	b.eval(`(() => {
	  const g = document.querySelector('#view .plan-group');
	  const set = (sel, v) => { const i = g.querySelector(sel); i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); };
	  set('input[data-field="title"]', 'Tag');
	  set('input[data-field="starts_at"]', '07:00');
	  set('input[data-field="ends_at"]', '20:00');
	  set('input[data-field="earned_minutes"]', '30');
	  set('.plan-task input[data-field="task-title"]', 'Katze füttern');
	  g.querySelector('button[data-plan="add-task"]').click();
	})()`, nil)
	b.waitFor(`document.querySelectorAll('#view .plan-task').length === 2`, 5*time.Second, "a second task row")
	// An open group is the widest thing on this tab: seven day buttons and three fields to a row.
	b.measure(t, "rules/plan editor").check(t, "rules/plan editor")
	b.eval(`(() => {
	  const rows = document.querySelectorAll('#view .plan-task');
	  const t = rows[1].querySelector('input[data-field="task-title"]'); t.value = 'Klavier üben'; t.dispatchEvent(new Event('input', { bubbles: true }));
	  const n = rows[1].querySelector('input[data-field="task-note"]'); n.value = '10 min'; n.dispatchEvent(new Event('input', { bubbles: true }));
	  document.querySelector('#view button[data-plan="save"]').click();
	})()`, nil)

	var plan struct {
		Groups []planGroupDTO `json:"groups"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.call(http.MethodGet, "/children/"+child.ID+"/plan", primary.Token, nil).expect(http.StatusOK).decode(&plan)
		if len(plan.Groups) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Save plan did not reach the server: %+v\n%s", plan, b.pageErrorReport())
		}
		time.Sleep(200 * time.Millisecond)
	}
	g := plan.Groups[0]
	if g.Title != "Tag" || g.StartsAt != "07:00" || g.EndsAt != "20:00" || g.EarnedMinutes != 30 || g.Weekdays != 127 {
		t.Errorf("the saved group is %+v; want Tag, 07:00–20:00, 30 min, every day", g)
	}
	if len(g.Tasks) != 2 || g.Tasks[1].Title != "Klavier üben" || g.Tasks[1].Note != "10 min" {
		t.Errorf("the saved tasks are %+v", g.Tasks)
	}

	// Edited and saved again: the same group, not a new one.
	b.waitFor(`document.querySelector('#view .plan-group input[data-field="title"]').value === 'Tag'`, 10*time.Second, "the saved plan redrawn")
	b.eval(`(() => {
	  const i = document.querySelector('#view .plan-group input[data-field="title"]'); i.value = 'Tagsüber'; i.dispatchEvent(new Event('input', { bubbles: true }));
	  document.querySelector('#view button[data-plan="save"]').click();
	})()`, nil)
	deadline = time.Now().Add(10 * time.Second)
	for {
		var again struct {
			Groups []planGroupDTO `json:"groups"`
		}
		h.call(http.MethodGet, "/children/"+child.ID+"/plan", primary.Token, nil).expect(http.StatusOK).decode(&again)
		if len(again.Groups) == 1 && again.Groups[0].Title == "Tagsüber" {
			if again.Groups[0].ID != g.ID {
				t.Error("saving an edited group created a new one, so its history would detach")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edit did not reach the server: %+v", again)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the plan editor made the page complain: %s", b.pageErrorReport())
	}
}

func TestAGuardianConfirmsTasksAndSeesBonuszeit(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	plan := h.putPlan(f.parent.Token, f.child.ID, []planGroupDTO{allDay("Tag", 30, "Katze füttern", "Klavier üben")})
	cat, piano := plan[0].Tasks[0].ID, plan[0].Tasks[1].ID
	h.call(http.MethodPost, "/device/tasks/"+cat+"/report", f.deviceToken(), nil).expect(http.StatusOK)
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	stateOf := func(task string) string {
		for _, g := range h.today(f.parent.Token, f.child.ID).Groups {
			for _, x := range g.Tasks {
				if x.ID == task {
					return x.State
				}
			}
		}
		return ""
	}
	waitState := func(task, want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for stateOf(task) != want {
			if time.Now().After(deadline) {
				t.Fatalf("task %s is %q, want %q", task, stateOf(task), want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	b := signInBrowser(t, h, guardianIdentity)
	sel := func(task, decision string) string {
		return `#view .waiting-card button[data-task="` + task + `"][data-decision="` + decision + `"]`
	}
	b.waitFor(`!!document.querySelector('`+sel(cat, "reject")+`')`, 15*time.Second, "the reported task waiting")
	b.measure(t, "guardian/waiting").check(t, "guardian/waiting")
	b.eval(`document.querySelector('`+sel(cat, "reject")+`').click()`, nil)
	waitState(cat, "REJECTED")

	card := func(task string) string {
		return `#view .guardian-card button[data-task="` + task + `"][data-decision="confirm"]`
	}
	b.waitFor(`!!document.querySelector('`+card(cat)+`')`, 10*time.Second, "the rejected task offered again on the card")
	b.eval(`document.querySelector('`+card(cat)+`').click()`, nil)
	waitState(cat, "CONFIRMED")
	b.waitFor(`!!document.querySelector('`+card(piano)+`')`, 10*time.Second, "the unreported task on the card")
	b.eval(`document.querySelector('`+card(piano)+`').click()`, nil)
	waitState(piano, "CONFIRMED")

	b.waitFor(`(document.querySelector('#view .guardian-card .earned') || {}).textContent?.includes('Bonuszeit: 30 min')`,
		10*time.Second, "the card to show 30 minutes of Bonuszeit")
	// When it runs out, as a German reader says a day — never the ISO date the API carries.
	var earnedText string
	b.eval(`document.querySelector('#view .guardian-card .earned').textContent`, &earnedText)
	if !regexp.MustCompile(`davon bis (Mo|Di|Mi|Do|Fr|Sa|So) \d{1,2}\.\d{1,2}\.$`).MatchString(earnedText) {
		t.Errorf("the Bonuszeit line does not say when it expires as a day: %q", earnedText)
	}
	// Each task over its state, not run into it ("Katze füttern" / "bestätigt").
	var stacked bool
	b.eval(`(() => {
	  const li = document.querySelector('#view .guardian-tasks li');
	  const title = li.querySelector('.label > span'), state = li.querySelector('.label > small');
	  return state.getBoundingClientRect().top >= title.getBoundingClientRect().bottom - 1;
	})()`, &stacked)
	if !stacked {
		t.Error("a task's state is drawn on the same line as its title")
	}
	var waiting int
	b.eval(`document.querySelectorAll('#view .waiting-card button[data-decision]').length`, &waiting)
	if waiting != 0 {
		t.Errorf("%d decision buttons still wait with nothing reported", waiting)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the guardian window made the page complain: %s", b.pageErrorReport())
	}
}

func TestTheAppsTabMarksABonusApp(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": []map[string]any{
		{"package_name": pkgMovies, "label": "Filme", "launchable": true},
	}}).expect(http.StatusOK)
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="apps"]').click()`, nil)
	b.waitFor(`Array.from(document.querySelectorAll('#view li')).some((li) => li.textContent.includes('`+pkgMovies+`') && li.querySelector('.seg'))`,
		15*time.Second, "the app's row")
	// The browser's own answer to the PUT is held back, so the server holds the rule before the page
	// has redrawn — the order CI's slower browser produced three times in a row, and the one a test
	// that read the row right after the server agreed could not survive.
	b.eval(`(() => {
	  const real = window.fetch;
	  window.fetch = (url, opts) => (opts && opts.method === 'PUT')
	    ? real(url, opts).then((r) => new Promise((ok) => setTimeout(() => ok(r), 800)))
	    : real(url, opts);
	})()`, nil)
	b.eval(clickCategory(pkgMovies, "Bonus app"), nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rules struct {
			Rules []struct {
				PackageName string `json:"package_name"`
				Action      string `json:"action"`
			} `json:"rules"`
		}
		h.call(http.MethodGet, "/children/"+f.child.ID+"/app-rules", f.parent.Token, nil).expect(http.StatusOK).decode(&rules)
		found := ""
		for _, r := range rules.Rules {
			if r.PackageName == pkgMovies {
				found = r.Action
			}
		}
		if found == "BONUS" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("'Bonus app' stored %q, want BONUS", found)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The row redraws when the page has its answer, which can be after the server holds the rule:
	// waited for, not read once.
	row := `Array.from(document.querySelectorAll('#view li')).find((li) => li.textContent.includes('` + pkgMovies + `'))`
	b.waitFor(`(`+row+` || {}).textContent?.includes('earned time')`, 10*time.Second, "the bonus app's row to say it runs on earned time")
}

// A bonus app with no earned time left is paused whatever the hour, and both the server's day view
// and the Activity tab must say that this is why — not "Blocked", which reads as a parent's rule.
func TestABonusAppWithoutEarnedTimeSaysWhyItIsPaused(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	zurich, _ := time.LoadLocation("Europe/Zurich")
	today := time.Now().In(zurich).Format("2006-01-02")
	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": []map[string]any{
		{"package_name": pkgMovies, "label": "Filme", "launchable": true},
	}}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgMovies, "action": "BONUS"}).expect(http.StatusOK)
	// Five minutes of it, paid in gold earlier today, so it is a row of the day.
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"day": today, "samples": map[string]int64{pkgMovies: 5 * 60_000},
		"earned": map[string]int64{pkgMovies: 5 * 60_000},
	}).expect(http.StatusOK)

	var day dayViewDTO
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline?day="+today, f.parent.Token, nil).
		expect(http.StatusOK).decode(&day)
	row := day.app(t, pkgMovies)
	if row.Rule != "BONUS" || row.Blocked != "EARNED" {
		t.Fatalf("a bonus app with no earned time reads rule=%q blocked=%q; want BONUS and EARNED", row.Rule, row.Blocked)
	}

	b := signInBrowser(t, h, primaryParent)
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	b.switchTab(t, "activity", "#view .app-bars")
	var text string
	b.eval(`document.querySelector('#view .app-bars > li').textContent`, &text)
	for _, must := range []string{"Bonus app", "runs only on earned time", "now: Paused — no earned time left"} {
		if !strings.Contains(text, must) {
			t.Errorf("the bonus app's row does not say %q: %q", must, text)
		}
	}
}
