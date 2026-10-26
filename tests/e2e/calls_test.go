package e2e

// FR-31 against the real server: the family numbers (normalised, admins only, at most 10), the
// per-child switch reaching the phone with them, the refusal while a phone of the child cannot screen
// calls, the phone's report of the calls it rejected (stored once however often it is re-sent), and
// whether the phone holds the call-screening role — remembered once seen, so the console can tell
// "never granted" from "taken away". Numbers are fictional: +1 555 01xx.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

type familyNumbersDTO struct {
	Numbers []string `json:"numbers"`
}

func (h *harness) familyNumbers(token string) familyNumbersDTO {
	h.t.Helper()
	var out familyNumbersDTO
	h.call(http.MethodGet, "/family/family-numbers", token, nil).expect(http.StatusOK).decode(&out)
	return out
}

func (h *harness) putFamilyNumbers(token string, numbers ...string) apiResponse {
	h.t.Helper()
	return h.call(http.MethodPut, "/family/family-numbers", token, map[string]any{"numbers": numbers})
}

// phoneCallSettings is what the phone is sent for FR-31, from the desired state itself.
func (h *harness) phoneCallSettings(deviceToken string) (bool, []string) {
	h.t.Helper()
	var out struct {
		Desired struct {
			CallsContactsOnly *bool    `json:"calls_contacts_only"`
			FamilyNumbers     []string `json:"family_numbers"`
		} `json:"desired"`
	}
	h.call(http.MethodGet, "/device/policy", deviceToken, nil).expect(http.StatusOK).decode(&out)
	if out.Desired.CallsContactsOnly == nil {
		h.t.Fatal("the desired state carries no calls_contacts_only")
	}
	if out.Desired.FamilyNumbers == nil {
		h.t.Fatal("the desired state carries no family_numbers (null or absent, never an empty list)")
	}
	return *out.Desired.CallsContactsOnly, out.Desired.FamilyNumbers
}

type rejectedCallDTO struct {
	Number string    `json:"number"`
	At     time.Time `json:"at"`
}

func (h *harness) rejectedCalls(token, deviceID string) []rejectedCallDTO {
	h.t.Helper()
	var out struct {
		Calls []rejectedCallDTO `json:"calls"`
	}
	h.call(http.MethodGet, "/devices/"+deviceID+"/rejected-calls?days=7", token, nil).expect(http.StatusOK).decode(&out)
	if out.Calls == nil {
		h.t.Fatal("rejected-calls answered calls=null, want a list")
	}
	return out.Calls
}

func TestFamilyNumbersAreNormalisedAndAdminOnly(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	if got := h.familyNumbers(f.parent.Token); got.Numbers == nil || len(got.Numbers) != 0 {
		t.Fatalf("a new family's numbers are %#v, want an empty list", got.Numbers)
	}

	for _, bad := range [][]string{
		{"12"},
		{"+1 555"},
		{"0"},
		{"+41 79 123 45 67 89 01 23"},
		{"079 123 45 67 ext 2"},
		{""},
		{"+15550101", "+15550102", "+15550103", "+15550104", "+15550105", "+15550106",
			"+15550107", "+15550108", "+15550109", "+15550110", "+15550111"},
	} {
		h.putFamilyNumbers(f.parent.Token, bad...).expectError(http.StatusBadRequest, "invalid_input")
	}

	var put familyNumbersDTO
	h.putFamilyNumbers(f.parent.Token,
		"079 123 45 67", "0041 79 123 45 67", "+1 (555) 010-1", " +1.555.0102 ").
		expect(http.StatusOK).decode(&put)
	want := []string{"+41791234567", "+15550101", "+15550102"}
	if !slices.Equal(put.Numbers, want) {
		t.Fatalf("stored %v, want %v: national 0 read as +41, 00 as +, separators gone, the duplicate once", put.Numbers, want)
	}
	if got := h.familyNumbers(f.parent.Token); !slices.Equal(got.Numbers, want) {
		t.Errorf("read back %v, want %v", got.Numbers, want)
	}

	// The change reaches the phone with its next policy.
	if _, numbers := h.phoneCallSettings(f.deviceToken()); !slices.Equal(numbers, []string{"+15550101", "+15550102", "+41791234567"}) {
		t.Errorf("the phone is sent %v, want the family numbers sorted", numbers)
	}

	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	h.call(http.MethodGet, "/family/family-numbers", guardian.Token, nil).expectError(http.StatusForbidden, "forbidden")
	h.putFamilyNumbers(guardian.Token).expectError(http.StatusForbidden, "forbidden")

	h.putFamilyNumbers(f.parent.Token).expect(http.StatusOK)
	if _, numbers := h.phoneCallSettings(f.deviceToken()); len(numbers) != 0 {
		t.Errorf("emptied, the phone is still sent %v", numbers)
	}
}

