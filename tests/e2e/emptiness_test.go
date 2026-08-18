package e2e

// NFR-5, the half that was owed: "No seeded demo families, children or devices in any deployable
// build. An empty system shows as empty."
//
// Until this file existed the traceability table pointed at `TestDeviceLifecycleJourney` and called
// it evidence-but-not-the-assertion, which was honest: that test starts from an empty database and
// every number it reads is one it caused, so it would keep passing on a build that also shipped a
// demo family — it never looks at anything it did not create.
//
// The requirement has two halves and they fail in different ways.
//
// The first is *structural*: nothing in the deployable artifact plants rows. That is a claim about
// the build, not about a running server, and the only way a running server could catch it is by
// noticing rows it did not cause — which is what section 2 does — so both are here.
//
// The second is subtler and is the one this project keeps re-learning: **an unknown must not be
// reported as a measurement.** A device that has enrolled and never sent telemetry has no battery
// level. Reporting `battery_level: 0` would be a fabricated reading, and a parent looking at a
// console that says "0%, offline" has been told something false about their child's phone.
// `getDevice` already says so in a comment — "battery 0%, offline is a measurement, and we have not
// taken it" — and a comment is not a control. The same shape as "zero errors in a window with zero
// traffic is not evidence", one layer up.
//
// A third assertion falls out of the second and is worth stating separately: an empty collection
// must arrive as `[]`, never as `null`. This is not pedantry about JSON. `for (const c of null)`
// throws in the browser and `for (const c of [])` renders the empty state, so a store function that
// returns a nil slice turns "you have no children yet" into a console that renders nothing at all
// and reports no error. Go hides this from a decoded test: `json.Unmarshal` maps BOTH `[]` and
// `null` onto a nil slice, so every assertion in this file that cares about the difference reads the
// bytes off the wire instead of a decoded struct.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestAFreshSystemShowsAsEmpty(t *testing.T) {
	// ---- 1. the build itself plants nothing ---------------------------------
	//
	// Before a server is even started, because a seed that ships in the schema would be present in
	// every deployment of this build and no amount of black-box probing of one instance proves its
	// absence from the artifact.
	assertMigrationsSeedNothing(t)

	h := newHarness(t)

	// ---- 2. a server that has only ever been started reports only what it was configured with --
	//
	// Bootstrap writes exactly two kinds of row: the family, named from FAMILY_NAME, and one parent
	// per address in BOOTSTRAP_PARENT_EMAILS. Both are configuration a deployer supplied, not data
	// this build invented, and the assertion is that there is nothing else.
	parent := h.signIn(primaryParent)

	var fam familyDTO
	h.call(http.MethodGet, "/family", parent.Token, nil).expect(http.StatusOK).decode(&fam)
	if fam.Name != "E2E Family" {
		t.Errorf("the family is named %q; this harness configured FAMILY_NAME=%q. A name that is "+
			"neither the configured one nor empty is a name this build chose", fam.Name, "E2E Family")
	}

	var parents struct {
		Parents []parentDTO `json:"parents"`
	}
	h.call(http.MethodGet, "/parents", parent.Token, nil).expect(http.StatusOK).decode(&parents)
	gotParents := make([]string, 0, len(parents.Parents))
	for _, p := range parents.Parents {
		gotParents = append(gotParents, p.Email+"/"+p.Role)
	}
	sort.Strings(gotParents)
	wantParents := []string{
		secondParent.Email + "/ADMIN",
		primaryParent.Email + "/PRIMARY_ADMIN",
	}
	sort.Strings(wantParents)
	if strings.Join(gotParents, " ") != strings.Join(wantParents, " ") {
		t.Errorf("parents on a fresh system are %v, want exactly the two configured in "+
			"BOOTSTRAP_PARENT_EMAILS %v", gotParents, wantParents)
	}

	// The audit log is the one collection that is legitimately non-empty here, and that makes it the
	// sharpest place to look: a seeded build would have a history of things nobody did. One sign-in
	// has happened, so there must be exactly one row, and it must be that sign-in.
	entries := h.readAudit(parent.Token)
	if len(entries) != 1 {
		t.Errorf("a system whose only event is one sign-in has %d audit rows, want 1: %+v",
			len(entries), entries)
	} else if e := entries[0]; e.Action != "PARENT_SIGNED_IN" || e.ActorID != parent.Parent.ID {
		t.Errorf("the single audit row is %s by %s/%s, want PARENT_SIGNED_IN by parent/%s",
			e.Action, e.ActorType, e.ActorID, parent.Parent.ID)
	}

	// ---- 3. the console ships no rows --------------------------------------
	//
	// Read back over HTTP rather than off disk: the served bytes are what a phone gets, and the
	// embedding is one `go:embed` away from being a different file than the one in the tree.
	assertConsoleShipsNoRows(t, h)

	// ---- 4. the top-level collections are empty ARRAYS ----------------------
	//
	// No child and no device exists yet, so both must be `[]`. Read before the fixture below
	// creates either, because after that they are correctly non-empty.
	for _, c := range collectionsAt(stageFresh, "", "") {
		expectEmptyArray(t, h, parent.Token, c.path, c.key)
	}

	// ---- 5. a child and a device this test created, and nothing more --------
	//
	// From here on every row in the database was caused by this test, so anything else a per-entity
	// endpoint reports is something the build supplied. A new child is not pre-configured with
	// sample rules, and a device that has never been switched on has no telemetry.
	child := h.newChild(parent.Token, "Mira")
	device := h.newDevice(parent.Token, child.ID, "Mira's phone")

	for _, c := range collectionsAt(stageCreated, child.ID, device.ID) {
		expectEmptyArray(t, h, parent.Token, c.path, c.key)
	}

	// A fresh child's policy governs nothing. The defaults are deliberate and other tests pin their
	// values; what matters here is that no quota, bedtime or rule arrives that no parent set.
	var pol policyDTO
	h.call(http.MethodGet, "/children/"+child.ID+"/policy", parent.Token, nil).
		expect(http.StatusOK).decode(&pol)
	if pol.DailyLimitMinutes != 0 {
		t.Errorf("a brand-new child has a %d minute daily limit; nobody set one",
			pol.DailyLimitMinutes)
	}
	if pol.BedtimeEnabled {
		t.Error("a brand-new child has bedtime enabled; nobody set one")
	}
	if pol.TrackingOnly || pol.YouTubeBlocked {
		t.Errorf("a brand-new child arrives with enforcement already configured: %+v", pol)
	}
	if pol.Version != 1 {
		t.Errorf("a brand-new child's policy is at version %d, want 1: a higher version means "+
			"something already edited it", pol.Version)
	}

	// Usage is a number rather than a collection, and 0 is the honest answer for a phone that has
	// never reported: unlike battery level, minutes-used has a true value before any report, and it
	// is zero.
	usage := wireFields(t, h.call(http.MethodGet, "/devices/"+device.ID+"/usage", parent.Token, nil).
		expect(http.StatusOK).Body, "GET /devices/:id/usage")
	if got := string(usage["minutes"]); got != "0" {
		t.Errorf("a phone that has never reported has %s minutes of screen time today, want 0", got)
	}

	// ---- 6. unknown is reported as unknown, not as zero ---------------------
	//
	// Before enrolment the device has never checked in at all, so there is no state to report and
	// the field is absent rather than zeroed.
	view := wireFields(t, h.call(http.MethodGet, "/devices/"+device.ID, parent.Token, nil).
		expect(http.StatusOK).Body, "GET /devices/:id before enrolment")
	if got := strings.TrimSpace(string(view["state"])); got != "null" {
		t.Errorf("a device that has never checked in reports state %s, want null. A zeroed state "+
			"is a reading, and nobody has taken one", got)
	}
	if got := strings.TrimSpace(string(view["enrolled"])); got != "false" {
		t.Errorf("a device that has not been provisioned reports enrolled=%s", got)
	}

	// After enrolment the device HAS checked in once — that is a real event and last_seen_at is a
	// real measurement — but it has still never reported a battery level, a screen state or a
	// connectivity type. Those three must stay absent while the row itself exists. This is the
	// assertion the requirement is really about: the difference between "0%" and "we do not know".
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", []string{oemDialer})

	view = wireFields(t, h.call(http.MethodGet, "/devices/"+device.ID, parent.Token, nil).
		expect(http.StatusOK).Body, "GET /devices/:id after enrolment")
	if strings.TrimSpace(string(view["state"])) == "null" {
		t.Fatal("a device that has enrolled has checked in; its state row must exist")
	}
	state := wireFields(t, view["state"], "the state of a just-enrolled device")
	unmeasured := []string{"battery_level", "charging", "screen_on"}
	for _, field := range unmeasured {
		// Absent and null are both honest — the server uses `omitempty` on the pointer, so an
		// unmeasured reading does not appear at all — and both are what the console tests for. What
		// must never appear is a value: `0`, `false`, `"unknown"`.
		raw, present := state[field]
		if got := strings.TrimSpace(string(raw)); present && got != "null" {
			t.Errorf("a device that has enrolled and reported no telemetry has %s=%s, want the "+
				"field absent or null. Any other value is a reading this server did not receive",
				field, got)
		}
	}
	if got := strings.TrimSpace(string(state["connectivity"])); got != `""` {
		t.Errorf("a device that has reported no telemetry has connectivity=%s, want the empty "+
			"string", got)
	}
	if got := strings.TrimSpace(string(state["policy_version"])); got != "0" {
		t.Errorf("a device that has never applied a policy reports policy_version=%s, want 0", got)
	}

	// The negative control for the three assertions above, and the reason they are not enough on
	// their own. "Absent" carries meaning only if a *measured* value is present — and the mechanism
	// that omits an unmeasured reading is `omitempty`, which on a plain `int` would also omit a
	// real 0. A phone reporting a flat battery and a dark screen would then be indistinguishable
	// from one that has never reported at all, and the console would show nothing for the device
	// that most needs looking at.
	h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken, map[string]any{
		"battery_level": 0, "charging": false, "screen_on": false, "connectivity": "wifi",
	}).expect(http.StatusOK)

	view = wireFields(t, h.call(http.MethodGet, "/devices/"+device.ID, parent.Token, nil).
		expect(http.StatusOK).Body, "GET /devices/:id after a zero-valued heartbeat")
	state = wireFields(t, view["state"], "the state after a zero-valued heartbeat")
	for field, want := range map[string]string{
		"battery_level": "0", "charging": "false", "screen_on": "false",
	} {
		got, present := state[field]
		if !present || strings.TrimSpace(string(got)) != want {
			t.Errorf("a device that reported %s=%s has it back as %q (present=%v). A measured zero "+
				"must be reported as zero: dropping it makes a flat battery look like a phone that "+
				"never checked in", field, want, string(got), present)
		}
	}

	// The device's own view of the world, asked with the device's own credential, must be empty in
	// the same way — and `PendingCommands` is only reachable this way.
	for _, c := range collectionsAt(stageEnrolled, child.ID, device.ID) {
		expectEmptyArray(t, h, enrolled.DeviceToken, c.path, c.key)
	}

	// ---- 7. the computed answer contains no fabricated numbers --------------
	//
	// desired-state is derived rather than stored, so there is nothing here that could be "not yet
	// known": every field has a true value the moment a child exists. That makes a null anywhere in
	// it a bug rather than an honest absence, and it makes the arrays a second reading of section 4
	// through a different code path — the policy engine's, not the store's.
	desired := h.call(http.MethodGet, "/devices/"+device.ID+"/desired-state", parent.Token, nil).
		expect(http.StatusOK)
	var tree any
	if err := json.Unmarshal(desired.Body, &tree); err != nil {
		t.Fatalf("desired-state is not JSON: %v", err)
	}
	var nulls []string
	collectNulls(tree, "", &nulls)
	if len(nulls) > 0 {
		sort.Strings(nulls)
		t.Errorf("desired-state carries nulls at %v. Every field there is computed from data that "+
			"exists, so a null is a collection that came back nil rather than empty — which the "+
			"console iterates", nulls)
	}

	st := h.desiredState(parent.Token, device.ID, "")
	if st.Desired.QuotaMinutes != 0 || st.Desired.UsedMinutes != 0 || st.Desired.RemainingMinutes != 0 {
		t.Errorf("a child with no quota and a phone with no usage resolves to quota=%d used=%d "+
			"remaining=%d, want all zero", st.Desired.QuotaMinutes, st.Desired.UsedMinutes,
			st.Desired.RemainingMinutes)
	}
	if len(st.Input.Installed) != 0 {
		t.Errorf("a phone that has sent no inventory has %d installed apps: %+v",
			len(st.Input.Installed), st.Input.Installed)
	}

	// ---- 8. the ratchet ------------------------------------------------------
	assertEveryCollectionIsCovered(t, child.ID, device.ID)
}

