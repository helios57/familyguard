package e2e

// FR-31 on a real Android device: with the switch on and the call-screening role held, a call from a
// contact rings, a call from a stranger does not — and is logged only as blocked, never as missed,
// and reaches the console — and a family number that is not a contact rings too. The switch off lets the stranger
// ring (the negative control), and the role taken away is reported to the console as red.
// Driven by tests/android/calls.sh; run on its own it SKIPS.
//
// The decision is unit-tested in CallDecisionTest. What only a device can show is the platform's
// part: that Telecom binds the screening service at all, that it passes only non-contacts, that the
// SIM's own normaliser reads a national number, and that a rejected call really never rings —
// read from Telecom's call list (`dumpsys telecom`) and the call log, never from the app's claim.

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const callScreeningRole = "android.app.role.CALL_SCREENING"

// The numbers, as the emulator's modem dials them in (national, US SIM) and as the console holds
// them. 555-01xx is reserved for fiction in every NANP area code. Each ends in its own two digits,
// because Telecom's dump masks all but the last two.
const (
	contactNational  = "6505550111"
	familyNational   = "6505550123"
	familyE164       = "+16505550123"
	strangerNational = "6505550199"
	strangerE164     = "+16505550199"
	offNational      = "6505550188"
)

func TestOnlyContactsCanCallARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, _ := managedOnEmulator(t, h, d)
	pkg := "io.github.helios57.familyguard"
	sdk, _ := strconv.Atoi(strings.TrimSpace(d.mustRun(30*time.Second, "shell", "getprop", "ro.build.version.sdk")))

	h.putFamilyNumbers(parent.Token, "+1 650 555 0123").expect(http.StatusOK)
	d.addContact(t, "Grosi", "+1 650 555 0111")
	t.Cleanup(func() {
		d.run(30*time.Second, "shell", "content", "delete", "--uri", "content://com.android.contacts/raw_contacts")
	})
	// An earlier run or a hand-placed call on this image would otherwise answer for this one.
	d.mustRun(30*time.Second, "shell", "content", "delete", "--uri", "content://call_log/calls")

	state := func() *deviceStateDTO {
		v := h.deviceView(parent.Token, device.ID)
		if v.State == nil {
			return &deviceStateDTO{}
		}
		return v.State
	}
	awaitScreening := func(what string, want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for {
			h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
			st := state()
			if st.CallScreening != nil && *st.CallScreening == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: after 2 min the phone reports call_screening=%v, want %v", what, st.CallScreening, want)
			}
			time.Sleep(5 * time.Second)
		}
	}
	// awaitApplied waits until the phone runs the policy a patch produced, so a call placed next is
	// decided by it and not by the one before.
	awaitApplied := func(what string, version int64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for state().PolicyVersion < version {
			if time.Now().After(deadline) {
				t.Fatalf("%s: after 2 min the phone runs policy %d, want %d", what, state().PolicyVersion, version)
			}
			h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
			time.Sleep(3 * time.Second)
		}
	}
	defaultAppsLocked := func() bool {
		users, _ := d.run(30*time.Second, "shell", "dumpsys", "user")
		return strings.Contains(users, "disallow_config_default_apps")
	}

	// The switch on, the role not yet held: the phone says so, and asks a person to grant it.
	on := h.patchPolicy(parent.Token, child.ID, map[string]any{"calls_contacts_only": true})
	awaitApplied("switch on", on.Version)
	awaitScreening("on, before the grant", false)
	if !d.notificationShown(t, "Set up call protection", "Anrufschutz einrichten") {
		t.Error("the switch is on and the role is not held, and the setup notification is not shown")
	}
	if defaultAppsLocked() {
		t.Error("disallow_config_default_apps is set before the role is held, which would block the grant dialog")
	}

	// The grant, as the system dialog makes it.
	d.mustRun(30*time.Second, "shell", "cmd", "role", "add-role-holder", "--user", "0", callScreeningRole, pkg)
	t.Cleanup(func() {
		d.run(30*time.Second, "shell", "cmd", "role", "remove-role-holder", "--user", "0", callScreeningRole, pkg)
	})
	if holders := d.roleHolders(t, callScreeningRole); !strings.Contains(holders, pkg) {
		t.Fatalf("after the grant `dumpsys role` lists holders %q, so its answer after the removal below would prove nothing", holders)
	}
	awaitScreening("after the grant", true)
	if d.notificationShown(t, "Set up call protection", "Anrufschutz einrichten") {
		t.Error("the role is held and the setup notification is still shown")
	}
	if sdk >= 34 && !defaultAppsLocked() {
		t.Errorf("API %d, role held, switch on: disallow_config_default_apps is not in effect", sdk)
	}

	// A contact rings. This is also the positive control for the two instruments: Telecom's call
	// list showing RINGING, and the call log the call lands in once it is cancelled.
	if got := d.incomingCall(t, contactNational); got != "rang" {
		t.Fatalf("a contact's call: %s, want it to ring", got)
	}
	if got := d.callLogTypes(t, "0111"); !slices.Equal(got, []string{callTypeMissed}) {
		t.Fatalf("the contact's cancelled call is logged as %v, want [%s] — the stranger's entry below would prove nothing", got, callTypeMissed)
	}

	// A stranger does not ring, and reaches the console. Android logs every call a screening app
	// rejects as BLOCKED whatever setSkipCallLog says (the platform's documentation of that call),
	// so the claim that can hold is: blocked, never missed — no "missed call" to call back.
	strangerNotes := d.notificationCount(t, "555-0199")
	if got := d.incomingCall(t, strangerNational); got != "rejected" {
		t.Fatalf("a stranger's call: %s, want it rejected", got)
	}
	if got := d.callLogTypes(t, "0199"); !slices.Equal(got, []string{callTypeBlocked}) {
		t.Errorf("the stranger's rejected call is logged as %v, want only [%s] (blocked)", got, callTypeBlocked)
	}
	deadline := time.Now().Add(90 * time.Second)
	for !rejectedHas(h.rejectedCalls(parent.Token, device.ID), strangerE164) {
		if time.Now().After(deadline) {
			t.Fatalf("90 s after the stranger's call the console lists %v, want %s", h.rejectedCalls(parent.Token, device.ID), strangerE164)
		}
		time.Sleep(3 * time.Second)
	}

	// A family number that is not a contact, dialled in national form, rings.
	familyNotes := d.notificationCount(t, "555-0123")
	if got := d.incomingCall(t, familyNational); got != "rang" {
		t.Errorf("the family number %s: %s, want it to ring", familyE164, got)
	}
	// The missed-call notification, counted before and after so that one left by an earlier run
	// cannot answer: the family number's unanswered call adds one (the positive control for the
	// instrument, which reads the dialer's own notification), the stranger's added none. Read
	// after the family call, so the dialer has had the stranger's call for longer than its own.
	if !d.awaitNotificationCount(t, "555-0123", familyNotes) {
		t.Error("the family number's unanswered call left no missed-call notification, so the stranger's count would prove nothing")
	} else if got := d.notificationCount(t, "555-0199"); got != strangerNotes {
		t.Errorf("the stranger's rejected call changed the missed-call notifications for its number: %d -> %d", strangerNotes, got)
	}

	// Negative control: the switch off, and a stranger rings — the rejection above came from the
	// switch, not from something else about this phone or this number.
	off := h.patchPolicy(parent.Token, child.ID, map[string]any{"calls_contacts_only": false})
	awaitApplied("switch off", off.Version)
	if got := d.incomingCall(t, offNational); got != "rang" {
		t.Errorf("switch off, a stranger's call: %s, want it to ring", got)
	}
	if sdk >= 34 && defaultAppsLocked() {
		t.Error("switch off and disallow_config_default_apps is still in effect")
	}

	// The role taken away while the switch is on: reported, and the console turns red.
	on = h.patchPolicy(parent.Token, child.ID, map[string]any{"calls_contacts_only": true})
	awaitApplied("switch on again", on.Version)
	d.mustRun(30*time.Second, "shell", "cmd", "role", "remove-role-holder", "--user", "0", callScreeningRole, pkg)
	if holders := d.roleHolders(t, callScreeningRole); strings.Contains(holders, pkg) {
		t.Fatalf("the shell could not take the role away (holders: %q), so the lost-role case was not measured", holders)
	}
	awaitScreening("role removed", false)
	if st := state(); st.CallScreeningHeldAt == nil {
		t.Error("the role was held and lost, and the console has no held_at — it would show amber, not red")
	}
	if !d.notificationShown(t, "Set up call protection", "Anrufschutz einrichten") {
		t.Error("the role was lost and the phone does not ask for it again")
	}
}

