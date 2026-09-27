package e2e

// FR-20: who may do what, measured against the real server. The unit tests in httpapi prove every
// route DECLARES its roles; these prove the declarations are what a signed-in person actually meets.

import (
	"encoding/json"
	"net/http"
	"testing"
)

var guardianIdentity = identity{
	Email: "guardian@family.test", Subject: "google-guardian", Name: "Guardian", Verified: true,
}

func (h *harness) addParent(token, email, role string) parentDTO {
	h.t.Helper()
	var p parentDTO
	h.call(http.MethodPost, "/parents", token, map[string]any{"email": email, "role": role}).
		expect(http.StatusCreated).decode(&p)
	return p
}

func (h *harness) setRole(token, parentID, role string) apiResponse {
	return h.call(http.MethodPatch, "/parents/"+parentID, token, map[string]any{"role": role})
}

func TestARoleChangeTakesEffectOnTheNextRequest(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	added := h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	limit := map[string]any{"daily_limit_minutes": 60}
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).
		expectError(http.StatusForbidden, "forbidden")

	var promoted parentDTO
	h.setRole(primary.Token, added.ID, "ADMIN").expect(http.StatusOK).decode(&promoted)
	if promoted.Role != "ADMIN" {
		t.Fatalf("the answer says %q, expected ADMIN", promoted.Role)
	}
	// The same session token, not a new sign-in: the role is read from the database per request.
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).expect(http.StatusOK)

	h.setRole(primary.Token, added.ID, "GUARDIAN").expect(http.StatusOK)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).
		expectError(http.StatusForbidden, "forbidden")
	h.call(http.MethodGet, "/children", guardian.Token, nil).expect(http.StatusOK)
}

// Review focus 3: a key is its parent, so a demoted parent's key is demoted with them.
func TestADemotedParentsKeyIsDemotedWithThem(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	var second parentDTO
	h.call(http.MethodGet, "/me", h.signIn(secondParent).Token, nil).expect(http.StatusOK).decode(&second)
	h.setRole(primary.Token, second.ID, "PRIMARY_ADMIN").expect(http.StatusOK)
	secondSession := h.signIn(secondParent)

	var key struct {
		Token string `json:"token"`
	}
	h.call(http.MethodPost, "/api-keys", secondSession.Token, map[string]any{"name": "laptop"}).
		expect(http.StatusCreated).decode(&key)
	limit := map[string]any{"daily_limit_minutes": 45}
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", key.Token, limit).expect(http.StatusOK)

	h.setRole(primary.Token, second.ID, "GUARDIAN").expect(http.StatusOK)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", key.Token, limit).
		expectError(http.StatusForbidden, "forbidden")
	h.call(http.MethodGet, "/children", key.Token, nil).expect(http.StatusOK)
}

func TestOnlyThePrimaryAdminChangesRolesAndNeverTheirOwn(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	admin := h.signIn(secondParent)
	added := h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	var key struct {
		Token string `json:"token"`
	}
	h.call(http.MethodPost, "/api-keys", primary.Token, map[string]any{"name": "mcp"}).
		expect(http.StatusCreated).decode(&key)

	h.mustRefuse(t, []refusal{
		{what: "an admin changing a role", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: admin.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusForbidden, code: "forbidden"},
		{what: "an API key changing a role", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: key.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusForbidden, code: "api_key_forbidden"},
		{what: "the primary admin changing their own role", method: http.MethodPatch,
			path: "/parents/" + primary.Parent.ID, token: primary.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusConflict, code: "conflict", says: []string{"your own role"}},
		{what: "a role that does not exist", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: primary.Token, body: map[string]any{"role": "SUPERUSER"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"PRIMARY_ADMIN", "ADMIN", "GUARDIAN"}},
		{what: "a parent who does not exist", method: http.MethodPatch,
			path: "/parents/11111111-2222-3333-4444-555555555555", token: primary.Token,
			body: map[string]any{"role": "ADMIN"}, status: http.StatusNotFound, code: "not_found"},
	})
	// The refusals above are about who asked: the same change from the primary admin lands.
	h.setRole(primary.Token, added.ID, "ADMIN").expect(http.StatusOK)
}

