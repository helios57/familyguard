package console

import (
	"regexp"
	"sort"
	"testing"

	"github.com/helios57/familyguard/backend/internal/store"
)

// TestTheConsoleCanSendEveryCommandTheServerAccepts pins the join between the closed set of
// commands and the buttons that issue them.
//
// This guard exists because its absence cost a real evening. `STOP_ALARM` is in FR-9's table, the
// API has accepted it since the beginning, the DPC implements the handler and `SirenControllerTest`
// covers it — and the console never had a button for it. On 2026-09-07 a parent rang the phone and
// then could not stop it: not from the console, which offered no such control, and not from the
// handset. The siren ran its full five-minute cap with two people watching it.
//
// Nothing was red. Every layer was individually correct and individually tested; the wiring between
// the last two was simply absent, and no test in this repo looked at the join. That is the same
// shape as a guard that is defined and never called — each piece passes, the feature does not
// exist.
//
// The mirror of this check already existed on the device side, pointing the other way:
// `CommandHandlersTest` *the handlers implement exactly the command types the server accepts* reads
// the same `ValidCommandTypes`. Between them the set is now pinned at both ends — the phone can
// carry out everything the server accepts, and a parent can ask for it.
//
// Both authorities are read, never restated: `store.ValidCommandTypes` is the symbol itself, and
// the assets come out of the embedded FS, which is the copy that actually ships rather than a file
// on disk that a build might not include.
func TestTheConsoleCanSendEveryCommandTheServerAccepts(t *testing.T) {
	raw, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatalf("could not read the embedded console (%v): a check that scans nothing reports "+
			"clean for the same reason a passing one does", err)
	}

	// The console's own helper. Matching the call rather than the bare string is deliberate: a
	// command named only in a comment — which is exactly what a removed button leaves behind — is
	// not a button, and a scan for the bare literal would accept the tombstone as the feature.
	issues := regexp.MustCompile(`\bcmd\('([A-Z_]+)'`)
	wired := map[string]bool{}
	for _, m := range issues.FindAllStringSubmatch(string(raw), -1) {
		wired[m[1]] = true
	}
	if len(wired) == 0 {
		t.Fatalf("no cmd('…') call sites in the embedded app.js: the console no longer issues " +
			"commands this way, so this check is measuring nothing")
	}
	if len(store.ValidCommandTypes) < 4 {
		t.Fatalf("ValidCommandTypes holds only %d entries: it has stopped being the closed set "+
			"and this check would pass on a console with almost no buttons",
			len(store.ValidCommandTypes))
	}

	// A command that is deliberately not a button, and where a parent reaches it instead. Naming
	// the alternative rather than just excusing the type is the point: it is a claim about the
	// console that the next reader can go and check.
	elsewhere := map[string]string{
		store.CmdTypeBlockYouTube: "Rules → the `youtube_blocked` switch. Blocking YouTube is a " +
			"standing fact about a child rather than a one-off instruction, so it is policy and " +
			"the server issues the command itself when the switch moves",
		store.CmdTypeUnblockYouTube: "Rules → the same `youtube_blocked` switch, turned off",
	}

	var missing []string
	for cmd := range store.ValidCommandTypes {
		if wired[cmd] || elsewhere[cmd] != "" {
			continue
		}
		missing = append(missing, cmd)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the server accepts %v but the console offers no way to send them. Add a "+
			"cmd('TYPE', 'Label') button, or add the type to `elsewhere` naming the control a "+
			"parent uses instead — a command nobody can issue is a feature that does not exist, "+
			"however well the server and the phone implement it", missing)
	}

	// The other direction, and it is not symmetry for its own sake: a button whose type the API
	// rejects is a 400 that a parent reads as "the phone did not answer".
	var unknown []string
	for cmd := range wired {
		if !store.ValidCommandTypes[cmd] {
			unknown = append(unknown, cmd)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("the console offers %v, which ValidCommandTypes does not accept: the API answers "+
			"400 and the parent sees a button that never works", unknown)
	}
}