// ---- the collections ---------------------------------------------------------

// stage says how much of the fixture has to exist before a collection can be read. The two
// top-level lists are only empty BEFORE the test creates a child and a phone, and the device's own
// queue is only reachable with the device's own credential, so a single flat loop would either
// assert the wrong thing or get a 401.
type stage int

const (
	stageFresh    stage = iota // nothing exists yet
	stageCreated               // a child and an un-enrolled device exist
	stageEnrolled              // read with the device's credential
)

// collection is one list a client can read, and the store function that produces it. The store name
// is what the ratchet at the bottom of this file matches against the source, so a new list function
// cannot arrive without an entry here.
type collection struct {
	path  string
	key   string
	store string
	stage stage
}

// allCollections is the whole table. childID and deviceID may be empty when the caller only wants
// the names — the ratchet does, and the paths it would build are never requested.
func allCollections(childID, deviceID string) []collection {
	return []collection{
		{"/children", "children", "ListChildren", stageFresh},
		{"/devices", "devices", "ListDevices", stageFresh},
		{"/children/" + childID + "/app-rules", "rules", "ListAppRules", stageCreated},
		{"/children/" + childID + "/blocked-domains", "domains", "ListBlockedDomains", stageCreated},
		{"/devices/" + deviceID + "/apps", "apps", "ListInstalledApps", stageCreated},
		{"/devices/" + deviceID + "/locations", "locations", "ListLocations", stageCreated},
		{"/devices/" + deviceID + "/recovery-events", "events", "ListRecoveryEvents", stageCreated},
		{"/devices/" + deviceID + "/commands", "commands", "ListCommands", stageCreated},
		{"/devices/" + deviceID + "/usage", "packages", "UsageForDay", stageCreated},
		{"/devices/" + deviceID + "/usage", "history", "UsageHistory", stageCreated},
		{"/device/commands", "commands", "PendingCommands", stageEnrolled},
	}
}