func rejectedHas(calls []rejectedCallDTO, number string) bool {
	for _, c := range calls {
		if c.Number == number {
			return true
		}
	}
	return false
}

// addContact inserts one contact with one phone number through the contacts provider, the way the
// Contacts app does.
func (d *androidDevice) addContact(t *testing.T, name, number string) {
	t.Helper()
	d.mustRun(30*time.Second, "shell", "content", "insert", "--uri", "content://com.android.contacts/raw_contacts",
		"--bind", "account_type:n:", "--bind", "account_name:n:")
	rows := d.mustRun(30*time.Second, "shell", "content", "query", "--uri", "content://com.android.contacts/raw_contacts", "--projection", "_id")
	id := ""
	for _, line := range strings.Split(rows, "\n") {
		if i := strings.Index(line, "_id="); i >= 0 {
			id = strings.TrimSpace(line[i+4:])
		}
	}
	if id == "" {
		t.Fatalf("no raw contact after the insert: %q", rows)
	}
	d.mustRun(30*time.Second, "shell", "content", "insert", "--uri", "content://com.android.contacts/data",
		"--bind", "raw_contact_id:i:"+id, "--bind", "mimetype:s:vnd.android.cursor.item/name", "--bind", "data1:s:"+name)
	d.mustRun(30*time.Second, "shell", "content", "insert", "--uri", "content://com.android.contacts/data",
		"--bind", "raw_contact_id:i:"+id, "--bind", "mimetype:s:vnd.android.cursor.item/phone_v2",
		"--bind", "data1:s:"+strings.ReplaceAll(number, " ", ""), "--bind", "data2:i:2")
}

