package e2e

// FR-15.6 / FR-15.7 as a parent sees them: the console says a phone is behind, and says when an
// update did not take.
//
// This is the layer the whole feature failed at. On 2026-09-06 the phone's update was refused by
// Android, the command was acknowledged as "installing now", the phone kept heartbeating, and every
// screen a parent could look at stayed green — the only difference between "up to date" and "stuck
// on a build from six weeks ago" was a version number nobody reads. So the two states have to be
// visibly different in the rendered page, and that is what is measured here rather than in the API:
// a field that reaches /devices and is never drawn is a field that does not exist.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// card is what one device card in the home view actually renders.
type card struct {
	Text     string   `json:"text"`
	Badges   []string `json:"badges"`
	Warnings []string `json:"warnings"`
	Buttons  []string `json:"buttons"`
}

// deviceCardJS reads everything the console says about "The blue phone": its phone sheet, opened
// from its row in Übersicht if it is not open already. Since the redesign of 2026-09-30 the sheet is
// where a phone's facts, badges, warnings and commands live; an open sheet is redrawn from every
// refresh, so reading it after `refresh()` reads what the server now says.
const deviceCardJS = `(() => {
  const dlg = document.getElementById('sheet');
  if (!(dlg.open && dlg.dataset.kind === 'device' && document.getElementById('sheet-title').textContent === 'The blue phone')) {
    const row = Array.from(document.querySelectorAll('#view button.device-row')).find((r) => r.textContent.indexOf('The blue phone') >= 0);
    if (!row) throw new Error('no row for the device; Übersicht holds: '
      + Array.from(document.querySelectorAll('#view .device-row')).map((c) => c.textContent.slice(0, 40)).join(' | '));
    row.click();
  }
  const body = document.getElementById('sheet-body');
  return {
    text: body.textContent,
    badges: Array.from(body.querySelectorAll('.badge')).map((b) => b.textContent),
    warnings: Array.from(body.querySelectorAll('.notice')).map((p) => p.textContent),
    buttons: Array.from(body.querySelectorAll('button')).map((b) => b.textContent),
  };
})()`

// sheetShowsJS is true once the open phone sheet contains s.
func sheetShowsJS(s string) string {
	return "(() => { const d = document.getElementById('sheet'); return d.open && document.getElementById('sheet-body').textContent.indexOf(" + jsString(s) + ") >= 0; })()"
}

