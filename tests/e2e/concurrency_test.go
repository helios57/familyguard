package e2e

import (
	"net/http"
	"sync"
	"testing"
)

// raceCalls fires n calls at once — released together by one channel close, so they reach the
// server as close to the same instant as the client can make them — and returns each status.
func raceCalls(n int, call func(i int) int) []int {
	statuses := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i] = call(i)
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}

func count(statuses []int, want int) int {
	n := 0
	for _, s := range statuses {
		if s == want {
			n++
		}
	}
	return n
}

// TestConcurrentWritesKeepTheirInvariants holds the three places where two writers at once could
// mint time to their rules (FR-22, FR-28). Every other test drives them one call at a time, which
// cannot tell a transaction that serialises from one that merely was not raced.
//
//   - Confirming the last two tasks of a group at the same instant earns the group's minutes ONCE.
//   - A child tapping "Mehr Zeit erbitten" many times at once opens ONE request.
//   - Two parents answering the same request at once grant its minutes ONCE.
func TestConcurrentWritesKeepTheirInvariants(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	// ---- a group completed twice at once ----
	plan := h.putPlan(f.parent.Token, f.child.ID, []planGroupDTO{allDay("Tag", 30, "Katze füttern", "Klavier üben")})
	tasks := []string{plan[0].Tasks[0].ID, plan[0].Tasks[1].ID}
	decide := func(task, decision string) int {
		return h.call(http.MethodPost, "/children/"+f.child.ID+"/tasks/"+task+"/decision", f.parent.Token,
			map[string]any{"decision": decision}).Status
	}
	// Several rounds, because one race won by the right side proves little: undo both, then
	// confirm both at once, and the group must be credited exactly its 30 minutes every time.
	for round := range 5 {
		if round > 0 {
			for _, task := range tasks {
				if s := decide(task, "undo"); s != http.StatusOK {
					t.Fatalf("round %d: undo answered %d", round, s)
				}
			}
			if d := h.today(f.parent.Token, f.child.ID); d.Groups[0].Credited != 0 {
				t.Fatalf("round %d: after undoing both tasks the group is still credited %d", round, d.Groups[0].Credited)
			}
		}
		statuses := raceCalls(2, func(i int) int { return decide(tasks[i], "confirm") })
		if count(statuses, http.StatusOK) != 2 {
			t.Fatalf("round %d: two confirmations at once answered %v", round, statuses)
		}
		d := h.today(f.parent.Token, f.child.ID)
		if d.Groups[0].Credited != 30 || d.Earned.Available != 30 {
			// Both ways a race shows: credited twice (each saw the other done), or never (each saw
			// the other still open) — the second is what removing the row lock produced.
			t.Fatalf("round %d: a group of 30 minutes completed by two confirmations at once is credited %d, "+
				"with %d available; want exactly 30", round, d.Groups[0].Credited, d.Earned.Available)
		}
	}

	// ---- many taps at once ----
	statuses := raceCalls(8, func(int) int {
		return h.call(http.MethodPost, "/device/time-requests", f.deviceToken(), map[string]any{"minutes": 15}).Status
	})
	if count(statuses, http.StatusOK) != 1 || count(statuses, http.StatusConflict) != 7 {
		t.Fatalf("eight requests at once answered %v: want exactly one accepted and seven refused as already asked", statuses)
	}
	open := h.requests(f.parent.Token, f.child.ID)
	if len(open.TimeRequests) != 1 || open.TimeRequests[0].State != "OPEN" {
		t.Fatalf("after eight taps at once the day holds %+v, want one open request", open.TimeRequests)
	}

	// ---- two parents, one answer ----
	before := h.desiredState(f.parent.Token, f.device.ID, "").Desired.BonusMinutes
	path := "/children/" + f.child.ID + "/time-requests/" + open.TimeRequests[0].ID + "/decision"
	statuses = raceCalls(2, func(i int) int {
		token := f.parent.Token
		if i == 1 {
			token = guardian.Token
		}
		return h.call(http.MethodPost, path, token, map[string]any{"decision": "grant"}).Status
	})
	if count(statuses, http.StatusOK) != 1 || count(statuses, http.StatusConflict) != 1 {
		t.Fatalf("two grants of one request at once answered %v: want one accepted, one already decided", statuses)
	}
	if after := h.desiredState(f.parent.Token, f.device.ID, "").Desired.BonusMinutes; after-before != 15 {
		t.Fatalf("a 15-minute request granted by two parents at once added %d minutes of Extrazeit", after-before)
	}
}