// incomingCall has the emulator's modem ring the phone from number, and answers "rang" once Telecom
// lists the call as RINGING, or "rejected" when the call has left Telecom's list without ever
// ringing. Either way the call is cancelled and gone before it returns.
func (d *androidDevice) incomingCall(t *testing.T, number string) string {
	t.Helper()
	suffix := number[len(number)-2:]
	d.mustRun(30*time.Second, "emu", "gsm", "call", number)
	defer func() {
		d.run(30*time.Second, "emu", "gsm", "cancel", number)
		deadline := time.Now().Add(20 * time.Second)
		for d.telecomCallState(suffix) != "" && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
	}()
	seen := false
	start := time.Now()
	for time.Since(start) < 15*time.Second {
		switch st := d.telecomCallState(suffix); {
		case st == "RINGING":
			return "rang"
		case st != "":
			seen = true
		case seen || time.Since(start) > 8*time.Second:
			// Gone without ringing. 8 s is past Telecom's own 5 s screening timeout, after which an
			// unanswered screening rings the call anyway.
			return "rejected"
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "still undecided after 15 s (state " + d.telecomCallState(suffix) + ")"
}

// telecomCallState is the state Telecom's call list holds for the call whose number ends in suffix,
// or "" when it holds none.
func (d *androidDevice) telecomCallState(suffix string) string {
	dump, _ := d.run(30*time.Second, "shell", "dumpsys", "telecom")
	in := false
	for _, line := range strings.Split(dump, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "mCalls:") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if !strings.HasPrefix(trimmed, "[Call id=") {
			return ""
		}
		if strings.Contains(trimmed, "handle=tel:") && strings.Contains(trimmed, suffix+",") {
			if i := strings.Index(trimmed, "state="); i >= 0 {
				return strings.SplitN(trimmed[i+6:], ",", 2)[0]
			}
		}
	}
	return ""
}

// CallLog.Calls.TYPE values.
const (
	callTypeMissed  = "3"
	callTypeBlocked = "6"
)

// callLogTypes is the CallLog type of every call-log entry from a number ending in suffix, oldest
// first.
func (d *androidDevice) callLogTypes(t *testing.T, suffix string) []string {
	t.Helper()
	rows := d.mustRun(30*time.Second, "shell", "content", "query", "--uri", "content://call_log/calls",
		"--projection", "number:type", "--sort", "date")
	var types []string
	for _, line := range strings.Split(rows, "\n") {
		n, ty := strings.Index(line, "number="), strings.Index(line, ", type=")
		if n < 0 || ty < n || !strings.HasSuffix(line[n+7:ty], suffix) {
			continue
		}
		types = append(types, strings.TrimSpace(line[ty+7:]))
	}
	return types
}

// notificationCount is how often text occurs in the posted notifications of every app.
func (d *androidDevice) notificationCount(t *testing.T, text string) int {
	t.Helper()
	return strings.Count(d.mustRun(30*time.Second, "shell", "dumpsys", "notification", "--noredact"), text)
}

// awaitNotificationCount waits up to 10 s for text to occur more often than before.
func (d *androidDevice) awaitNotificationCount(t *testing.T, text string, before int) bool {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if d.notificationCount(t, text) > before {
			return true
		}
	}
	return false
}

// notificationShown is whether a notification of this app carrying one of the titles is posted.
func (d *androidDevice) notificationShown(t *testing.T, titles ...string) bool {
	t.Helper()
	dump := d.mustRun(30*time.Second, "shell", "dumpsys", "notification", "--noredact")
	for _, title := range titles {
		if strings.Contains(dump, title) {
			return true
		}
	}
	return false
}

// roleHolders is the holders line `dumpsys role` prints for role — `cmd role get-role-holders` does
// not exist before API 34 (exit 255 on API 33).
func (d *androidDevice) roleHolders(t *testing.T, role string) string {
	t.Helper()
	in := false
	for _, line := range strings.Split(d.mustRun(30*time.Second, "shell", "dumpsys", "role"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "name="+role:
			in = true
		case in && strings.HasPrefix(line, "holders="):
			return strings.TrimPrefix(line, "holders=")
		case in && line == "}":
			return ""
		}
	}
	return ""
}
