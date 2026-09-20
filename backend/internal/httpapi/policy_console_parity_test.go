package httpapi

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/helios57/familyguard/backend/internal/console"
)

// TestTheConsoleCanSetEveryPolicyFieldTheApiAccepts pins the join between the settings the server
// will take and the controls a parent can actually reach.
//
// This is the same shape as the STOP_ALARM defect that `console.TestTheConsoleCanSendEveryCommandTheServerAccepts`
// exists for, one layer over: the API accepted the command, the phone implemented it, a test
// covered the handler, and no button issued it — so the feature did not exist for the only person
// who needed it. A policy field fails the same way and more quietly, because there is no moment
// like a siren that will not stop to reveal it. It cost a real gap here too: `timezone` was
// accepted by this endpoint from the beginning, is what bedtime and the daily reset are measured
// against, and the console printed it as prose over a value nothing could change.
//
// Both sides are read from their authorities and neither is restated. The settable set is the
// json tags of [patchPolicyRequest] by reflection, so a field joins this test on the commit that
// adds it; the console comes out of the embedded FS, which is the copy that ships rather than a
// file on disk a build might not include.
//
// "Has a control" is deliberately weak — the key appearing in a PATCH body the console builds.
// A strict check would have to understand the DOM helper, and the failure this guards against is a
// field with NO path at all, not one with a clumsy widget.
func TestTheConsoleCanSetEveryPolicyFieldTheApiAccepts(t *testing.T) {
	assets, err := console.FS()
	if err != nil {
		t.Fatalf("could not open the console (%v): a check that scans nothing reports clean for "+
			"the same reason a passing one does", err)
	}
	raw, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		t.Fatalf("could not read app.js: %v", err)
	}
	app := string(raw)

	// A positive control. Without it, a renamed or emptied asset would make every field below
	// "missing" and read as a console that lost its Rules tab rather than as a test that lost its
	// input.
	if !strings.Contains(app, "'/children/' + state.childId + '/policy'") {
		t.Fatalf("app.js does not contain the policy PATCH call at all: this test is scanning the "+
			"wrong thing, so its verdict about the %d fields below means nothing",
			reflect.TypeOf(patchPolicyRequest{}).NumField())
	}

	typ := reflect.TypeOf(patchPolicyRequest{})
	var missing []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		key, _, _ := strings.Cut(tag, ",")
		if key == "" || key == "-" {
			t.Fatalf("%s carries no json tag, so the wire name a parent's browser would send "+
				"cannot be derived — this test cannot judge it either way", typ.Field(i).Name)
		}
		// The two forms the console builds a patch body with: a literal key in an object, and the
		// computed key the `toggle` helper uses (`{ [key]: … }` over a string it was handed).
		if !strings.Contains(app, key+":") && !strings.Contains(app, "'"+key+"'") {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the API accepts these policy settings and the console offers no way to set them: %s\n"+
			"A setting a parent cannot reach is a setting that does not exist, however well the "+
			"server implements it.", strings.Join(missing, ", "))
	}
}