// Self-change is refused, so the last-primary rule can only be reached by two primary admins
// demoting each other at once. Both requests run concurrently, round after round; after each round
// at least one primary admin must remain.
func TestTwoPrimaryAdminsCannotDemoteEachOtherToNone(t *testing.T) {
	h := newHarness(t)
	a := h.signIn(primaryParent)
	var bID string
	{
		var me parentDTO
		h.call(http.MethodGet, "/me", h.signIn(secondParent).Token, nil).expect(http.StatusOK).decode(&me)
		bID = me.ID
	}
	h.setRole(a.Token, bID, "PRIMARY_ADMIN").expect(http.StatusOK)
	for round := 0; round < 20; round++ {
		b := h.signIn(secondParent)
		done := make(chan int, 2)
		go func() { done <- h.setRole(a.Token, bID, "ADMIN").Status }()
		go func() { done <- h.setRole(b.Token, a.Parent.ID, "ADMIN").Status }()
		<-done
		<-done

		// Both are at least ADMIN whatever happened, so either may read the list.
		var list struct {
			Parents []parentDTO `json:"parents"`
		}
		h.call(http.MethodGet, "/parents", a.Token, nil).expect(http.StatusOK).decode(&list)
		var survivor string
		for _, p := range list.Parents {
			if p.Role == "PRIMARY_ADMIN" && (p.ID == a.Parent.ID || p.ID == bID) {
				survivor = p.ID
			}
		}
		if survivor == "" {
			t.Fatalf("round %d: neither primary admin is one any more", round)
		}
		// Start the next round from two primary admins again: the survivor promotes the other.
		if survivor == a.Parent.ID {
			h.setRole(a.Token, bID, "PRIMARY_ADMIN").expect(http.StatusOK)
		} else {
			h.setRole(b.Token, a.Parent.ID, "PRIMARY_ADMIN").expect(http.StatusOK)
		}
	}
}

