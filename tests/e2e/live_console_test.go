package e2e

// FR-27 in the console, at phone width: a guardian starts Live for the walk home, sees the phone's
// position arrive with its age, and stops it; the admin's device card shows the same session.

import (
	"net/http"
	"testing"
	"time"
)

func TestAGuardianStartsLiveAndSeesThePosition(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.addParent(f.parent.Token, guardianIdentity.Email, "GUARDIAN")
	liveNow := func() liveDTO {
		var l liveDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/live", f.parent.Token, nil).expect(http.StatusOK).decode(&l)
		return l
	}

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(guardianIdentity)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor(`document.querySelectorAll('#view .guardian-card [data-action="live-start"]').length === 1`,
		15*time.Second, "the guardian's Live button")

	b.eval(`document.querySelector('#view [data-action="live-start"]').click()`, nil)
	b.waitFor(`document.querySelector('#view [data-live="on"]') && document.querySelector('#view [data-live="on"]').textContent.includes('Live bis')`,
		15*time.Second, "the card to say Live is on")
	if l := liveNow(); !l.Active {
		t.Fatalf("the console says Live and the server holds %+v", l)
	}
	b.measure(t, "guardian/live").check(t, "guardian/live")

	// The phone reports; the page follows through its event stream, without a reload.
	h.call(http.MethodPost, "/device/location", f.deviceToken(), map[string]any{
		"latitude": 47.37, "longitude": 8.54, "accuracy_m": 9,
	}).expect(http.StatusOK)
	b.waitFor(`document.querySelector('#view [data-live="on"]').textContent.includes('Letzte Position')
	           && document.querySelector('#view [data-live="on"]').textContent.includes('±9 m')
	           && !!document.querySelector('#view [data-live="on"] a[href*="mlat=47.37"]')`,
		15*time.Second, "the position, its accuracy and a map link")

	b.eval(`document.querySelector('#view [data-action="live-stop"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view [data-action="live-start"]')`, 15*time.Second, "Live to end")
	if l := liveNow(); l.Active {
		t.Errorf("the guardian stopped Live and the server still holds %+v", l)
	}

	// The admin's device card, in English, for a session started elsewhere.
	h.call(http.MethodPost, "/devices/"+f.device.ID+"/live", f.parent.Token, map[string]any{"minutes": 10}).expect(http.StatusOK)
	a := startBrowser(t)
	a.phone(phoneWidth, phoneHeight)
	a.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	a.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	a.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	a.waitFor(`document.querySelector('#view [data-live="on"]') && document.querySelector('#view [data-live="on"]').textContent.includes('Live until')`,
		15*time.Second, "the admin's device card to show the session")
	a.measure(t, "home/live").check(t, "home/live")
}

// FR-26.1 in the console: a phone that checked in but holds no stream is resting, and the card says
// what that means for Lock and Ring; once it holds its stream it is simply online.
func TestTheConsoleSaysWhenAPhoneIsResting(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor(`!!document.querySelector('#view [data-link="resting"]') && !!document.querySelector('#view [data-resting]')`,
		15*time.Second, "the card to say the phone is resting and what that means")
	b.measure(t, "home/resting").check(t, "home/resting")

	stream := h.openStream("/device/stream", f.deviceToken())
	defer stream.Close()
	b.eval(`location.reload()`, nil)
	b.waitFor(`!!document.querySelector('#view [data-link="listening"]') && !document.querySelector('#view [data-resting]')`,
		15*time.Second, "the card to say online once the phone holds its stream")
}
