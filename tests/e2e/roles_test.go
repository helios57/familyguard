package e2e

// FR-20: who may do what, measured against the real server. The unit tests in httpapi prove every
// route DECLARES its roles; these prove the declarations are what a signed-in person actually meets.

import (
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