func TestContactsOnlySwitchReachesThePhone(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	if on, _ := h.phoneCallSettings(f.deviceToken()); on {
		t.Fatal("a new child's phone screens calls: the switch must be off until a parent turns it on")
	}
	var pol struct {
		CallsContactsOnly bool `json:"calls_contacts_only"`
	}
	h.call(http.MethodPatch, "/children/"+f.child.ID+"/policy", f.parent.Token,
		map[string]any{"calls_contacts_only": true}).expect(http.StatusOK).decode(&pol)
	if !pol.CallsContactsOnly {
		t.Fatal("the policy answered calls_contacts_only=false after it was turned on")
	}
	if on, _ := h.phoneCallSettings(f.deviceToken()); !on {
		t.Fatal("turned on, the phone is not told to screen calls")
	}

	// A patch about something else leaves it as it was.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 45})
	if on, _ := h.phoneCallSettings(f.deviceToken()); !on {
		t.Fatal("an unrelated change turned call screening off")
	}

	// A pause does not let strangers through: it takes the apps, not the protection.
	h.pause(f.parent.Token, f.child.ID, true).expect(http.StatusOK)
	if on, _ := h.phoneCallSettings(f.deviceToken()); !on {
		t.Fatal("a pause turned call screening off")
	}

	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"calls_contacts_only": false})
	if on, _ := h.phoneCallSettings(f.deviceToken()); on {
		t.Fatal("turned off, the phone still screens calls")
	}
}

// FR-31.1: a phone before 0.6.47 (build 56) has no screening service, so the console would say
// "only contacts" while every stranger rings. The server refuses that and names the phone.
func TestContactsOnlyIsRefusedWhileAPhoneIsOutdated(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	beat := func(code int) {
		t.Helper()
		h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
			"connectivity": "wifi", "app_version_name": "0.6.46", "app_version_code": code,
		}).expect(http.StatusOK)
	}
	on := map[string]any{"calls_contacts_only": true}

	beat(55)
	r := h.call(http.MethodPatch, "/children/"+f.child.ID+"/policy", f.parent.Token, on).
		expectError(http.StatusConflict, "devices_outdated")
	if !strings.Contains(string(r.Body), f.device.Name) || !strings.Contains(string(r.Body), "0.6.47") {
		t.Errorf("the refusal does not name the phone %q and the build it needs: %s", f.device.Name, r.Body)
	}
	if on, _ := h.phoneCallSettings(f.deviceToken()); on {
		t.Fatal("a refused change reached the phone")
	}
	// Turning it off is never refused: an old phone does not screen either way.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"calls_contacts_only": false})

	beat(56)
	h.patchPolicy(f.parent.Token, f.child.ID, on)
}