func collectionsAt(s stage, childID, deviceID string) []collection {
	var out []collection
	for _, c := range allCollections(childID, deviceID) {
		if c.stage == s {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		panic(fmt.Sprintf("no collections at stage %d: the table and the test have drifted", s))
	}
	return out
}

// Collections that reach a client somewhere other than as a top-level array, plus the one that
// never reaches a client at all. Each is here with the reason it cannot be driven by the table
// above, so the ratchet can tell "covered elsewhere" from "nobody noticed this one".
var collectionsCoveredElsewhere = map[string]string{
	"ListParents": "asserted by name and role in section 2: this is the one collection that is " +
		"legitimately non-empty on a fresh system, because a deployer configured it",
	"ListAudit": "asserted in section 2 as exactly one row — the sign-in this test performed",
	"BlockedPackages": "not its own endpoint; it arrives inside the policy response as " +
		"input.settings.blocked_packages, which section 7 checks for nulls along with every " +
		"other array in that tree",
	"DeviceIDsForChild": "never serialised. It fans a policy change out to a child's devices " +
		"inside the server and its result is a list of ids, not a response body",
}

// expectEmptyArray asserts one field arrived as the literal `[]`.
//
// It reads the bytes rather than a decoded value on purpose. json.Unmarshal maps both `[]` and
// `null` onto a nil slice, so `len(x) == 0` passes on a response that makes the console throw.
func expectEmptyArray(t *testing.T, h *harness, token, path, key string) {
	t.Helper()
	what := "GET " + path
	fields := wireFields(t, h.call(http.MethodGet, path, token, nil).expect(http.StatusOK).Body, what)
	raw, ok := fields[key]
	if !ok {
		t.Errorf("%s: the response has no %q field at all", what, key)
		return
	}
	if got := strings.TrimSpace(string(raw)); got != "[]" {
		t.Errorf("%s: %q came back as %s, want the literal []. On a fresh system this collection "+
			"is empty, and it must SAY empty: null is a different value, and the console iterates "+
			"it without checking", what, key, got)
	}
}

// wireFields decodes exactly one level, keeping every value as the bytes that came off the socket.
func wireFields(t *testing.T, body []byte, what string) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: not a JSON object: %v\nbody: %s", what, err, body)
	}
	return out
}

