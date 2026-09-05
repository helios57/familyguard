package e2e

// FR-14, the half that was owed.
//
// `internal/store/audit.go` writes the rows and every handler calls it, and until this file existed
// nothing asserted that a given action produces a given row. That gap has a specific shape: the
// audit log is write-only in normal operation — nobody reads it until something has gone wrong —
// so a handler that stopped calling `auditParent` would keep working, keep returning its usual
// status, and keep the console looking exactly the same. The absence would surface on the one day
// it mattered, as a hole in the record of who did what to a child's phone. `audit()` deliberately
// never fails the request it describes, which is the right trade-off and also the reason the only
// thing standing between a missing call and a silent hole is a test.
//
// `TestRecoveryAndAudit` in journeys_test.go already checks that ten action NAMES appear. This is
// the other three quarters of the requirement: all twenty-one actions, each bound to the actor and
// the object it was taken against, and a ratchet that notices a twenty-second.
//
// Three assertions, and none of them is sufficient alone:
//
//   - each action, driven over real HTTP, produces a row naming the actor, the action, the target
//     type and the target *id*. Target id is the half that catches a copy-paste between handlers;
//     an action name alone would still match if `DEVICE_RENAMED` were logged against the child.
//   - the rows that identify *which* change was made carry it in their detail. An action name says
//     a rename happened; only the detail says what it was renamed to.
//   - the set of actions this file covers equals the set the handlers can emit, read out of the
//     handler source. Without it a NEW audited action arrives uncovered and this suite still
//     passes — the test would be pinned to the actions that existed the day it was written, which
//     is the same "guard that stopped growing with the code" this project keeps finding elsewhere.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// readAudit pulls the log back over the API the console uses. The limit is raised past the default
// 100 because this test deliberately performs more actions than a default page holds, and a
// truncated read would report a missing row for an action that was audited perfectly.
func (h *harness) readAudit(token string) []auditEntryDTO {
	h.t.Helper()
	var body struct {
		Entries []auditEntryDTO `json:"entries"`
	}
	h.call(http.MethodGet, "/audit?limit=500", token, nil).expect(http.StatusOK).decode(&body)
	return body.Entries
}

// want is one expected row. TargetID is compared exactly, so a handler auditing the right action
// against the wrong object fails here rather than passing on the name alone.
type want struct {
	Action     string
	ActorType  string
	ActorID    string
	TargetType string
	TargetID   string
}

func (w want) String() string {
	return fmt.Sprintf("%s by %s/%s on %s/%s", w.Action, w.ActorType, w.ActorID, w.TargetType, w.TargetID)
}

