package e2e

// FR-5.10 and FR-5.11 as a parent meets them, in a real browser: Camera is in the Apps list without
// hunting behind a switch and says it is always free, a service with no icon is not, and an app the
// phone no longer has can be removed from the list with one tap.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const appRowsJS = `Array.from(document.querySelectorAll('#view .applist li')).map((li) => ({
  text: li.textContent,
  forget: !!Array.from(li.querySelectorAll('button')).find((b) => b.textContent === 'Remove from list'),
}))`

type appRowText struct {
	Text   string `json:"text"`
	Forget bool   `json:"forget"`
}

func rowFor(rows []appRowText, pkg string) *appRowText {
	for i := range rows {
		if strings.Contains(rows[i].Text, pkg) {
			return &rows[i]
		}
	}
	return nil
}

func TestTheConsoleShowsPreinstalledAppsAndRemovesUninstalledOnes(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "The blue phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy S20", "Android 13", nil)

	const pkgOld = "com.example.oldgame"
	const pkgService = "com.samsung.android.service.hidden"
	apps := []map[string]any{
		{"package_name": pkgGame, "label": "Brawl Stars"},
		{"package_name": pkgCamera, "label": "Camera", "system_app": true, "launchable": true},
		{"package_name": pkgService, "label": "Hidden Service", "system_app": true, "launchable": false},
	}
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken,
		map[string]any{"apps": append(append([]map[string]any{}, apps...),
			map[string]any{"package_name": pkgOld, "label": "Old Game"})}).expect(http.StatusOK)
	// Uninstalled since: the phone's next report no longer has it.
	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken,
		map[string]any{"apps": apps}).expect(http.StatusOK)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	b.eval("document.querySelector('.tab[data-tab=\"apps\"]').click()", nil)
	b.waitFor("document.querySelectorAll('#view .applist li').length > 0", 15*time.Second,
		"the Apps tab to list the phone's apps")

	var rows []appRowText
	b.eval(appRowsJS, &rows)
	camera := rowFor(rows, pkgCamera)
	if camera == nil {
		t.Fatalf("the Camera is not in the Apps list with the default filters: %+v", rows)
	}
	if !strings.Contains(camera.Text, "Always free (preinstalled)") {
		t.Errorf("the Camera row does not say it is always free: %q", camera.Text)
	}
	if camera.Forget {
		t.Errorf("an installed app offers 'Remove from list': %q", camera.Text)
	}
	if r := rowFor(rows, pkgService); r != nil {
		t.Errorf("a service with no icon is listed without 'Show background services': %q", r.Text)
	}
	if r := rowFor(rows, pkgGame); r == nil || strings.Contains(r.Text, "preinstalled") {
		t.Errorf("the child's own game must be listed and must not be called preinstalled: %+v", r)
	}
	old := rowFor(rows, pkgOld)
	if old == nil || !old.Forget {
		t.Fatalf("the uninstalled game has no 'Remove from list' button: %+v", old)
	}

	b.eval(`(() => { const li = Array.from(document.querySelectorAll('#view .applist li'))
        .find((l) => l.textContent.indexOf('`+pkgOld+`') >= 0);
      Array.from(li.querySelectorAll('button')).find((b) => b.textContent === 'Remove from list').click();
      return true; })()`, nil)
	b.waitFor(`!Array.from(document.querySelectorAll('#view .applist li'))
        .some((l) => l.textContent.indexOf('`+pkgOld+`') >= 0)`, 15*time.Second,
		"the removed app to leave the list")

	// The authority agrees: the row is gone from the server, not only from the page.
	var listed struct {
		Apps []struct {
			PackageName string `json:"package_name"`
		} `json:"apps"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/apps?include_system=1", parent.Token, nil).
		expect(http.StatusOK).decode(&listed)
	for _, a := range listed.Apps {
		if a.PackageName == pkgOld {
			t.Fatal("the console removed the row from the page but the server still lists it")
		}
	}
}