// collectNulls walks a decoded tree and records the dotted path of every JSON null.
func collectNulls(v any, path string, out *[]string) {
	switch t := v.(type) {
	case nil:
		if path == "" {
			path = "(root)"
		}
		*out = append(*out, path)
	case map[string]any:
		for k, child := range t {
			next := k
			if path != "" {
				next = path + "." + k
			}
			collectNulls(child, next, out)
		}
	case []any:
		for i, child := range t {
			collectNulls(child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// ---- the structural half -----------------------------------------------------

// assertMigrationsSeedNothing reads the schema the deployable build carries and refuses to find a
// row in it.
//
// This is the only half of NFR-5 that a running server cannot answer. A `INSERT INTO children` in
// the migration would produce a demo child in every deployment of this build, and a black-box probe
// of one instance could only ever report what that instance happens to hold.
func assertMigrationsSeedNothing(t *testing.T) {
	t.Helper()

	dir := filepath.Join("..", "..", "backend", "internal", "store", "migrations")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("could not read the migrations at %s (%d files, err %v): a check that scans no "+
			"files reports clean for the same reason a passing one does", dir, len(files), err)
	}

	// INSERT plants rows one at a time; COPY plants them in bulk and would sail past a scan that
	// only knows the first word.
	seeding := regexp.MustCompile(`(?i)\b(INSERT\s+INTO|COPY\s+\w+\s+FROM)\b`)
	comment := regexp.MustCompile(`--.*$`)

	found := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			// A `-- INSERT INTO` in a comment is prose about the schema, not a seed. Stripping it
			// is what keeps this assertion from being the permanently-red kind that gets muted.
			if m := seeding.FindString(comment.ReplaceAllString(line, "")); m != "" {
				found++
				t.Errorf("%s:%d plants rows (%s): the deployable schema must create tables and "+
					"nothing else.\n  %s", filepath.Base(file), i+1, m, strings.TrimSpace(line))
			}
		}
	}
	t.Logf("scanned %d migration file(s) for seeded rows: %d found", len(files), found)
}

// assertConsoleShipsNoRows checks the served markup for baked-in content.
//
// The console builds every row from an API response, so the containers it fills must arrive empty.
// A demo row in the shipped HTML would render before the first fetch and would survive a signed-out
// session — visible to anyone who loads the page.
func assertConsoleShipsNoRows(t *testing.T, h *harness) {
	t.Helper()

	html := string(h.call(http.MethodGet, "/index.html", "", nil).expect(http.StatusOK).Body)
	for _, container := range []struct{ id, tag string }{
		{"child-switcher", "nav"},
		{"view", "main"},
		{"sheet-body", "div"},
	} {
		inner, ok := innerMarkup(html, container.id, container.tag)
		if !ok {
			t.Errorf("the served console has no <%s id=%q> element; this check no longer knows "+
				"where the rows would be", container.tag, container.id)
			continue
		}
		if strings.TrimSpace(inner) != "" {
			t.Errorf("<%s id=%q> ships with content: %q. Everything in it comes from an API "+
				"response, so anything baked in is data no server sent",
				container.tag, container.id, strings.TrimSpace(inner))
		}
	}

	// A literal id in the client would be a reference to one specific family, child or device —
	// which is what a demo dataset looks like once it has been through a build.
	uuidLiteral := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	for _, asset := range []string{"/index.html", "/app.js", "/manifest.webmanifest"} {
		body := string(h.call(http.MethodGet, asset, "", nil).expect(http.StatusOK).Body)
		if m := uuidLiteral.FindString(body); m != "" {
			t.Errorf("%s contains the literal id %s: a client that names a specific row is "+
				"carrying data, not code", asset, m)
		}
		if strings.Contains(body, "Math.random") {
			t.Errorf("%s calls Math.random: a number the console invents is a number no device "+
				"reported", asset)
		}
	}
}

// innerMarkup returns what sits between the opening and closing tag of the element with the given
// id. Deliberately simple — it only handles the empty containers it is pointed at, and reports
// !ok rather than guessing if the shape changes.
func innerMarkup(doc, id, tag string) (string, bool) {
	anchor := strings.Index(doc, `id="`+id+`"`)
	if anchor < 0 {
		return "", false
	}
	open := strings.Index(doc[anchor:], ">")
	if open < 0 {
		return "", false
	}
	start := anchor + open + 1
	end := strings.Index(doc[start:], "</"+tag+">")
	if end < 0 {
		return "", false
	}
	return doc[start : start+end], true
}

// ---- the ratchet -------------------------------------------------------------

// assertEveryCollectionIsCovered reads the store's own source for functions that return a slice and
// requires each one to be either driven above or listed with a reason.
//
// Without it this file is pinned to the collections that existed the day it was written. A new
// `ListSomething` would arrive returning a nil slice, the console would throw on it, and this suite
// would stay green — the exact shape of "a guard that stopped growing with the code" that this
// project has found in an alerting rule, a coverage waiver and a proto ratchet already.
func assertEveryCollectionIsCovered(t *testing.T, childID, deviceID string) {
	t.Helper()

	dir := filepath.Join("..", "..", "backend", "internal", "store")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("could not read the store source at %s (%d files, err %v): this check cannot "+
			"report completeness against nothing", dir, len(files), err)
	}

	// A method on *Store whose first return value is a slice. That is what a collection is here;
	// nothing else in this package hands a list to a caller.
	sig := regexp.MustCompile(`^func \(s \*Store\) ([A-Z]\w*)\([^)]*\) \(\[\]`)

	found := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if m := sig.FindStringSubmatch(line); m != nil {
				found[m[1]] = true
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("found no collection-returning functions in %d store files: the pattern no "+
			"longer matches the signatures, so this check is measuring nothing", len(files))
	}

	driven := map[string]bool{}
	for _, c := range allCollections(childID, deviceID) {
		driven[c.store] = true
	}

	var uncovered []string
	for name := range found {
		if !driven[name] && collectionsCoveredElsewhere[name] == "" {
			uncovered = append(uncovered, name)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("the store returns collections this test never reads: %v. Add each to "+
			"allCollections with the endpoint that serves it and the stage it can be read at, "+
			"or to "+
			"collectionsCoveredElsewhere with the reason it cannot be — a collection nobody "+
			"drives is one that can start returning null unnoticed", uncovered)
	}

	// The other direction: an entry that no longer matches anything in the source is a line that
	// looks like coverage and checks nothing.
	var stale []string
	for name := range driven {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	for name := range collectionsCoveredElsewhere {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("this test claims to cover store functions that no longer exist: %v", stale)
	}
	t.Logf("collections: %d in the store, %d driven here, %d covered elsewhere with a reason",
		len(found), len(driven), len(collectionsCoveredElsewhere))
}
