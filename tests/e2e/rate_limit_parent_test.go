package e2e

// The limit a parent meets while working (NFR-2), against a server configured the way the cluster
// configures it.
//
// The owner's words, 2026-09-20, mid-way through answering the queue of apps waiting on a phone:
// "i just got an error on the website 'too many requests on your family' as i was approving the
// apps. thats SHIT, i want to be able to approve 100 apps within one minute". The console said
// "Could not load your family: too many requests" and stopped, because every tap spent several
// requests out of a budget that existed for anonymous callers.
//
// Nothing here is new API surface — it is the same PUT the console has always sent. What is
// measured is that a hundred of them, plus the reads a console interleaves, fit inside one minute
// on a deployment that did not raise any limit. The harness normally sets RATE_LIMIT_PER_MINUTE to
// 6000 so long journeys do not race a limit that is not what they are measuring; this test removes
// that override on purpose, because the value the cluster runs is the one the owner hit.

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestAParentCanAnswerAHundredWaitingAppsInOneMinute(t *testing.T) {
	const apps = 100

	// No override: the server takes RATE_LIMIT_PER_MINUTE's own default, which is what the cluster
	// runs. A test that raised it would be measuring a deployment nobody has.
	h := newHarness(t, withoutEnv("RATE_LIMIT_PER_MINUTE"))
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "The blue phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy S20", "Android 13", nil)

	// A phone that has just had a hundred things installed on it, with the switch that makes each
	// of them wait for a parent.
	h.patchPolicy(parent.Token, child.ID, map[string]any{"allow_child_installs": false})
	inventory := make([]map[string]any, 0, apps)
	packages := make([]string, 0, apps)
	for i := 0; i < apps; i++ {
		pkg := fmt.Sprintf("com.example.waiting%03d", i)
		packages = append(packages, pkg)
		inventory = append(inventory, map[string]any{"package_name": pkg, "label": fmt.Sprintf("App %03d", i)})
	}
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken,
		map[string]any{"apps": inventory}).expect(http.StatusOK)

	start := time.Now()
	requests := 0
	refused := 0
	firstRefusalAt := -1
	note := func(i int, status int) {
		requests++
		if status == http.StatusTooManyRequests {
			refused++
			if firstRefusalAt < 0 {
				firstRefusalAt = i
			}
		}
	}
	for i, pkg := range packages {
		// One answer, exactly as the console sends it.
		res := h.call(http.MethodPut, "/children/"+child.ID+"/app-rules", parent.Token,
			map[string]any{"package_name": pkg, "action": "LIMIT", "limit_minutes": 0})
		note(i, res.Status)
		if res.Status != http.StatusTooManyRequests &&
			res.Status != http.StatusOK && res.Status != http.StatusCreated && res.Status != http.StatusNoContent {
			t.Fatalf("answering app %d (%s) failed with %d: %s", i, pkg, res.Status, res.Body)
		}
		// A read after each answer, because a console draws the result of the tap it just took.
		// /family is the one whose refusal the owner actually saw, in the words the page printed:
		// "Could not load your family: too many requests".
		note(i, h.call(http.MethodGet, "/family", parent.Token, nil).Status)
	}
	elapsed := time.Since(start)

	// The test must cross the budget it is about. Written because the first version of this loop
	// spent 118 requests against an anonymous budget of 120 and passed against the very defect it
	// was written for — a test that sits just under the threshold measures nothing and reads as
	// coverage.
	if requests <= defaultAnonymousPerMinute {
		t.Fatalf("this run spent %d requests against an anonymous budget of %d; it would pass "+
			"whether or not a parent has a budget of their own", requests, defaultAnonymousPerMinute)
	}
	if refused > 0 {
		t.Fatalf("a signed-in parent was refused %d of %d requests while answering %d apps, first at "+
			"app %d (%.1fs in). This is the owner's report: approving apps must not spend an "+
			"anonymous caller's budget.", refused, requests, apps, firstRefusalAt, elapsed.Seconds())
	}
	if elapsed >= time.Minute {
		t.Fatalf("answering %d apps took %v; the requirement is a hundred inside one minute", apps, elapsed)
	}

	// Every answer landed. A run that was never refused but also never wrote anything would pass
	// every assertion above.
	rules := listRules(t, h, parent.Token, child.ID)
	if len(rules) != apps {
		t.Fatalf("%d rules were stored for %d answers; the requests were accepted and did nothing",
			len(rules), apps)
	}

	// ---- the other half: the strict bucket is still strict ----
	//
	// Without this, a limiter that had simply been switched off would pass everything above. The
	// anonymous surface on this same server, with no credential, must still refuse — and it must
	// refuse at the anonymous budget rather than at the flood ceiling, which is what tells a
	// raised limit apart from a re-keyed one.
	served, blocked := drain(h, "")
	if blocked.Status != http.StatusTooManyRequests {
		t.Fatalf("the unauthenticated surface served %d requests without ever refusing; raising the "+
			"limit is not the fix, re-keying it is", served)
	}
	if served > 2*defaultAnonymousPerMinute {
		t.Fatalf("the unauthenticated surface served %d requests before refusing; its budget is %d "+
			"per minute, so the strict bucket is no longer strict",
			served, defaultAnonymousPerMinute)
	}
	// And the parent is unaffected by the anonymous flood that just happened from the same address:
	// the two no longer share a bucket, which is the actual change.
	h.call(http.MethodGet, "/family", parent.Token, nil).expect(http.StatusOK)
}

// defaultAnonymousPerMinute is RATE_LIMIT_PER_MINUTE's default in backend/internal/config. Written
// here rather than imported because the e2e module deliberately links nothing from the server it
// drives: it is a black-box suite, and a constant it shared with the code under test would be one
// fewer thing the test can disagree with.
const defaultAnonymousPerMinute = 120
