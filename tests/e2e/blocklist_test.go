package e2e

// The family-wide blocklist, end to end: FR-18.
//
// The unit vectors in backend/internal/policy already prove the precedence arithmetic, and they
// prove it on both engines. What they cannot reach is the half that made this feature worth
// building: that one list, written once, governs a child who did not exist when it was written, and
// that it survives the server being restarted. Both of those are properties of the database and the
// wiring, not of Compute, and both are the kind that a unit test with a hand-built Settings
// silently assumes.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type blocklistEntryDTO struct {
	PackageName string `json:"package_name"`
	Label       string `json:"label"`
	Reason      string `json:"reason"`
	Source      string `json:"source"`
	CreatedAt   string `json:"created_at"`
}

// The four Meta packages the curated set seeds. Named here rather than read from the server so that
// this suite asserts what migration 0005 was written to do, instead of agreeing with whatever it
// happens to contain.
var metaPreinstall = []string{
	"com.facebook.appmanager",
	"com.facebook.katana",
	"com.facebook.services",
	"com.facebook.system",
}

func (h *harness) blocklist(token string) []blocklistEntryDTO {
	h.t.Helper()
	var out struct {
		Packages []blocklistEntryDTO `json:"packages"`
	}
	h.call(http.MethodGet, "/family/blocked-packages", token, nil).expect(http.StatusOK).decode(&out)
	return out.Packages
}

func (h *harness) blockForFamily(token, pkg, label, reason string) blocklistEntryDTO {
	h.t.Helper()
	var out blocklistEntryDTO
	h.call(http.MethodPut, "/family/blocked-packages", token, map[string]any{
		"package_name": pkg, "label": label, "reason": reason,
	}).expect(http.StatusOK).decode(&out)
	return out
}

func names(entries []blocklistEntryDTO) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.PackageName)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestTheCuratedBlocklistIsSeededAndReachesAPhone is the whole feature in one path: a server that
// has never been configured already blocks the Meta preinstall, and a phone that reported none of
// those packages is told to hide all four.
//
// The "reported none of them" half is the one worth having. A phone whose inventory contains
// Facebook would pass an implementation that only ever blocks what it can see — and that
// implementation is wrong, because the installer stub puts the app back after the parent's decision
// was already made (FR-18.2).
func TestTheCuratedBlocklistIsSeededAndReachesAPhone(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	seeded := names(h.blocklist(f.parent.Token))
	for _, pkg := range metaPreinstall {
		if !contains(seeded, pkg) {
			t.Errorf("the curated set does not carry %s; the list is %v", pkg, seeded)
		}
	}
	for _, e := range h.blocklist(f.parent.Token) {
		if e.Source != "BUILTIN" {
			t.Errorf("%s was seeded with source %q, expected BUILTIN", e.PackageName, e.Source)
		}
		if e.Reason == "" {
			t.Errorf("%s carries no reason; a bare package name cannot be reviewed later", e.PackageName)
		}
	}

	// The phone's inventory deliberately excludes every package on the list.
	h.call(http.MethodPost, "/device/inventory", f.enroll.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": "com.example.game", "label": "Game"},
			{"package_name": oemDialer, "label": "Dialer", "system_app": true},
		},
	}).expect(http.StatusOK)

	state := h.devicePolicy(f.enroll.DeviceToken)
	for _, pkg := range metaPreinstall {
		if !contains(state.Desired.HiddenPackages, pkg) {
			t.Errorf("the device is not told to hide %s, though no phone reported it installed:\n  hidden: %v",
				pkg, state.Desired.HiddenPackages)
		}
		if !contains(state.Desired.SuspendedPackages, pkg) {
			t.Errorf("the device is not told to suspend %s:\n  suspended: %v",
				pkg, state.Desired.SuspendedPackages)
		}
	}

	// The offline half. A device that received only the computed output would reveal all four the
	// first time it recomputed in a tunnel, and nothing online would ever look wrong.
	if !contains(state.Input.Settings.FamilyBlockedPackages, "com.facebook.katana") {
		t.Errorf("the policy input carries no family blocklist, so the phone cannot recompute it offline: %+v",
			state.Input.Settings.FamilyBlockedPackages)
	}
}