// FR-31.5: the phone re-sends what it has not seen acknowledged, so the same call can arrive twice.
func TestRejectedCallReportIsIdempotent(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	at := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	report := map[string]any{"calls": []map[string]any{
		{"id": "c-1", "number": "+15550188", "at": at.Format(time.RFC3339)},
		{"id": "c-2", "number": "+15550199", "at": at.Add(time.Minute).Format(time.RFC3339)},
	}}

	var first struct {
		Accepted int `json:"accepted"`
	}
	h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), report).expect(http.StatusOK).decode(&first)
	if first.Accepted != 2 {
		t.Fatalf("accepted %d, want 2", first.Accepted)
	}
	// The response was lost; the phone sends the same two again. Still 200, so it can acknowledge,
	// and nothing new: the console's count of rejected calls must not grow with the retries.
	var again struct {
		Accepted int `json:"accepted"`
	}
	h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), report).expect(http.StatusOK).decode(&again)
	if again.Accepted != 0 {
		t.Errorf("a re-send was accepted as %d new calls, want 0", again.Accepted)
	}

	got := h.rejectedCalls(f.parent.Token, f.device.ID)
	if len(got) != 2 {
		t.Fatalf("stored %d rejected calls after a re-send, want 2: %+v", len(got), got)
	}
	if got[0].Number != "+15550199" || !got[0].At.Equal(at.Add(time.Minute)) || got[1].Number != "+15550188" {
		t.Errorf("rejected calls %+v, want newest first with their numbers and times", got)
	}

	for _, bad := range []map[string]any{
		{"calls": []map[string]any{{"id": "", "number": "+15550188", "at": at.Format(time.RFC3339)}}},
		{"calls": []map[string]any{{"id": "c-3", "number": "+15550188", "at": "yesterday"}}},
		{"calls": []map[string]any{{"id": "c-4", "number": strings.Repeat("9", 65), "at": at.Format(time.RFC3339)}}},
	} {
		h.call(http.MethodPost, "/device/rejected-calls", f.deviceToken(), bad).expectError(http.StatusBadRequest, "invalid_input")
	}

	// Usage-class data: the guardian window does not show it (FR-20.1).
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/rejected-calls", guardian.Token, nil).expectError(http.StatusForbidden, "forbidden")
}

// FR-31.4: amber "never granted" and red "taken away" need the server to remember that the role was
// held once, because the phone only ever says what is true now.
func TestCallScreeningHeldIsRememberedOnceSeen(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	state := func() map[string]json.RawMessage {
		t.Helper()
		var v struct {
			State map[string]json.RawMessage `json:"state"`
		}
		h.call(http.MethodGet, "/devices/"+f.device.ID, f.parent.Token, nil).expect(http.StatusOK).decode(&v)
		return v.State
	}
	beat := func(body map[string]any) {
		t.Helper()
		body["connectivity"] = "wifi"
		h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), body).expect(http.StatusOK)
	}

	beat(map[string]any{})
	if st := state(); st["call_screening"] != nil || st["call_screening_held_at"] != nil {
		t.Fatalf("a heartbeat that said nothing was recorded as call_screening=%s held_at=%s", st["call_screening"], st["call_screening_held_at"])
	}
	beat(map[string]any{"call_screening": false})
	if st := state(); string(st["call_screening"]) != "false" || st["call_screening_held_at"] != nil {
		t.Fatalf("never held: call_screening=%s held_at=%s, want false and no time", st["call_screening"], st["call_screening_held_at"])
	}
	beat(map[string]any{"call_screening": true})
	held := state()["call_screening_held_at"]
	if held == nil {
		t.Fatal("the role was reported held and no time was recorded")
	}
	beat(map[string]any{"call_screening": false})
	if st := state(); string(st["call_screening"]) != "false" || string(st["call_screening_held_at"]) != string(held) {
		t.Fatalf("taken away: call_screening=%s held_at=%s, want false and the first time %s kept", st["call_screening"], st["call_screening_held_at"], held)
	}
	// Silence keeps what was said.
	beat(map[string]any{})
	if st := state(); string(st["call_screening"]) != "false" {
		t.Fatalf("a heartbeat that said nothing changed call_screening to %s", st["call_screening"])
	}
	beat(map[string]any{"call_screening": true})
	if st := state(); string(st["call_screening_held_at"]) != string(held) {
		t.Errorf("held again: held_at moved from %s to %s, want the first time kept", held, st["call_screening_held_at"])
	}
}
