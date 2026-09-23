package e2e

// FR-5.10 and FR-5.11 end to end against the real server: a preinstalled app a child can open is
// free unless it is screen time or a parent decided otherwise, and an app the phone no longer has
// can be taken off the list without losing the rule for it.

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

const (
	pkgCamera  = "com.sec.android.app.camera"
	pkgGallery = "com.sec.android.gallery3d"
	pkgChrome  = "com.android.chrome"
)

func TestPreinstalledAppsAreFreeByDefault(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"})
	zurich, _ := time.LoadLocation("Europe/Zurich")
	today := time.Now().In(zurich).Format("2006-01-02")

	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Brawl Stars"},
			{"package_name": pkgCamera, "label": "Camera", "system_app": true, "launchable": true},
			{"package_name": pkgGallery, "label": "Gallery", "system_app": true, "launchable": true},
			{"package_name": pkgChrome, "label": "Chrome", "system_app": true, "launchable": true},
			{"package_name": "com.samsung.android.emergency", "system_app": true, "launchable": false},
		},
	}).expect(http.StatusOK)
	// 65 minutes of use, the camera's included: free means never paused, not uncounted — the
	// same as a parent's "Always free".
	h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
		"day": today,
		"samples": map[string]int64{
			pkgGame: 50 * 60_000, pkgCamera: 10 * 60_000, pkgChrome: 5 * 60_000,
		},
	}).expect(http.StatusOK)

	type desired struct {
		SuspendReason string   `json:"suspend_reason"`
		Suspended     []string `json:"suspended_packages"`
		Hidden        []string `json:"hidden_packages"`
		FreeByDefault []string `json:"free_by_default"`
	}
	read := func() desired {
		var out struct {
			Desired desired `json:"desired"`
		}
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/desired-state", f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		return out.Desired
	}

	d := read()
	if d.SuspendReason != "QUOTA" {
		t.Fatalf("65 of 60 minutes should pause the phone; reason=%q", d.SuspendReason)
	}
	for _, pkg := range []string{pkgGame, pkgChrome} {
		if !slices.Contains(d.Suspended, pkg) {
			t.Errorf("%s counts toward the daily limit and should be paused: %v", pkg, d.Suspended)
		}
	}
	for _, pkg := range []string{pkgCamera, pkgGallery} {
		if slices.Contains(d.Suspended, pkg) {
			t.Errorf("%s is part of the phone and should stay usable (FR-5.10): %v", pkg, d.Suspended)
		}
	}
	if want := []string{pkgCamera, pkgGallery}; !slices.Equal(d.FreeByDefault, want) {
		t.Errorf("free_by_default = %v, want %v", d.FreeByDefault, want)
	}

	// The phone recomputes offline from what it is sent (FR-9), so it must be sent the list too.
	var devicePolicy struct {
		Input struct {
			Settings struct {
				Counted []string `json:"counted_system_packages"`
			} `json:"settings"`
		} `json:"input"`
	}
	h.call(http.MethodGet, "/device/policy", f.deviceToken(), nil).expect(http.StatusOK).decode(&devicePolicy)
	if !slices.Contains(devicePolicy.Input.Settings.Counted, pkgChrome) {
		t.Errorf("the phone is not told that Chrome counts: %v", devicePolicy.Input.Settings.Counted)
	}

	// The console's Activity card says the same, per app.
	type row struct {
		PackageName   string `json:"package_name"`
		Blocked       string `json:"blocked"`
		FreeByDefault bool   `json:"free_by_default"`
	}
	var view struct {
		Apps []row `json:"apps"`
	}
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline?day="+today, f.parent.Token, nil).
		expect(http.StatusOK).decode(&view)
	rows := map[string]row{}
	for _, r := range view.Apps {
		rows[r.PackageName] = r
	}
	if r := rows[pkgCamera]; !r.FreeByDefault || r.Blocked != "" {
		t.Errorf("the camera's row should say free and not blocked: %+v", r)
	}
	if r := rows[pkgChrome]; r.FreeByDefault || r.Blocked != "QUOTA" {
		t.Errorf("Chrome's row should say paused by the daily limit: %+v", r)
	}

	// A parent's rule outranks the default: blocking the gallery hides it and takes it off the list.
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgGallery, "action": "BLOCK"}).expect(http.StatusOK)
	d = read()
	if !slices.Contains(d.Hidden, pkgGallery) || slices.Contains(d.FreeByDefault, pkgGallery) {
		t.Errorf("a blocked gallery must be hidden and not free: hidden=%v free=%v", d.Hidden, d.FreeByDefault)
	}
	// And a LIMIT puts the camera on the daily limit like any installed app.
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgCamera, "action": "LIMIT"}).expect(http.StatusOK)
	d = read()
	if !slices.Contains(d.Suspended, pkgCamera) || len(d.FreeByDefault) != 0 {
		t.Errorf("a camera on the daily limit must be paused with it: suspended=%v free=%v", d.Suspended, d.FreeByDefault)
	}
}