// TestTheBlocklistCoversAChildAddedAfterIt is the reason the list is not a per-child rule.
//
// A parent who blocks something today has decided it for the household. If the next child enrolled
// starts without it, the parent has to remember to repeat a decision they already made — and they
// will find out they forgot from the child.
func TestTheBlocklistCoversAChildAddedAfterIt(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	h.blockForFamily(parent.Token, "com.example.timesink", "Timesink", "Nobody here needs this.")

	// Only now does the second child exist.
	later := h.newChild(parent.Token, "Younger")
	device := h.newDevice(parent.Token, later.ID, "Younger's phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})

	state := h.devicePolicy(enrolled.DeviceToken)
	if !contains(state.Desired.HiddenPackages, "com.example.timesink") {
		t.Fatalf("a child added after the entry was written is not covered by it: hidden=%v",
			state.Desired.HiddenPackages)
	}
}

// TestAChildAllowExemptsOnlyThatChild pins FR-18.3 where it is observable: two children, one
// exemption, two different answers.
//
// The second child is the control. Without it, an implementation that dropped the entry from the
// family list entirely — rather than exempting one child — would pass.
func TestAChildAllowExemptsOnlyThatChild(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	h.blockForFamily(parent.Token, "com.example.timesink", "Timesink", "Household default.")

	type phone struct {
		child string
		token string
	}
	var phones []phone
	for _, name := range []string{"Older", "Younger"} {
		child := h.newChild(parent.Token, name)
		device := h.newDevice(parent.Token, child.ID, name+"'s phone")
		_, enrollToken := h.provision(parent.Token, device.ID)
		enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})
		phones = append(phones, phone{child: child.ID, token: enrolled.DeviceToken})
	}

	h.call(http.MethodPut, "/children/"+phones[0].child+"/app-rules", parent.Token,
		map[string]any{"package_name": "com.example.timesink", "action": "ALLOW"}).
		expect(http.StatusOK)

	exempt := h.devicePolicy(phones[0].token)
	if contains(exempt.Desired.HiddenPackages, "com.example.timesink") {
		t.Errorf("the ALLOW rule did not exempt the child it was written for: hidden=%v",
			exempt.Desired.HiddenPackages)
	}
	other := h.devicePolicy(phones[1].token)
	if !contains(other.Desired.HiddenPackages, "com.example.timesink") {
		t.Errorf("one child's exemption unblocked the package for the whole family: hidden=%v",
			other.Desired.HiddenPackages)
	}
}

// TestTheCriticalWhitelistOutranksTheBlocklist is FR-18.4 against the failure it exists to prevent.
//
// The device reports its own dialer as critical, the parent blocks that exact package, and the
// answer must be that it is neither hidden nor suspended. The blocklist is the one surface where a
// parent can name any package at all, so it is the one most able to reach a package that must never
// be touched.
func TestTheCriticalWhitelistOutranksTheBlocklist(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	// A negative control first: something that is NOT critical, blocked in the same call, must be
	// hidden. Without it, an implementation that ignored the blocklist entirely would pass this
	// test by doing nothing at all.
	h.blockForFamily(f.parent.Token, "com.example.timesink", "Timesink", "The control.")
	h.blockForFamily(f.parent.Token, oemDialer, "Dialer", "Deliberately wrong, to see it refused.")

	state := h.devicePolicy(f.enroll.DeviceToken)
	if !contains(state.Desired.HiddenPackages, "com.example.timesink") {
		t.Fatalf("the control package is not hidden, so this test proves nothing about the dialer: hidden=%v",
			state.Desired.HiddenPackages)
	}
	if contains(state.Desired.HiddenPackages, oemDialer) {
		t.Errorf("the device's own dialer was hidden by the blocklist: hidden=%v", state.Desired.HiddenPackages)
	}
	if contains(state.Desired.SuspendedPackages, oemDialer) {
		t.Errorf("the device's own dialer was suspended by the blocklist: suspended=%v",
			state.Desired.SuspendedPackages)
	}
}

