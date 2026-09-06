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

const deviceCardJS = `(() => {
  const cards = Array.from(document.querySelectorAll('#view .card'));
  const card = cards.find((c) => c.textContent.indexOf('The blue phone') >= 0);
  if (!card) throw new Error('no card for the device; the home view holds: '
    + cards.map((c) => c.textContent.slice(0, 40)).join(' | '));
  return {
    text: card.textContent,
    badges: Array.from(card.querySelectorAll('.badge')).map((b) => b.textContent),
    warnings: Array.from(card.querySelectorAll('p.warn')).map((p) => p.textContent),
    buttons: Array.from(card.querySelectorAll('button')).map((b) => b.textContent),
  };
})()`

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
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")

	var behind card
	b.eval(deviceCardJS, &behind)

	if !strings.Contains(strings.Join(behind.Warnings, "\n"), reason) {
		t.Errorf("the card does not carry the platform's own reason.\nwarnings: %q\ncard: %s",
			behind.Warnings, behind.Text)
	}
	if !strings.Contains(strings.Join(behind.Warnings, "\n"), "did not take the last update") {
		t.Errorf("the reason is shown without saying what it is about: %q", behind.Warnings)
	}
	// The badge pair: what it runs, and what it could run. Both are needed — "app 0.0.1" alone is a
	// number with nothing to compare it to, which is what the console showed while the phone was
	// stuck.
	if !hasBadge(behind.Badges, "app 0.0.1") {
		t.Errorf("the card does not say which build the phone runs: %q", behind.Badges)
	}
	if !hasBadge(behind.Badges, "→ 0.0.2") {
		t.Errorf("the card does not say which build the server offers: %q", behind.Badges)
	}
	if !hasButton(behind.Buttons, "Update to 0.0.2") {
		t.Errorf("the update button does not name the build it would install: %q", behind.Buttons)
	}

	// The negative control, and the half that makes the assertions above mean something. The phone
	// reports the build the server hosts and nothing to report; every difference must disappear.
	// Without this, a card that drew the warning unconditionally would pass everything above.
	beat(map[string]any{"app_version_name": "0.0.2", "app_version_code": 2, "update_error": ""})
	b.eval("refresh()", nil)
	b.waitFor(
		"(() => { const c = Array.from(document.querySelectorAll('#view .card'))"+
			".find((x) => x.textContent.indexOf('The blue phone') >= 0);"+
			"return !!c && c.textContent.indexOf('0.0.2') >= 0; })()",
		15*time.Second, "the card to follow the update")

	var current card
	b.eval(deviceCardJS, &current)

	if joined := strings.Join(current.Warnings, "\n"); strings.Contains(joined, "did not take the last update") {
		t.Errorf("the phone reported the hosted build and the console still shows the old failure: %q", joined)
	}
	for _, badge := range current.Badges {
		if strings.HasPrefix(badge, "→ ") {
			t.Errorf("an up-to-date phone is still offered %q", badge)
		}
	}
	if !hasButton(current.Buttons, "Update app") {
		t.Errorf("with nothing to offer, the button must go back to its plain label: %q", current.Buttons)
	}
	if !hasBadge(current.Badges, "app 0.0.2") {
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