// Every parent route that is not on the guardian allowlist, called as a guardian, against a real
// server with a real enrolled phone, so a path parameter that resolves to nothing cannot be what
// produced the refusal. The unit test in httpapi is what catches a NEW route; this list is the
// surface as of phase 1.
func TestAGuardianIsRefusedEverythingOutsideTheGuardianWindow(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	admin := h.signIn(secondParent)
	child := h.newChild(primary.Token, "Mira")
	device := h.newDevice(primary.Token, child.ID, "Mira's phone")
	_, enrollToken := h.provision(primary.Token, device.ID)
	h.enrollDevice(enrollToken, "Pixel 8", "Android 16", nil)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", primary.Token,
		map[string]any{"daily_limit_minutes": 60}).expect(http.StatusOK)
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	c, d := "/children/"+child.ID, "/devices/"+device.ID
	allowed := []struct {
		method, path string
		body         any
		status       int
	}{
		{http.MethodGet, "/me", nil, http.StatusOK},
		{http.MethodGet, "/family", nil, http.StatusOK},
		{http.MethodGet, "/children", nil, http.StatusOK},
		{http.MethodGet, "/devices?child_id=" + child.ID, nil, http.StatusOK},
		{http.MethodGet, d + "/desired-state", nil, http.StatusOK},
		{http.MethodPost, c + "/bonus", map[string]any{"minutes": 15}, http.StatusOK},
		{http.MethodGet, c + "/today", nil, http.StatusOK},
	}
	for _, a := range allowed {
		if r := h.call(a.method, a.path, guardian.Token, a.body); r.Status != a.status {
			t.Errorf("guardian %s %s: got %d, want %d\n%s", a.method, a.path, r.Status, a.status, r.Body)
		}
	}

	refused := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/dpc", nil},
		{http.MethodGet, "/family/blocked-packages", nil},
		{http.MethodPut, "/family/blocked-packages", map[string]any{"package_name": "com.example.x"}},
		{http.MethodDelete, "/family/blocked-packages?package_name=com.example.x", nil},
		{http.MethodGet, "/parents", nil},
		{http.MethodPost, "/parents", map[string]any{"email": "x@family.test", "role": "ADMIN"}},
		{http.MethodPatch, "/parents/" + primary.Parent.ID, map[string]any{"role": "ADMIN"}},
		{http.MethodDelete, "/parents/" + primary.Parent.ID, nil},
		{http.MethodPost, "/children", map[string]any{"name": "X"}},
		{http.MethodPatch, c, map[string]any{"name": "X"}},
		{http.MethodDelete, c, nil},
		{http.MethodGet, c + "/policy", nil},
		{http.MethodPatch, c + "/policy", map[string]any{"daily_limit_minutes": 600}},
		{http.MethodGet, c + "/app-rules", nil},
		{http.MethodGet, c + "/plan", nil},
		{http.MethodPut, c + "/plan", map[string]any{"groups": []any{}}},
		{http.MethodPut, c + "/app-rules", map[string]any{"package_name": "com.example.x", "action": "ALLOW"}},
		{http.MethodDelete, c + "/app-rules?package_name=com.example.x", nil},
		{http.MethodGet, c + "/blocked-domains", nil},
		{http.MethodPost, c + "/blocked-domains", map[string]any{"domain": "example.com"}},
		{http.MethodDelete, c + "/blocked-domains?domain=example.com", nil},
		{http.MethodPost, c + "/devices", map[string]any{"name": "X"}},
		{http.MethodGet, c + "/managed-apps", nil},
		{http.MethodPut, c + "/managed-apps/com.example.x", nil},
		{http.MethodDelete, c + "/managed-apps/com.example.x", nil},
		{http.MethodGet, d, nil},
		{http.MethodPatch, d, map[string]any{"name": "X"}},
		{http.MethodDelete, d, nil},
		{http.MethodPost, d + "/provisioning", nil},
		{http.MethodGet, d + "/recovery-code", nil},
		{http.MethodGet, d + "/recovery-events", nil},
		{http.MethodGet, d + "/apps", nil},
		{http.MethodDelete, d + "/apps/com.example.x", nil},
		{http.MethodGet, d + "/usage", nil},
		{http.MethodGet, d + "/usage/timeline", nil},
		{http.MethodGet, d + "/locations", nil},
		{http.MethodGet, d + "/commands", nil},
		{http.MethodPost, d + "/commands", map[string]any{"type": "RING"}},
		{http.MethodGet, d + "/debug", nil},
		{http.MethodGet, "/apps", nil},
		{http.MethodPost, "/apps", nil},
		{http.MethodPost, "/apps/scan", nil},
		{http.MethodDelete, "/apps/11111111-2222-3333-4444-555555555555", nil},
		{http.MethodGet, "/api-keys", nil},
		{http.MethodPost, "/api-keys", map[string]any{"name": "x"}},
		{http.MethodPost, "/api-keys/11111111-2222-3333-4444-555555555555/revoke", nil},
		{http.MethodDelete, "/api-keys/11111111-2222-3333-4444-555555555555", nil},
		{http.MethodGet, "/audit", nil},
	}
	for _, r := range refused {
		resp := h.call(r.method, r.path, guardian.Token, r.body)
		if resp.Status != http.StatusForbidden || resp.errorCode() != "forbidden" {
			t.Errorf("guardian %s %s: got %d %q, want 403 forbidden", r.method, r.path, resp.Status, resp.errorCode())
		}
	}
	// Control: the refusals are about the role. An admin reading the same routes is not refused.
	// Reads only, because an admin's writes here would delete the fixture under the rest of the loop.
	for _, r := range refused {
		if r.method != http.MethodGet || r.path == "/api-keys" || r.path == d+"/debug" {
			continue
		}
		if resp := h.call(r.method, r.path, admin.Token, nil); resp.Status == http.StatusForbidden {
			t.Errorf("admin GET %s was refused too, so the guardian's 403 proves nothing about roles", r.path)
		}
	}
	// The desired state is on the allowlist, but its "input" carries the child's installed apps, the
	// minutes per app and every rule — what /devices/:id/apps and /usage refuse a guardian. A guardian
	// gets the state and nothing it was computed from; an admin still gets both.
	var forGuardian, forAdmin map[string]json.RawMessage
	h.call(http.MethodGet, d+"/desired-state", guardian.Token, nil).expect(http.StatusOK).decode(&forGuardian)
	h.call(http.MethodGet, d+"/desired-state", admin.Token, nil).expect(http.StatusOK).decode(&forAdmin)
	if _, ok := forGuardian["input"]; ok {
		t.Error("a guardian's desired state carries the input: installed apps and per-app minutes by another route")
	}
	if _, ok := forGuardian["desired"]; !ok {
		t.Error("a guardian's desired state has no desired state")
	}
	if _, ok := forAdmin["input"]; !ok {
		t.Error("an admin's desired state lost its input")
	}

	// The phone still exists and the child still has a limit: nothing above landed.
	var pol struct {
		DailyLimitMinutes int `json:"daily_limit_minutes"`
	}
	h.call(http.MethodGet, c+"/policy", primary.Token, nil).expect(http.StatusOK).decode(&pol)
	if pol.DailyLimitMinutes != 60 {
		t.Errorf("a refused write landed: the daily limit is %d", pol.DailyLimitMinutes)
	}
}