// TestDeletingACuratedEntryIsPermanent is FR-18.5, and the restart is the entire test.
//
// Migrations run on every start. A curated set re-applied on each start would put a deleted entry
// back with no error, no audit line and nothing for a parent to read — this project's recurring
// defect, a decision that reads as taken and was undone. Seeding once is what makes the deletion
// stick, and only a restart can tell the two implementations apart.
func TestDeletingACuratedEntryIsPermanent(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	before := names(h.blocklist(parent.Token))
	if !contains(before, "com.facebook.katana") {
		t.Fatalf("the curated entry is not there to delete: %v", before)
	}
	h.call(http.MethodDelete, "/family/blocked-packages?package_name=com.facebook.katana",
		parent.Token, nil).expect(http.StatusNoContent)

	h.restart()
	parent = h.signIn(primaryParent)

	after := names(h.blocklist(parent.Token))
	if contains(after, "com.facebook.katana") {
		t.Fatalf("a restart put a deleted curated entry back: %v", after)
	}
	// The rest of the curated set must still be there — a "deletion" that emptied the table would
	// otherwise pass.
	if !contains(after, "com.facebook.system") {
		t.Fatalf("the restart lost the rest of the curated set: %v", after)
	}
}

// TestABlocklistChangeBumpsEveryChildsPolicyVersion is how the change reaches a phone that is not
// listening on the push stream.
//
// A device fetches new state when the version it holds is behind the server's. An entry written
// without a bump is a row that is correct in the database and enforced nowhere, and the console
// would show it as applied.
func TestABlocklistChangeBumpsEveryChildsPolicyVersion(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	first := h.newChild(parent.Token, "Older")
	second := h.newChild(parent.Token, "Younger")

	var beforeFirst, beforeSecond policyDTO
	h.call(http.MethodGet, "/children/"+first.ID+"/policy", parent.Token, nil).
		expect(http.StatusOK).decode(&beforeFirst)
	h.call(http.MethodGet, "/children/"+second.ID+"/policy", parent.Token, nil).
		expect(http.StatusOK).decode(&beforeSecond)

	h.blockForFamily(parent.Token, "com.example.timesink", "Timesink", "")

	var afterFirst, afterSecond policyDTO
	h.call(http.MethodGet, "/children/"+first.ID+"/policy", parent.Token, nil).
		expect(http.StatusOK).decode(&afterFirst)
	h.call(http.MethodGet, "/children/"+second.ID+"/policy", parent.Token, nil).
		expect(http.StatusOK).decode(&afterSecond)

	if afterFirst.Version <= beforeFirst.Version {
		t.Errorf("the first child's policy version stayed at %d", afterFirst.Version)
	}
	if afterSecond.Version <= beforeSecond.Version {
		t.Errorf("the second child's policy version stayed at %d — one write must move every child",
			afterSecond.Version)
	}
}

// TestTheBlocklistIsReachableByAPIKeyAndGuardedByRole covers the two access rules together, because
// they are the same question asked from two directions: who may read this, and who may change it.
func TestTheBlocklistIsReachableByAPIKeyAndGuardedByRole(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)

	var created struct {
		Token string `json:"token"`
	}
	h.call(http.MethodPost, "/api-keys", primary.Token, map[string]any{"name": "MCP server"}).
		expect(http.StatusCreated).decode(&created)

	// FR-17: a key is a parent. Everything the console can do here, a script can do.
	h.blockForFamily(created.Token, "com.example.byscript", "By script", "Written with a key.")
	if !contains(names(h.blocklist(created.Token)), "com.example.byscript") {
		t.Error("a key wrote an entry it cannot then read back")
	}

	guardianIdentity := identity{
		Email: "guardian@family.test", Subject: "google-guardian", Name: "Guardian", Verified: true,
	}
	h.call(http.MethodPost, "/parents", primary.Token, map[string]any{
		"email": guardianIdentity.Email, "role": "GUARDIAN",
	}).expect(http.StatusCreated)
	guardian := h.signIn(guardianIdentity)

	// A guardian sees the list — it explains why an app is missing from a phone they are watching.
	if len(h.blocklist(guardian.Token)) == 0 {
		t.Error("a guardian cannot read the blocklist, so a hidden app has no explanation")
	}
	// They do not change it. One entry moves every phone in the family.
	h.call(http.MethodPut, "/family/blocked-packages", guardian.Token,
		map[string]any{"package_name": "com.example.nope"}).expect(http.StatusForbidden)
	h.call(http.MethodDelete, "/family/blocked-packages?package_name=com.facebook.katana",
		guardian.Token, nil).expect(http.StatusForbidden)

	// The refusal must be about the role and not about the request, so the same call from an admin
	// has to work. Without this, a typo in the path would produce the same two 403s.
	h.blockForFamily(primary.Token, "com.example.nope", "", "")
}