func TestTheConsoleShowsAPhoneThatIsBehindAndWhyItsUpdateFailed(t *testing.T) {
	// The server hosts build 2; the phone will report build 1. Both numbers come from a real
	// archive the server parses at startup, so "behind" is a comparison of two measurements rather
	// than of two constants this test wrote.
	apkPath := filepath.Join(t.TempDir(), "familyguard.apk")
	if err := os.WriteFile(apkPath, fixtureAPK(t, "fixture-v2.apk"), 0o600); err != nil {
		t.Fatalf("write the hosted DPC: %v", err)
	}
	h := newHarness(t, withSelfHostedAPK(apkPath))

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "The blue phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy S20", "Android 13", nil)

	beat := func(body map[string]any) {
		t.Helper()
		body["connectivity"] = "wifi"
		h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken, body).expect(http.StatusOK)
	}

	// The state this feature exists for: an old build, and an update that did not take.
	const reason = "Android asked for someone to confirm this install; a device owner should never be asked"
	beat(map[string]any{
		"app_version_name": "0.0.1", "app_version_code": 1, "update_error": reason,
	})

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")

	var bootType string
	b.eval("typeof boot", &bootType)
	if bootType != "function" {
		t.Fatalf("the console's JavaScript did not run: `typeof boot` is %q\n%s", bootType, b.pageErrorReport())
	}

	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .device-row').length > 0", 15*time.Second, "Übersicht")

	var behind card
	b.eval(deviceCardJS, &behind)

	if !strings.Contains(strings.Join(behind.Warnings, "\n"), reason) {
		t.Errorf("the card does not carry the platform's own reason.\nwarnings: %q\ncard: %s",
			behind.Warnings, behind.Text)
	}
	if !strings.Contains(strings.Join(behind.Warnings, "\n"), "hat das letzte Update nicht übernommen") {
		t.Errorf("the reason is shown without saying what it is about: %q", behind.Warnings)
	}
	// The badge pair: what it runs, and what it could run. Both are needed — "app 0.0.1" alone is a
	// number with nothing to compare it to, which is what the console showed while the phone was
	// stuck.
	if !hasBadge(behind.Badges, "App 0.0.1") {
		t.Errorf("the card does not say which build the phone runs: %q", behind.Badges)
	}
	if !hasBadge(behind.Badges, "→ 0.0.2") {
		t.Errorf("the card does not say which build the server offers: %q", behind.Badges)
	}
	if !hasButton(behind.Buttons, "Auf 0.0.2 aktualisieren") {
		t.Errorf("the update button does not name the build it would install: %q", behind.Buttons)
	}

	// **The state a real family ended up in, and the one this test could not see until 2026-09-20.**
	// The phone catches up to the hosted build while the stored failure is STILL SET — which is not
	// an edge case but the normal outcome of a failure recorded against the newest build: the DPC
	// clears its record only when a build ABOVE the recorded one runs, and there is no such build.
	// On the family phone one lost `apk-info` connection put "This phone did not take the last
	// update" on the card and left it there, on a phone that was on the newest build and
	// heartbeating every 60 seconds.
	//
	// It sits here, before the control below, because the control below changes TWO things at once
	// — the build and the error — so whichever of them the card was keying on, it would go green.
	// That is exactly why this was not caught: a negative control that moves two variables proves
	// nothing about either.
	beat(map[string]any{
		"app_version_name": "0.0.2", "app_version_code": 2, "update_error": reason,
	})
	b.eval("refresh()", nil)
	b.waitFor(sheetShowsJS("App 0.0.2"), 15*time.Second, "the phone sheet to follow the update")

	var caughtUp card
	b.eval(deviceCardJS, &caughtUp)

	if joined := strings.Join(caughtUp.Warnings, "\n"); strings.Contains(joined, "hat das letzte Update nicht übernommen") {
		t.Errorf("the phone runs the build the server hosts and the console still calls its update failed."+
			"\nA warning that cannot go away is one a parent learns to scroll past.\nwarnings: %q", joined)
	}
	// The failure text itself must go with it: half a warning — the platform's words with no
	// sentence saying what they are about — is worse than none.
	if joined := strings.Join(caughtUp.Warnings, "\n"); strings.Contains(joined, reason) {
		t.Errorf("the stale reason is still drawn on an up-to-date phone: %q", joined)
	}
	// ...and the card has not otherwise gone quiet. Without this, a card that failed to render at
	// all would pass both assertions above.
	if !hasBadge(caughtUp.Badges, "App 0.0.2") {
		t.Errorf("the card stopped naming the build the phone runs: %q", caughtUp.Badges)
	}

	// The negative control, and the half that makes the assertions above mean something. The phone
	// reports the build the server hosts and nothing to report; every difference must disappear.
	// Without this, a card that drew the warning unconditionally would pass everything above.
	beat(map[string]any{"app_version_name": "0.0.2", "app_version_code": 2, "update_error": ""})
	b.eval("refresh()", nil)
	b.waitFor(sheetShowsJS("App 0.0.2"), 15*time.Second, "the phone sheet to follow the update")

	var current card
	b.eval(deviceCardJS, &current)

	if joined := strings.Join(current.Warnings, "\n"); strings.Contains(joined, "hat das letzte Update nicht übernommen") {
		t.Errorf("the phone reported the hosted build and the console still shows the old failure: %q", joined)
	}
	for _, badge := range current.Badges {
		if strings.HasPrefix(badge, "→ ") {
			t.Errorf("an up-to-date phone is still offered %q", badge)
		}
	}
	if !hasButton(current.Buttons, "App aktualisieren") {
		t.Errorf("with nothing to offer, the button must go back to its plain label: %q", current.Buttons)
	}
	if !hasBadge(current.Badges, "App 0.0.2") {
		t.Errorf("the card stopped naming the build the phone runs: %q", current.Badges)
	}
}

func hasBadge(badges []string, want string) bool {
	for _, b := range badges {
		if strings.TrimSpace(b) == want {
			return true
		}
	}
	return false
}

func hasButton(buttons []string, want string) bool {
	for _, b := range buttons {
		if strings.TrimSpace(b) == want {
			return true
		}
	}
	return false
}
