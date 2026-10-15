package e2e

// FR-29: a sign-in lasts while it is used, ends on the server when the parent signs out, and every
// parent can see the family's sign-ins and end their own.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type signInDTO struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parent_id"`
	ParentEmail string    `json:"parent_email"`
	Label       string    `json:"label"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	Current     bool      `json:"current"`
}

func (h *harness) signIns(token string) []signInDTO {
	h.t.Helper()
	var out struct {
		SignIns []signInDTO `json:"sign_ins"`
	}
	h.call(http.MethodGet, "/sign-ins", token, nil).expect(http.StatusOK).decode(&out)
	return out.SignIns
}

// current returns the one sign-in the list marks as the caller's, failing when there is not exactly
// one: a list that marks none, or two, cannot tell a parent which entry is the browser in their hand.
func current(t *testing.T, list []signInDTO) signInDTO {
	t.Helper()
	var found []signInDTO
	for _, s := range list {
		if s.Current {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d sign-ins are marked current, expected exactly one: %+v", len(found), list)
	}
	return found[0]
}

func listed(list []signInDTO, id string) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

// The heart of FR-29, both halves on one credential. Used more often than the idle timeout, a
// sign-in outlives that timeout several times over — which a token with a fixed lifetime cannot do,
// so this half goes red if use stops extending it. Then left alone, the same token is refused — which
// a token that never lapses cannot do, so the second half goes red if the timeout stops being applied.
func TestASignInLastsWhileItIsUsedAndEndsWhenItIsNot(t *testing.T) {
	const idle = 3 * time.Second
	h := newHarness(t, withEnv("SIGN_IN_IDLE_TIMEOUT", idle.String()))
	s := h.signIn(primaryParent)

	until := time.Now().Add(3 * idle)
	uses := 0
	for time.Now().Before(until) {
		h.call(http.MethodGet, "/me", s.Token, nil).expect(http.StatusOK)
		uses++
		time.Sleep(time.Second)
	}
	if uses < 6 {
		t.Fatalf("only %d uses in %s; the loop did not exercise the timeout", uses, 3*idle)
	}

	time.Sleep(idle + 1500*time.Millisecond)
	resp := h.call(http.MethodGet, "/me", s.Token, nil)
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("a sign-in idle for longer than %s answered %d, expected 401\nbody: %s", idle, resp.Status, resp.Body)
	}
	// And signing in again is all it takes: the lapse is of that sign-in, not of the parent.
	h.call(http.MethodGet, "/me", h.signIn(primaryParent).Token, nil).expect(http.StatusOK)
}

// Signing out ends the sign-in on the server, and only that one: the same parent on another device
// stays signed in.
func TestSigningOutEndsTheSignInOnTheServer(t *testing.T) {
	h := newHarness(t)
	laptop := h.signIn(primaryParent)
	phone := h.signIn(primaryParent)

	h.call(http.MethodGet, "/me", laptop.Token, nil).expect(http.StatusOK)
	h.call(http.MethodDelete, "/sign-ins/current", laptop.Token, nil).expect(http.StatusNoContent)

	if resp := h.call(http.MethodGet, "/me", laptop.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Fatalf("a signed-out token answered %d, expected 401\nbody: %s", resp.Status, resp.Body)
	}
	h.call(http.MethodGet, "/me", phone.Token, nil).expect(http.StatusOK)
	for _, s := range h.signIns(phone.Token) {
		if s.ParentEmail == primaryParent.Email && !s.Current {
			t.Errorf("the signed-out laptop is still listed: %+v", s)
		}
	}
}

// FR-29.4 and FR-29.5, through the API: who may see which sign-in, and who may end it.
func TestEveryParentSeesTheFamilysSignInsAndEndsTheirOwn(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	primaryPhone := h.signIn(primaryParent)

	guardianIdentity := identity{Email: "guardian@family.test", Subject: "google-guardian", Name: "Guardian", Verified: true}
	var guardianParent parentDTO
	h.call(http.MethodPost, "/parents", primary.Token, map[string]any{"email": guardianIdentity.Email, "role": "GUARDIAN"}).
		expect(http.StatusCreated).decode(&guardianParent)
	guardian := h.signIn(guardianIdentity)

	// Everyone sees everyone's — a guardian included — and each list marks its reader's own.
	seen := h.signIns(guardian.Token)
	if len(seen) != 3 {
		t.Fatalf("the guardian sees %d sign-ins, expected 3 (two of the primary's, one own): %+v", len(seen), seen)
	}
	own := current(t, seen)
	if own.ParentID != guardianParent.ID || own.ParentEmail != guardianIdentity.Email {
		t.Fatalf("the guardian's list marks %+v as theirs", own)
	}
	for _, s := range seen {
		if s.Label == "" || s.CreatedAt.IsZero() || s.LastUsedAt.IsZero() {
			t.Errorf("a sign-in without its label or its times: %+v", s)
		}
	}
	primaryOwn := current(t, h.signIns(primary.Token))

	// A guardian cannot end someone else's sign-in, and the refusal changes nothing.
	resp := h.call(http.MethodDelete, "/sign-ins/"+primaryOwn.ID, guardian.Token, nil)
	if resp.Status != http.StatusForbidden {
		t.Fatalf("a guardian ending the primary's sign-in answered %d, expected 403\nbody: %s", resp.Status, resp.Body)
	}
	h.call(http.MethodGet, "/me", primary.Token, nil).expect(http.StatusOK)

	// A primary admin can: the other phone of the primary ends the laptop's sign-in.
	h.call(http.MethodDelete, "/sign-ins/"+primaryOwn.ID, primaryPhone.Token, nil).expect(http.StatusNoContent)
	if resp := h.call(http.MethodGet, "/me", primary.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Fatalf("an ended sign-in answered %d, expected 401", resp.Status)
	}
	if listed(h.signIns(primaryPhone.Token), primaryOwn.ID) {
		t.Error("the ended sign-in is still listed")
	}
	if resp := h.call(http.MethodDelete, "/sign-ins/"+primaryOwn.ID, primaryPhone.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("ending an ended sign-in answered %d, expected 404", resp.Status)
	}

	// An API key may look, and may not end one (FR-17.2).
	var key apiKeyDTO
	h.call(http.MethodPost, "/api-keys", primaryPhone.Token, map[string]any{"name": "script"}).
		expect(http.StatusCreated).decode(&key)
	viaKey := h.signIns(key.Token)
	for _, s := range viaKey {
		if s.Current {
			t.Errorf("an API key's list marks a sign-in as its own: %+v", s)
		}
	}
	resp = h.call(http.MethodDelete, "/sign-ins/"+own.ID, key.Token, nil)
	if resp.Status != http.StatusForbidden || resp.errorCode() != "api_key_forbidden" {
		t.Fatalf("an API key ending a sign-in answered %d %q, expected 403 api_key_forbidden", resp.Status, resp.errorCode())
	}
	if resp := h.call(http.MethodDelete, "/sign-ins/current", key.Token, nil); resp.Status != http.StatusForbidden {
		t.Fatalf("an API key signing itself out answered %d, expected 403", resp.Status)
	}

	// The guardian ends their own.
	h.call(http.MethodDelete, "/sign-ins/"+own.ID, guardian.Token, nil).expect(http.StatusNoContent)
	if resp := h.call(http.MethodGet, "/me", guardian.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Fatalf("the guardian's ended sign-in answered %d, expected 401", resp.Status)
	}

	// Removing a parent ends their sign-ins with them.
	again := h.signIn(guardianIdentity)
	h.call(http.MethodGet, "/me", again.Token, nil).expect(http.StatusOK)
	h.call(http.MethodDelete, "/parents/"+guardianParent.ID, primaryPhone.Token, nil).expect(http.StatusNoContent)
	if resp := h.call(http.MethodGet, "/me", again.Token, nil); resp.Status != http.StatusUnauthorized {
		t.Fatalf("a removed parent's sign-in answered %d, expected 401", resp.Status)
	}
	for _, s := range h.signIns(primaryPhone.Token) {
		if s.ParentID == guardianParent.ID {
			t.Errorf("a removed parent's sign-in is still listed: %+v", s)
		}
	}
	if resp := h.call(http.MethodDelete, "/sign-ins/not-a-uuid", primaryPhone.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("a malformed sign-in id answered %d, expected 400", resp.Status)
	}
}

// In a real browser: the console keeps no expiry of its own, lists this browser as the current
// device, and its Abmelden ends the sign-in on the server — a copy of the token taken before the
// click is refused after it.
func TestTheConsoleStaysSignedInAndSignsOutOnTheServer(t *testing.T) {
	h := newHarness(t)
	b := signInBrowser(t, h, primaryParent)

	var stored string
	b.eval(`localStorage.getItem('fg.session')`, &stored)
	if !strings.Contains(stored, `"token"`) {
		t.Fatalf("the console stored %q, expected a session with a token", stored)
	}
	var token string
	b.eval(`JSON.parse(localStorage.getItem('fg.session')).token`, &token)
	h.call(http.MethodGet, "/me", token, nil).expect(http.StatusOK)

	// A reload is a new page with nothing in memory: it must come back signed in.
	b.navigate(h.base + "/")
	b.waitFor("!document.getElementById('app').hidden", 15*time.Second, "the console after a reload")

	b.eval(`document.querySelector('.tab[data-tab="family"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view [data-signins] li.is-current')`, 15*time.Second, "this browser in Angemeldete Geräte")
	var label string
	b.eval(`document.querySelector('#view [data-signins] li.is-current').textContent`, &label)
	if !strings.Contains(label, "Chrome") || !strings.Contains(label, "Dieses Gerät") {
		t.Errorf("this browser is listed as %q; expected its browser and 'Dieses Gerät'", label)
	}

	b.eval(`document.querySelector('#view button[data-action="signout"]').click()`, nil)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := h.call(http.MethodGet, "/me", token, nil)
		if resp.Status == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the token from before Abmelden still answers %d", resp.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var after any
	b.eval(`localStorage.getItem('fg.session')`, &after)
	if after != nil {
		t.Errorf("the console kept %v after signing out", after)
	}
}