// TestTheBlocklistRefusesWhatCanNeverMatchAnApp keeps the list honest.
//
// An entry that is not shaped like a package name can never match an installed application, so it
// is a row that looks like protection and is not — and the console shows it exactly like a working
// one.
func TestTheBlocklistRefusesWhatCanNeverMatchAnApp(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	for _, bad := range []string{"", "facebook", "com..facebook", "https://facebook.com", "com.9lives"} {
		h.call(http.MethodPut, "/family/blocked-packages", parent.Token,
			map[string]any{"package_name": bad}).
			expectError(http.StatusBadRequest, "invalid_input")
	}
	// Calibration: the same call with a well-formed name is accepted, so the refusals above are
	// about the value and not about the route.
	h.blockForFamily(parent.Token, "com.example.fine", "", "")

	// A reason longer than the column allows is trimmed rather than refused: losing the tail of a
	// sentence is a better outcome than a parent's decision failing to land.
	long := strings.Repeat("ä", 400)
	entry := h.blockForFamily(parent.Token, "com.example.verbose", "", long)
	if len(entry.Reason) > 200 {
		t.Errorf("the reason came back %d bytes long", len(entry.Reason))
	}
	if !strings.HasPrefix(long, entry.Reason) {
		t.Error("the trimmed reason is not a prefix of what was sent")
	}
	if strings.ContainsRune(entry.Reason, '\uFFFD') {
		t.Error("the reason was cut mid-rune; a label typed in German or with an emoji finds this")
	}
}