func TestEveryAuditedActionIsWritten(t *testing.T) {
	// With an APK directory, so the catalog's actions (FR-16) are drivable here rather than being
	// permanently listed as uncovered — an exclusion list is how a ratchet stops ratcheting.
	h, apkDir := catalogHarness(t)

	// PARENT_SIGNED_IN, before anything else can have written a row.
	parent := h.signIn(primaryParent)
	expected := []want{{
		Action: "PARENT_SIGNED_IN", ActorType: "PARENT", ActorID: parent.Parent.ID,
		TargetType: "parent", TargetID: parent.Parent.ID,
	}}
	byParent := func(action, targetType, targetID string) {
		expected = append(expected, want{
			Action: action, ActorType: "PARENT", ActorID: parent.Parent.ID,
			TargetType: targetType, TargetID: targetID,
		})
	}
	byDevice := func(action, deviceID string) {
		expected = append(expected, want{
			Action: action, ActorType: "DEVICE", ActorID: deviceID,
			TargetType: "device", TargetID: deviceID,
		})
	}

	// ---- the family: parents ----
	//
	// Added and then removed, because PARENT_REMOVED is the most consequential row in this log —
	// it is how someone loses access to a child's phone — and it is written by the handler least
	// likely to be exercised by any other test.
	var added parentDTO
	h.call(http.MethodPost, "/parents", parent.Token,
		map[string]any{"email": "third@family.test", "role": "GUARDIAN"}).
		expect(http.StatusCreated).decode(&added)
	byParent("PARENT_ADDED", "parent", added.ID)

	h.call(http.MethodDelete, "/parents/"+added.ID, parent.Token, nil).expect(http.StatusNoContent)
	byParent("PARENT_REMOVED", "parent", added.ID)

	// ---- a child, its policy and its rules ----
	child := h.newChild(parent.Token, "Mira")
	byParent("CHILD_ADDED", "child", child.ID)

	h.call(http.MethodPatch, "/children/"+child.ID, parent.Token,
		map[string]any{"name": "Mira R."}).expect(http.StatusOK)
	byParent("CHILD_UPDATED", "child", child.ID)

	h.patchPolicy(parent.Token, child.ID, map[string]any{"daily_limit_minutes": 90})
	byParent("POLICY_UPDATED", "child", child.ID)

	h.call(http.MethodPut, "/children/"+child.ID+"/app-rules", parent.Token,
		map[string]any{"package_name": pkgGame, "action": "BLOCK"}).expect(http.StatusOK)
	byParent("APP_RULE_SET", "child", child.ID)

	// The two removals take their subject in the query string rather than a body — a DELETE with a
	// body is legal and widely dropped by proxies, so the handlers read `?package_name=`/`?domain=`.
	h.call(http.MethodDelete, "/children/"+child.ID+"/app-rules?package_name="+pkgGame,
		parent.Token, nil).expect(http.StatusNoContent)
	byParent("APP_RULE_CLEARED", "child", child.ID)

	h.call(http.MethodPost, "/children/"+child.ID+"/blocked-domains", parent.Token,
		map[string]any{"domain": "example.invalid"}).expect(http.StatusCreated)
	byParent("DOMAIN_BLOCKED", "child", child.ID)

	h.call(http.MethodDelete, "/children/"+child.ID+"/blocked-domains?domain=example.invalid",
		parent.Token, nil).expect(http.StatusNoContent)
	byParent("DOMAIN_UNBLOCKED", "child", child.ID)

	// ---- a device, through its whole life ----
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")
	byParent("DEVICE_ADDED", "device", device.ID)

	h.call(http.MethodPatch, "/devices/"+device.ID, parent.Token,
		map[string]any{"name": "Mira's Pixel"}).expect(http.StatusOK)
	byParent("DEVICE_RENAMED", "device", device.ID)

	_, enrollToken := h.provision(parent.Token, device.ID)
	byParent("ENROLLMENT_ISSUED", "device", device.ID)

	// Enrolment is audited with the DEVICE as the actor, not the parent who issued the token. The
	// row records what the phone did, and "this credential was redeemed, by something claiming to
	// be a Pixel 7a" is a fact none of the parent's own rows can show.
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})
	byDevice("DEVICE_ENROLLED", device.ID)

	// After enrolment, not before: the recovery code is generated by the phone's first handshake, so
	// a console asking for it earlier gets an honest 404 rather than a code no device would accept.
	h.call(http.MethodGet, "/devices/"+device.ID+"/recovery-code", parent.Token, nil).
		expect(http.StatusOK)
	byParent("RECOVERY_CODE_VIEWED", "device", device.ID)

	// ---- commands: issued by a parent, acknowledged by the phone ----
	//
	// Two of them, because the device-side action name is *computed* — `"COMMAND_"+cmd.State` — so
	// one command exercises one branch of it. A successful ack writes COMMAND_ACKED and a failed
	// one writes COMMAND_FAILED, and the failing branch is the one that matters: a command the
	// phone could not carry out is exactly what a parent staring at a still-unlocked phone needs
	// the log to say.
	okCmd := h.issueCommand(parent.Token, device.ID, "LOCK_NOW", nil)
	failCmd := h.issueCommand(parent.Token, device.ID, "UNLOCK_DEVICE", nil)
	byParent("COMMAND_ISSUED", "device", device.ID)

	// The phone fetches before it acks, the way the real client does. Fetching is what moves a
	// command to DELIVERED, and acking accepts QUEUED or DELIVERED — so skipping this step would
	// still pass, and would stop testing the path any device actually takes.
	var fetched struct {
		Commands []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"commands"`
	}
	h.call(http.MethodGet, "/device/commands", enrolled.DeviceToken, nil).
		expect(http.StatusOK).decode(&fetched)
	if len(fetched.Commands) != 2 {
		t.Fatalf("the device fetched %d commands, expected the 2 that were queued: %+v",
			len(fetched.Commands), fetched.Commands)
	}

	h.call(http.MethodPost, "/device/commands/"+okCmd.ID+"/ack", enrolled.DeviceToken,
		map[string]any{"ok": true}).expect(http.StatusOK)
	byDevice("COMMAND_ACKED", device.ID)

	h.call(http.MethodPost, "/device/commands/"+failCmd.ID+"/ack", enrolled.DeviceToken,
		map[string]any{"ok": false, "error": "the device owner app is not admin"}).
		expect(http.StatusOK)
	byDevice("COMMAND_FAILED", device.ID)

	// ---- recovery, used from the phone ----
	h.call(http.MethodPost, "/device/recovery-event", enrolled.DeviceToken,
		map[string]any{"succeeded": true}).expect(http.StatusOK)
	byDevice("RECOVERY_CODE_USED", device.ID)

	// ---- the application catalog and API keys (FR-16, FR-17) ----
	//
	// Every one of these changes what a child's phone will install or who can reach this family's
	// data, so each has to leave a row. APP_REGISTERED in particular is the only record of which
	// bytes entered the catalog and which key signed them.
	var registered appDTO
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").
		expect(http.StatusCreated).decode(&registered)
	byParent("APP_REGISTERED", "app", registered.ID)

	// A scan with the directory already holding that one file: it registers nothing new, and must
	// still say it ran. "Nothing to do" and "never ran" are the two states an operator has to be
	// able to tell apart.
	h.call(http.MethodPost, "/apps/scan", parent.Token, nil).expect(http.StatusOK)
	byParent("APP_DIRECTORY_SCANNED", "app", "")

	h.call(http.MethodPut, "/children/"+child.ID+"/managed-apps/"+fixturePackage, parent.Token, nil).
		expect(http.StatusNoContent)
	byParent("APP_DECLARED", "child", child.ID)

	h.call(http.MethodDelete, "/children/"+child.ID+"/managed-apps/"+fixturePackage, parent.Token, nil).
		expect(http.StatusNoContent)
	byParent("APP_WITHDRAWN", "child", child.ID)

	h.call(http.MethodDelete, "/apps/"+registered.ID, parent.Token, nil).expect(http.StatusNoContent)
	byParent("APP_DELETED", "app", registered.ID)

	var key apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "audited"}).
		expect(http.StatusCreated).decode(&key)
	byParent("API_KEY_CREATED", "api_key", key.ID)

	h.call(http.MethodPost, "/api-keys/"+key.ID+"/revoke", parent.Token, nil).expect(http.StatusOK)
	byParent("API_KEY_REVOKED", "api_key", key.ID)

	h.call(http.MethodDelete, "/api-keys/"+key.ID, parent.Token, nil).expect(http.StatusNoContent)
	byParent("API_KEY_DELETED", "api_key", key.ID)

	// The directory is the server's, and this test used it: nothing here should have left the
	// deleted application's bytes behind.
	if entries, err := os.ReadDir(apkDir); err != nil {
		t.Errorf("read the apk directory: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("the apk directory still holds %d file(s) after the app was deleted", len(entries))
	}

	// ---- and the removals, last ----
	h.call(http.MethodDelete, "/devices/"+device.ID, parent.Token, nil).expect(http.StatusNoContent)
	byParent("DEVICE_REMOVED", "device", device.ID)

	h.call(http.MethodDelete, "/children/"+child.ID, parent.Token, nil).expect(http.StatusNoContent)
	byParent("CHILD_REMOVED", "child", child.ID)

	// ---- what the log says ----
	entries := h.readAudit(parent.Token)
	if len(entries) == 0 {
		t.Fatal("the audit log is empty after a journey that performed every audited action")
	}

	have := map[want]bool{}
	for _, e := range entries {
		have[want{
			Action: e.Action, ActorType: e.ActorType, ActorID: e.ActorID,
			TargetType: e.TargetType, TargetID: e.TargetID,
		}] = true
		// Every row is attributable to the request that caused it. This is what joins a row to a
		// server log line, and `audit()` sets it for every caller — so a row without one means
		// something wrote to this table by a path that bypassed the helper.
		if id, _ := e.Detail["request_id"].(string); id == "" {
			t.Errorf("audit row %d (%s) carries no request_id: %+v", e.ID, e.Action, e.Detail)
		}
	}
	for _, w := range expected {
		if !have[w] {
			t.Errorf("no audit row for %s", w)
		}
	}

	// The details that identify WHICH change was made. A handler that logged the right action with
	// an empty detail would pass every assertion above and still leave the parent unable to tell
	// what was blocked.
	for _, tc := range []struct {
		action string
		key    string
		value  any
	}{
		{"PARENT_ADDED", "email", "third@family.test"},
		{"CHILD_ADDED", "name", "Mira"},
		{"CHILD_UPDATED", "name", "Mira R."},
		{"DEVICE_RENAMED", "name", "Mira's Pixel"},
		{"APP_RULE_SET", "package", pkgGame},
		{"DOMAIN_BLOCKED", "domain", "example.invalid"},
		{"COMMAND_ISSUED", "type", "LOCK_NOW"},
		{"COMMAND_FAILED", "error", "the device owner app is not admin"},
		{"DEVICE_ENROLLED", "model", "Pixel 7a"},
	} {
		found := false
		for _, e := range entries {
			if e.Action == tc.action && e.Detail[tc.key] == tc.value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no %s row whose detail[%q] is %v", tc.action, tc.key, tc.value)
		}
	}

	// The completeness half: every action the handlers can emit is one this test drove.
	literals, dynamic := auditActionsInHandlers(t)
	covered := map[string]bool{}
	for _, w := range expected {
		covered[w.Action] = true
	}
	var uncovered []string
	for _, a := range literals {
		if !covered[a] {
			uncovered = append(uncovered, a)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("these audited actions exist in the handlers and are not driven by this test: %s",
			strings.Join(uncovered, ", "))
	}

	// One call site computes its action name instead of writing it: `"COMMAND_"+cmd.State` in
	// ackCommand. A scan cannot resolve that to the set of names it can produce — the reachable
	// states are decided in the store, not at the call site — so this test drives both of them by
	// hand (COMMAND_ACKED, COMMAND_FAILED) and pins the *count* of such sites instead.
	//
	// Pinning the count rather than expanding the prefix is deliberate. Expanding it against the
	// five command states would demand rows for COMMAND_QUEUED, COMMAND_DELIVERED and
	// COMMAND_EXPIRED, which that call site can never write — a permanent red, and a permanently
	// red assertion gets muted within a week. A second computed site failing here means a human has
	// to decide what it can emit, which is the correct amount of friction.
	if dynamic != 1 {
		t.Errorf("the handlers now have %d audit call sites with a computed action name, not the 1 "+
			"this test knows how to cover (%s). Extend this test to drive the new one's reachable "+
			"names before changing this number", dynamic, `"COMMAND_"+cmd.State`)
	}
}

// auditActionsInHandlers reads the action names out of the handler source, returning the literals
// and a count of the call sites whose action is computed rather than written.
//
// Source-scanning rather than a registry, because a registry is a second place to forget: a handler
// that calls `auditParent` with a brand-new string is exactly the change this must notice, and that
// string exists nowhere but the call site. Three literal call shapes are matched — `s.audit(c,
// actorType, actorID, "ACTION"`, `s.auditParent(c, "ACTION"` and `s.bumpAndNotify(c, childID,
// "ACTION"`, the last being a wrapper around the second.
//
// It fails rather than returns nothing when the source cannot be read or the patterns stop
// matching: a scan that found no files would report "every action is covered", which is precisely
// the control-that-evaluated-nothing this file exists to prevent.
func auditActionsInHandlers(t *testing.T) (literals []string, dynamic int) {
	t.Helper()

	dir := filepath.Join("..", "..", "backend", "internal", "httpapi")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("could not read the handler source at %s (%d files, err %v): this check "+
			"cannot report completeness against nothing", dir, len(files), err)
	}

	// Two details in these patterns are load-bearing, and the second was found by this test failing:
	//
	//   - `[^,\n]` rather than `[^,]`. A negated class matches newlines, so the looser spelling lets
	//     one call's arguments run into the next line's string and attribute an action to the wrong
	//     call. Actions are SCREAMING_SNAKE by convention, which is what keeps these from matching
	//     the target-type strings ("device", "child") sitting beside them.
	//   - the captured name may not END in an underscore. `"COMMAND_"+cmd.State` otherwise reads as
	//     a literal action named `COMMAND_`, and the completeness check demands a row for a name no
	//     handler can ever write — a permanent, unfixable red. Requiring the last character to be
	//     alphanumeric is what separates a whole action name from the prefix of a computed one, and
	//     the dynamic pattern below picks that call site up instead.
	literalPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\bs\.audit\(c,[^,\n]+,[^,\n]+,\s*"([A-Z](?:[A-Z0-9_]*[A-Z0-9])?)"`),
		regexp.MustCompile(`\bs\.auditParent\(c,\s*"([A-Z](?:[A-Z0-9_]*[A-Z0-9])?)"`),
		regexp.MustCompile(`\bs\.bumpAndNotify\(c,\s*[^,\n]+,\s*"([A-Z](?:[A-Z0-9_]*[A-Z0-9])?)"`),
	}
	// An action assembled from a prefix and a variable, e.g. `"COMMAND_"+cmd.State`.
	dynamicPattern := regexp.MustCompile(`"[A-Z][A-Z0-9_]*_"\s*\+`)

	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("could not read %s: %v", f, err)
		}
		text := string(src)
		for _, re := range literalPatterns {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				seen[m[1]] = true
			}
		}
		dynamic += len(dynamicPattern.FindAllString(text, -1))
	}

	// The two helpers themselves pass their `action` parameter through, so a scan that matched
	// nothing means the call shapes changed, not that auditing was removed. Either way this check
	// has stopped measuring and must say so rather than report a clean sweep.
	if len(seen) == 0 {
		t.Fatalf("found no audited actions in %d handler files: the patterns no longer match the "+
			"call sites, so this check is measuring nothing", len(files))
	}

	literals = make([]string, 0, len(seen))
	for a := range seen {
		literals = append(literals, a)
	}
	sort.Strings(literals)
	return literals, dynamic
}