func TestAnUninstalledAppCanBeRemovedFromTheList(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	inventory := func(pkgs ...string) {
		apps := []map[string]any{}
		for _, p := range pkgs {
			apps = append(apps, map[string]any{"package_name": p, "label": labelFor(p)})
		}
		h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{"apps": apps}).
			expect(http.StatusOK)
	}
	listed := func() map[string]bool {
		var out struct {
			Apps []struct {
				PackageName string  `json:"package_name"`
				RemovedAt   *string `json:"removed_at"`
			} `json:"apps"`
		}
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/apps?include_system=1", f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		m := map[string]bool{}
		for _, a := range out.Apps {
			m[a.PackageName] = a.RemovedAt != nil
		}
		return m
	}
	forget := func(pkg string) apiResponse {
		return h.call(http.MethodDelete, "/devices/"+f.device.ID+"/apps/"+pkg, f.parent.Token, nil)
	}

	inventory(pkgGame, pkgWhatsApp)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/app-rules", f.parent.Token,
		map[string]any{"package_name": pkgGame, "action": "BLOCK"}).expect(http.StatusOK)

	// Installed: refused, and the row stays.
	forget(pkgGame).expectError(http.StatusConflict, "still_installed")
	if removed, ok := listed()[pkgGame]; !ok || removed {
		t.Fatalf("a refused removal changed the list: present=%v removed=%v", ok, removed)
	}

	// Uninstalled: the row is kept (removed_at) until a parent removes it.
	inventory(pkgWhatsApp)
	if removed := listed()[pkgGame]; !removed {
		t.Fatal("the uninstalled game should still be listed, marked removed")
	}
	forget(pkgGame).expect(http.StatusNoContent)
	if _, ok := listed()[pkgGame]; ok {
		t.Fatal("the game is still listed after it was removed from the list")
	}
	forget(pkgGame).expect(http.StatusNotFound)
	forget("com.example.never.installed").expect(http.StatusNotFound)
	if _, ok := listed()[pkgWhatsApp]; !ok {
		t.Fatal("removing one app took another off the list")
	}

	// The rule survives: a blocked game that comes back is still blocked.
	var rules struct {
		Rules []struct {
			PackageName string `json:"package_name"`
			Action      string `json:"action"`
		} `json:"rules"`
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/app-rules", f.parent.Token, nil).
		expect(http.StatusOK).decode(&rules)
	kept := false
	for _, r := range rules.Rules {
		kept = kept || (r.PackageName == pkgGame && r.Action == "BLOCK")
	}
	if !kept {
		t.Fatalf("removing the game from the list dropped its rule: %+v", rules.Rules)
	}
	inventory(pkgGame, pkgWhatsApp)
	var out struct {
		Desired struct {
			Hidden []string `json:"hidden_packages"`
		} `json:"desired"`
	}
	h.call(http.MethodGet, "/devices/"+f.device.ID+"/desired-state", f.parent.Token, nil).
		expect(http.StatusOK).decode(&out)
	if !slices.Contains(out.Desired.Hidden, pkgGame) {
		t.Errorf("the reinstalled game is not hidden: %v", out.Desired.Hidden)
	}
}