// TestTheBlocklistIsAudited — a family-wide change with no trail is the one a parent cannot explain
// to the other parent.
func TestTheBlocklistIsAudited(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	h.blockForFamily(parent.Token, "com.example.timesink", "Timesink", "")
	h.call(http.MethodDelete, "/family/blocked-packages?package_name=com.example.timesink",
		parent.Token, nil).expect(http.StatusNoContent)

	var audit struct {
		Entries []struct {
			Action string         `json:"action"`
			Detail map[string]any `json:"detail"`
		} `json:"entries"`
	}
	h.call(http.MethodGet, "/audit?limit=50", parent.Token, nil).expect(http.StatusOK).decode(&audit)

	want := map[string]bool{"FAMILY_BLOCKLIST_SET": false, "FAMILY_BLOCKLIST_CLEARED": false}
	for _, e := range audit.Entries {
		if _, ok := want[e.Action]; ok && fmt.Sprint(e.Detail["package"]) == "com.example.timesink" {
			want[e.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Errorf("no audit entry names %s for the package that was changed", action)
		}
	}
}

// storeDefaultBlocked mirrors store.DefaultBlockedPackages for the suites that need to say "this
// package is on the curated list" without importing the server.
//
// Written out again like every other shape in this suite: adding a package to the curated set has
// to be a deliberate edit here too, not something a shared import absorbs silently.
func suspendedBeyondTheBlocklist(d desiredStateDTO) []string {
	var extra []string
	for _, pkg := range d.SuspendedPackages {
		if !storeDefaultBlocked(pkg) {
			extra = append(extra, pkg)
		}
	}
	return extra
}

func storeDefaultBlocked(pkg string) bool {
	return contains([]string{
		"com.facebook.appmanager",
		"com.facebook.katana",
		"com.facebook.services",
		"com.facebook.system",
		"com.microsoft.appmanager",
		"com.microsoft.skydrive",
		"com.mygalaxy.service",
	}, pkg)
}

// FR-18.6: the console reports what the PHONE says, not what the rule asked for.
//
// The rest of this file proves the list reaches the device. This proves the answer comes back —
// which is the half a parent asking "is the bloatware actually gone?" is asking about. Without it
// the console can only restate the policy, and a policy that was delivered to a phone that refused
// to apply it looks exactly like one that worked.
//
// It also pins the property that makes the round trip possible at all: a package the device has
// hidden must STAY in the inventory. Hiding clears the installed-for-this-user flag, so a DPC that
// enumerated only installed packages would drop the blocked app from its next report — and the
// console would then answer "is it gone?" with "not installed here", which is both wrong and
// reassuring.
func TestThePhoneConfirmsWhatItHasHidden(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	type reported struct {
		PackageName string `json:"package_name"`
		Label       string `json:"label"`
		SystemApp   bool   `json:"system_app"`
		Hidden      bool   `json:"hidden"`
		Suspended   bool   `json:"suspended"`
	}
	// include_system=1, because the packages this feature exists for ARE system apps. A Samsung
	// ships Facebook as a preinstall — that is precisely why it cannot be uninstalled and has to be
	// hidden instead — and the endpoint filters system apps unless asked. The console omitted this
	// flag, so its blocklist card answered "is the bloatware gone?" with "not installed on any
	// phone here" about a package sitting on the phone. Asserted below in both directions.
	inventory := func(includeSystem bool) map[string]reported {
		t.Helper()
		path := "/devices/" + f.device.ID + "/apps"
		if includeSystem {
			path += "?include_system=1"
		}
		var out struct {
			Apps []reported `json:"apps"`
		}
		h.call(http.MethodGet, path, f.parent.Token, nil).expect(http.StatusOK).decode(&out)
		byPkg := map[string]reported{}
		for _, a := range out.Apps {
			byPkg[a.PackageName] = a
		}
		return byPkg
	}

	// First report: the phone has Facebook and it is running. This is the state a phone is in
	// between enrolling and its first policy apply, and the console must not claim otherwise.
	h.call(http.MethodPost, "/device/inventory", f.enroll.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": "com.facebook.katana", "label": "Facebook", "system_app": true},
			{"package_name": pkgGame, "label": "Game"},
		},
	}).expect(http.StatusOK)

	before := inventory(true)
	if before["com.facebook.katana"].Hidden {
		t.Fatal("a phone that reported the app as running is listed as having it hidden")
	}

	// Second report: the phone has applied the block. The package is still reported — that is the
	// property under test — and now carries the flag.
	h.call(http.MethodPost, "/device/inventory", f.enroll.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": "com.facebook.katana", "label": "Facebook", "system_app": true, "hidden": true, "suspended": false},
			{"package_name": pkgGame, "label": "Game", "suspended": true},
		},
	}).expect(http.StatusOK)

	// The default listing does not carry it, and that is the trap this test exists to pin: a caller
	// that forgets the flag gets a 200 and a shorter list, never an error. The console's blocklist
	// read exactly this list.
	if _, leaked := inventory(false)["com.facebook.katana"]; leaked {
		t.Fatal("the default listing now returns system apps; the console's include_system=1 is " +
			"no longer what makes the blocklist see a preinstall, and this test no longer binds to it")
	}

	after := inventory(true)
	fb, ok := after["com.facebook.katana"]
	if !ok {
		t.Fatal("the hidden package dropped out of the inventory, so the console shows the blocked app as not installed")
	}
	if !fb.Hidden {
		t.Fatalf("the device reported the package hidden and the console does not: %+v", fb)
	}
	if !after[pkgGame].Suspended {
		t.Fatalf("the device reported %s suspended and the console does not: %+v", pkgGame, after[pkgGame])
	}

	// And it comes back down again, because a parent who unblocks an app has to be able to see that
	// it worked. A flag that only ever ratchets on would report every app ever blocked as still
	// hidden forever.
	h.call(http.MethodPost, "/device/inventory", f.enroll.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": "com.facebook.katana", "label": "Facebook", "system_app": true},
			{"package_name": pkgGame, "label": "Game"},
		},
	}).expect(http.StatusOK)

	revealed := inventory(true)
	if revealed["com.facebook.katana"].Hidden || revealed[pkgGame].Suspended {
		t.Fatalf("the flags did not clear when the phone stopped reporting them: %+v", revealed)
	}
}
