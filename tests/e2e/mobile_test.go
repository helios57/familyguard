package e2e

// FR-13.2 and FR-13.3, measured on a rendered page rather than in the source that hopes to produce
// one.
//
// Everything else that guards the console reads text: the viewport meta is present, the stylesheet
// declares `--tap: 44px`, the manifest would install. Those are worth having and they are all
// satisfiable by a page that is unusable on a phone. Only a browser can answer whether the thumb
// has 44 px to hit, whether the page scrolls sideways, and whether the last card is reachable or
// parked under the tab bar.
//
// The data below is seeded through the real API before anything is measured, because an empty
// console lays out perfectly: the overflow this catches comes from a long device name, a package
// id, a domain — content, not chrome. A layout guard driven against empty views is the same kind of
// green as an alert rule evaluating zero series.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The phone the requirement is written about. 360x800 is the smallest width in wide use — a Galaxy
// A-series, the handset a family is most likely to hand down — and every constant in app.css is
// declared for it.
const (
	phoneWidth  = 360
	phoneHeight = 800

	// From app.css's own header: "no tap target smaller than 44 px, no input smaller than 16 px".
	// Repeated here rather than read out of the stylesheet on purpose. A guard that parses the
	// value it is checking agrees with whatever the stylesheet was changed to, and would have gone
	// green on the day somebody set `--tap: 24px`.
	minTapPx       = 44.0
	minInputFontPx = 16.0
)

type renderedBox struct {
	What   string  `json:"what"`
	Width  float64 `json:"w"`
	Height float64 `json:"h"`
}

type renderedOverflow struct {
	What  string  `json:"what"`
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
}

type renderedScroller struct {
	What        string  `json:"what"`
	ScrollWidth float64 `json:"scrollWidth"`
	ClientWidth float64 `json:"clientWidth"`
}

type renderedFont struct {
	What     string  `json:"what"`
	FontSize float64 `json:"fontSize"`
}

// layout is one measurement of one screen.
type layout struct {
	ViewportWidth float64 `json:"viewportWidth"`
	ScrollWidth   float64 `json:"scrollWidth"`
	ClientWidth   float64 `json:"clientWidth"`

	Overflowing []renderedOverflow `json:"overflowing"`
	Sideways    []renderedScroller `json:"sideways"`
	Tiny        []renderedBox      `json:"tiny"`
	SmallFont   []renderedFont     `json:"smallFont"`

	Interactive int `json:"interactive"`
	Elements    int `json:"elements"`
}

// measureJS is evaluated inside the page. It reports facts; every judgement is made in Go, so a
// change to what counts as a failure is a change to a test file and not to a string.
const measureJS = `(() => {
  const VIEWPORT = document.documentElement.clientWidth;
  const MIN_TAP = %f;
  const MIN_FONT = %f;

  const describe = (e) => {
    const id = e.id ? '#' + e.id : '';
    const cls = (typeof e.className === 'string' && e.className.trim())
      ? '.' + e.className.trim().split(/\s+/).join('.') : '';
    const text = (e.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 40);
    return e.tagName.toLowerCase() + id + cls + (text ? ' "' + text + '"' : '');
  };

  const shown = (e) => {
    const cs = getComputedStyle(e);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  const overflowing = [], sideways = [], tiny = [], smallFont = [];
  let elements = 0;

  for (const e of document.querySelectorAll('body *')) {
    if (!shown(e)) continue;
    elements++;
    const cs = getComputedStyle(e);
    const r = e.getBoundingClientRect();

    // Anything that scrolls sideways is reported by name rather than exempted. The console has
    // exactly one such container by design; a second one appearing is the finding, and an
    // exemption rule keyed on "has overflow-x" would be what hides it.
    if (e.scrollWidth > e.clientWidth + 1 && (cs.overflowX === 'auto' || cs.overflowX === 'scroll')) {
      sideways.push({ what: describe(e), scrollWidth: e.scrollWidth, clientWidth: e.clientWidth });
    }
    // An element inside the sideways-scrolling switcher is offscreen because it is swipeable, not
    // because the page is too wide.
    if (!e.closest('.pills') && (r.right > VIEWPORT + 0.5 || r.left < -0.5)) {
      overflowing.push({ what: describe(e), left: r.left, right: r.right });
    }
  }

  let interactive = 0;
  const TAPPABLE = 'a[href], button, input:not([type="hidden"]), select, textarea, summary, [role="button"]';
  for (const e of document.querySelectorAll(TAPPABLE)) {
    if (!shown(e)) continue;
    interactive++;
    // A checkbox wrapped in a <label> is activated by tapping anywhere on the label, so the label
    // is the tap target. Measuring the 30 px switch itself would report a failure for a 44 px row
    // and send somebody to "fix" a control that is already right — a false red costs the same
    // credibility as a false green.
    let target = e;
    const label = e.closest('label');
    if (label && (e.type === 'checkbox' || e.type === 'radio')) target = label;
    const r = target.getBoundingClientRect();
    if (r.width + 0.5 < MIN_TAP || r.height + 0.5 < MIN_TAP) {
      tiny.push({ what: describe(e), w: r.width, h: r.height });
    }
    if (e.tagName === 'INPUT' || e.tagName === 'SELECT' || e.tagName === 'TEXTAREA') {
      const fs = parseFloat(getComputedStyle(e).fontSize);
      if (fs + 0.01 < MIN_FONT) smallFont.push({ what: describe(e), fontSize: fs });
    }
  }

  return {
    viewportWidth: VIEWPORT,
    scrollWidth: document.scrollingElement.scrollWidth,
    clientWidth: document.scrollingElement.clientWidth,
    overflowing, sideways, tiny, smallFont, interactive, elements,
  };
})()`

// measure lays the current screen out and reports it. The name of the screen is carried through so
// a failure says which one, which is the difference between a finding and a hunt.
func (b *browser) measure(t *testing.T, screen string) layout {
	t.Helper()
	var out layout
	b.eval(fmt.Sprintf(measureJS, minTapPx, minInputFontPx), &out)

	// Calibration, on every screen rather than once: the emulation override is the only reason any
	// of this is a phone measurement, and an override that silently stopped applying would turn the
	// whole file into a desktop layout check that passes.
	if out.ViewportWidth != phoneWidth {
		t.Fatalf("%s: the page laid out at %.0f px, not %d — the phone emulation is not in effect, "+
			"so nothing this file reports is a measurement of a phone",
			screen, out.ViewportWidth, phoneWidth)
	}
	if out.Elements < 5 || out.Interactive < 1 {
		t.Fatalf("%s: %d visible elements and %d interactive ones — this screen is empty, and an "+
			"empty screen passes every check below", screen, out.Elements, out.Interactive)
	}
	return out
}

// check turns one measurement into pass or fail. Every rule is spelled out with what it costs the
// person holding the phone, because a layout failure with no consequence attached gets waived.
func (l layout) check(t *testing.T, screen string) {
	t.Helper()

	if l.ScrollWidth > l.ClientWidth+0.5 {
		t.Errorf("%s: the page scrolls sideways (%.0f px of content in a %.0f px viewport). "+
			"Every vertical swipe on a page that also scrolls horizontally is a chance to drift off "+
			"the column, and the controls at the right edge cannot be reached without noticing that "+
			"first.\noverflowing: %s", screen, l.ScrollWidth, l.ClientWidth, formatOverflow(l.Overflowing))
	}
	if len(l.Overflowing) > 0 {
		t.Errorf("%s: %d element(s) stick out past the %d px viewport:\n%s",
			screen, len(l.Overflowing), phoneWidth, formatOverflow(l.Overflowing))
	}
	// The child switcher is the one container built to be swiped sideways; app.css says so where it
	// is defined. Anything else that scrolls sideways is content that did not fit.
	for _, s := range l.Sideways {
		if !strings.Contains(s.What, ".pills") {
			t.Errorf("%s: %s scrolls sideways (%.0f px of content in %.0f px). Only the child "+
				"switcher is meant to; anywhere else it is content nobody will find, because there "+
				"is no affordance saying it is there.", screen, s.What, s.ScrollWidth, s.ClientWidth)
		}
	}
	if len(l.Tiny) > 0 {
		var b strings.Builder
		for _, e := range l.Tiny {
			fmt.Fprintf(&b, "\n  %.0fx%.0f  %s", e.Width, e.Height, e.What)
		}
		t.Errorf("%s: %d tap target(s) below %.0f px, which is the size a thumb hits reliably "+
			"while standing up:%s", screen, len(l.Tiny), minTapPx, b.String())
	}
	if len(l.SmallFont) > 0 {
		var b strings.Builder
		for _, e := range l.SmallFont {
			fmt.Fprintf(&b, "\n  %.1fpx  %s", e.FontSize, e.What)
		}
		t.Errorf("%s: %d input(s) below %.0fpx. Mobile Safari zooms the page in when a field that "+
			"small is focused and does not zoom back out, so the parent finishes the form on a page "+
			"they now have to pan:%s", screen, len(l.SmallFont), minInputFontPx, b.String())
	}
}

func formatOverflow(list []renderedOverflow) string {
	var b strings.Builder
	for _, e := range list {
		fmt.Fprintf(&b, "\n  x %.0f…%.0f  %s", e.Left, e.Right, e.What)
	}
	if b.Len() == 0 {
		return "\n  (none — the overflow is in the page box itself, not in an element)"
	}
	return b.String()
}

// ---- the test -------------------------------------------------------------

// switchTab clicks a tab and waits for the view to have been *re-rendered*, not merely for a
// matching element to exist somewhere in it.
//
// The console replaces `#view`'s children wholesale on every refresh, so a sentinel appended before
// the click disappears exactly when the new screen is in the DOM. Waiting on a selector alone is not
// enough, and that is not a hypothetical: several views render the same element types, so
// `#view .card` was already true of the screen being navigated *away* from, the wait returned
// instantly, and the measurement that followed was of the previous view under the new one's name.
// The provisioning subtest found it — it went looking for a "Setup QR" button and was handed the
// family view's Remove / Rename / Sign out.
func (b *browser) switchTab(t *testing.T, tab, ready string) {
	t.Helper()
	b.eval(fmt.Sprintf(`(() => {
  const marker = document.createElement('div');
  marker.id = 'stale-view-marker';
  document.getElementById('view').appendChild(marker);
  document.querySelector('.tab[data-tab=%q]').click();
})()`, tab), nil)
	b.waitFor(fmt.Sprintf(
		"location.hash === '#/%s' && document.getElementById('stale-view-marker') === null && "+
			"document.querySelector(%q) !== null", tab, ready),
		20*time.Second, "the "+tab+" view to be rendered afresh")
}

// FR-13.3, answered by the engine that would do the installing rather than by the file we ship.
//
// The manifest route already has tests: it is served, it parses as JSON, it declares a start_url, a
// display mode and an icon. Every one of them reads our own bytes back, and a manifest can satisfy
// all of them while Chrome declines to install the page — a start_url outside the scope, `display:
// browser`, an icon nothing can decode. "Installs to a phone" is a statement about the browser's
// rule, not about our JSON, so this test asks the browser.
func TestTheConsoleInstallsToAPhone(t *testing.T) {
	h := newHarness(t)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)

	// The negative control, first on purpose. An empty installability-error list is the shape of
	// both "this page installs" and "this browser computes nothing", and under the second reading
	// every assertion below passes having evaluated nothing. about:blank has no manifest at all, so
	// a browser that is really measuring has to complain about it.
	b.navigate("about:blank")
	if control := b.installabilityErrors(t); len(control) == 0 {
		t.Fatal("about:blank reported no installability errors, so this browser is not computing " +
			"them at all; every assertion below would pass having evaluated nothing")
	}

	b.navigate(h.base + "/")

	// The manifest as the browser parsed it, not as we wrote it.
	var manifest struct {
		URL    string `json:"url"`
		Errors []struct {
			Line   int `json:"line"`
			Column int `json:"column"`
			// An int, not a bool: CDP sends `"critical": 0`. A `bool` here unmarshals perfectly as
			// long as the list is empty, so the defect hides on the green path and surfaces only
			// when there is finally an error to report — the one moment this loop exists for. It
			// was found by breaking the manifest during calibration, not by reading the code.
			Critical int    `json:"critical"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b.call("Page.getAppManifest", nil), &manifest); err != nil {
		t.Fatalf("Page.getAppManifest: %v", err)
	}
	if manifest.URL == "" {
		t.Fatal("the browser found no web app manifest on the console at all; the <link rel=manifest> " +
			"is missing or points somewhere that does not answer")
	}
	for _, e := range manifest.Errors {
		t.Errorf("the browser could not parse %s at line %d column %d: %s (critical=%d)",
			manifest.URL, e.Line, e.Column, e.Message, e.Critical)
	}

	for _, e := range b.installabilityErrors(t) {
		args := make([]string, 0, len(e.ErrorArguments))
		for _, a := range e.ErrorArguments {
			args = append(args, a.Name+"="+a.Value)
		}
		t.Errorf("Chrome will not install the console to a home screen: %s %s",
			e.ErrorID, strings.Join(args, " "))
	}

	// The icon last, and by a different method, because it is a different failure: a page that is
	// installable but has no icon the browser can decode still installs — as a generated letter
	// tile. That is a console a parent cannot find on a home screen full of apps.
	raw, err := b.tryCall("Page.getManifestIcons", nil)
	if err != nil {
		t.Logf("Page.getManifestIcons is unavailable in this browser (%v); the home-screen icon is "+
			"NOT measured by this run", err)
		return
	}
	var icons struct {
		PrimaryIcon string `json:"primaryIcon"`
	}
	if err := json.Unmarshal(raw, &icons); err != nil {
		t.Fatalf("Page.getManifestIcons: %v", err)
	}
	if icons.PrimaryIcon == "" {
		t.Error("the browser decoded no icon from the manifest, so the console would land on the " +
			"home screen as a generated letter tile rather than as itself")
	}
}

func TestConsoleRendersOnAPhone(t *testing.T) {
	h := newHarness(t)
	seedAFamilyWorthLookingAt(t, h)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)

	// Signed out first. It is the only screen a parent sees before they have an account, and it is
	// the one screen the rest of this test cannot reach again once the session exists.
	b.navigate(h.base + "/")

	// Before a single pixel is measured: the console's own JavaScript must have executed.
	//
	// This is the guard the entire source-level suite lacked. `app.js` shipped with an unbalanced
	// parenthesis, was served 200 with the right content type and byte length, and passed every
	// asset-route, ETag, CSP, manifest and viewport-meta check that had ever been written for it —
	// while not one line of it had run in any browser. A page in that state renders the <noscript>
	// text and nothing else, so every assertion below it fails as "waited for X and it never
	// happened", naming neither the cause nor even the layer. One line here names it.
	var bootType string
	b.eval("typeof boot", &bootType)
	if bootType != "function" {
		t.Fatalf("the console's JavaScript did not run: `typeof boot` is %q, want \"function\".\n%s",
			bootType, b.pageErrorReport())
	}

	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.measure(t, "signed out").check(t, "signed out")

	// Sign in the way a phone does: tap the button and let the redirects happen in the browser.
	// Handing the token to localStorage directly would skip the flow whose final hop is the one
	// that has to land on a page laid out for a phone.
	h.issuer.setNextLogin(primaryParent)
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#child-switcher .pill').length >= 2", 15*time.Second,
		"both children to appear in the switcher")

	for _, screen := range []struct{ tab, ready string }{
		{"home", "#view .card"},
		{"rules", "#view .switch"},
		{"apps", "#view .list li"},
		{"activity", "#view .card"},
		{"family", "#view .list li"},
	} {
		t.Run(screen.tab, func(t *testing.T) {
			defer b.focus(t)()
			b.switchTab(t, screen.tab, screen.ready)

			b.measure(t, screen.tab).check(t, screen.tab)

			// The tab bar is fixed over the content, so "it fits" is not the same as "you can reach
			// the end of it". Two separate facts, and they need two separate measurements:
			//
			//  1. Unscrolled, the bar is already at the bottom edge of the screen.
			//  2. Scrolled to the very bottom, the last card is still above it.
			//
			// The first used to be measured *after* scrolling to the end, and calibration caught it:
			// with `.tabbar { position: static }` the suite stayed **green**, because at the end of
			// a long page a static bar has flowed to the bottom of the viewport too. The assertion
			// held while the property it names — pinned, reachable without scrolling — was gone.
			// Measured at rest, the same break is unmissable: the bar is below the fold.
			var bottom struct {
				ScrollHeight      float64 `json:"scrollHeight"`
				InnerHeight       float64 `json:"innerHeight"`
				TabbarBottomAtTop float64 `json:"tabbarBottomAtTop"`
				TabbarTop         float64 `json:"tabbarTop"`
				TabbarBottom      float64 `json:"tabbarBottom"`
				LastCardBottom    float64 `json:"lastCardBottom"`
			}
			b.eval(`(() => {
  const bar = document.querySelector('.tabbar');
  window.scrollTo(0, 0);
  const atTop = bar.getBoundingClientRect().bottom;
  window.scrollTo(0, document.scrollingElement.scrollHeight);
  const atEnd = bar.getBoundingClientRect();
  const cards = [...document.querySelectorAll('#view > *')];
  const last = cards.length ? cards[cards.length - 1].getBoundingClientRect().bottom : 0;
  return {
    scrollHeight: document.scrollingElement.scrollHeight,
    innerHeight: window.innerHeight,
    tabbarBottomAtTop: atTop,
    tabbarTop: atEnd.top, tabbarBottom: atEnd.bottom, lastCardBottom: last,
  };
})()`, &bottom)

			if d := bottom.TabbarBottomAtTop - bottom.InnerHeight; d < -0.5 || d > 0.5 {
				t.Errorf("%s: unscrolled, the tab bar's bottom edge is at %.0f in a %.0f px "+
					"viewport — it is not pinned to the bottom of the screen, so the navigation is "+
					"only reachable by scrolling to the end of whatever is on the page.",
					screen.tab, bottom.TabbarBottomAtTop, bottom.InnerHeight)
			}
			if bottom.LastCardBottom > bottom.TabbarTop+0.5 {
				t.Errorf("%s: scrolled all the way down, the last card ends at %.0f and the tab bar "+
					"starts at %.0f — the bottom %.0f px of the view is behind the navigation and "+
					"cannot be scrolled into the open.",
					screen.tab, bottom.LastCardBottom, bottom.TabbarTop,
					bottom.LastCardBottom-bottom.TabbarTop)
			}
			b.eval("window.scrollTo(0, 0)", nil)
		})
	}

	// The sheet is the only full-screen surface, and it is where the QR a parent has to point a
	// second phone at lives. It is measured separately because it is not in the DOM until it opens.
	t.Run("provisioning sheet", func(t *testing.T) {
		defer b.focus(t)()
		// Waited for by the button this subtest is about to click, not by "a card exists".
		b.switchTab(t, "home", "#view .card")
		b.waitFor(`[...document.querySelectorAll('#view button')]`+
			`.some((x) => /set up|qr|provision/i.test(x.textContent))`,
			20*time.Second, "the set-up button on the home view")
		b.eval(`(() => {
  const buttons = [...document.querySelectorAll('#view button')];
  const setup = buttons.find((x) => /set up|qr|provision/i.test(x.textContent));
  if (!setup) throw new Error('no set-up button on the home view: ' +
    buttons.map((x) => x.textContent.trim()).join(' | '));
  setup.click();
})()`, nil)
		b.waitFor("document.getElementById('sheet').open", 20*time.Second, "the provisioning sheet")
		b.waitFor("document.querySelector('#sheet-body svg') !== null", 20*time.Second, "the QR to render")

		b.measure(t, "provisioning sheet").check(t, "provisioning sheet")

		var sheet struct {
			Width   float64 `json:"width"`
			Height  float64 `json:"height"`
			QRWidth float64 `json:"qrWidth"`
		}
		b.eval(`(() => {
  const r = document.getElementById('sheet').getBoundingClientRect();
  const qr = document.querySelector('#sheet-body svg').getBoundingClientRect();
  return { width: r.width, height: r.height, qrWidth: qr.width };
})()`, &sheet)
		if sheet.Width > phoneWidth+0.5 {
			t.Errorf("the provisioning sheet is %.0f px wide on a %d px screen", sheet.Width, phoneWidth)
		}
		// A QR that renders smaller than about a third of the screen is one a second phone's camera
		// has to be held still against. This is the artefact the whole enrolment depends on.
		if sheet.QRWidth < float64(phoneWidth)/3 {
			t.Errorf("the provisioning QR renders %.0f px wide on a %d px screen; a camera has to "+
				"resolve 25 modules across that", sheet.QRWidth, phoneWidth)
		}
		b.eval("document.getElementById('sheet-close').click()", nil)
	})
}

// seedAFamilyWorthLookingAt fills the console with the content a layout has to survive: two
// children so the switcher is a real row, a phone that has reported apps and usage, blocked
// domains, a second parent, and — deliberately — the longest plausible values for the fields that
// are drawn as a single line. A layout guard against short strings measures the guard.
func seedAFamilyWorthLookingAt(t *testing.T, h *harness) {
	t.Helper()

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Mira")
	h.newChild(parent.Token, "Jonas-Alexander")

	// A name a parent would actually type, and one long enough to prove the ellipsis is real.
	device := h.newDevice(parent.Token, child.ID, "Mira's phone — the blue one with the cracked screen")
	_, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Samsung Galaxy A54 5G", "Android 14", []string{oemDialer})

	h.call(http.MethodPost, "/device/heartbeat", enrolled.DeviceToken, map[string]any{
		"battery_level": 41, "charging": false, "screen_on": true, "connectivity": "WIFI",
	}).expect(http.StatusOK)

	h.call(http.MethodPost, "/device/inventory", enrolled.DeviceToken, map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Game"},
			{"package_name": pkgChat, "label": "Chat"},
			{"package_name": pkgYouTube, "label": "YouTube"},
			{"package_name": pkgAOSPPhone, "label": "Phone", "system_app": true},
			{"package_name": "com.supercell.clashofclans.and.a.very.long.package.identifier",
				"label": "An app with a name long enough to need the ellipsis it was given"},
		},
	}).expect(http.StatusOK)

	h.call(http.MethodPost, "/device/usage", enrolled.DeviceToken, map[string]any{
		"samples": map[string]int64{
			pkgGame:    97 * 60 * 1000,
			pkgChat:    23 * 60 * 1000,
			pkgYouTube: 41 * 60 * 1000,
		},
	}).expect(http.StatusOK)

	h.call(http.MethodPost, "/device/location", enrolled.DeviceToken, map[string]any{
		"latitude": 47.3769, "longitude": 8.5417,
	}).expect(http.StatusOK)

	h.patchPolicy(parent.Token, child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": "21:00", "bedtime_end": "07:00",
		"daily_limit_minutes": 120, "youtube_blocked": true, "timezone": "Europe/Zurich",
	})
	for _, domain := range []string{"example.com", "a-domain-long-enough-to-need-wrapping.example.co.uk"} {
		h.call(http.MethodPost, "/children/"+child.ID+"/blocked-domains", parent.Token,
			map[string]any{"domain": domain}).expect(http.StatusCreated)
	}

	// A third parent, with an address long enough to be the one that overflows. The other two are
	// already there: the harness bootstraps both, so adding `secondParent` here would be a 409 and
	// the list would still only ever have been measured with short names.
	h.call(http.MethodPost, "/parents", parent.Token, map[string]any{
		"email": "grandmother.on.the.other.side@a-rather-long-mail-provider.example.org",
		"role":  "GUARDIAN",
	}).expect(http.StatusCreated)

	// Proof the seed landed, so a console that renders three empty views cannot be read as a
	// layout that survived them. json is used rather than a typed decode because the only thing
	// asserted here is "the server agrees there is something to draw".
	var devices struct {
		Devices []json.RawMessage `json:"devices"`
	}
	h.call(http.MethodGet, "/devices?child_id="+child.ID, parent.Token, nil).
		expect(http.StatusOK).decode(&devices)
	if len(devices.Devices) != 1 {
		t.Fatalf("the seed left %d devices, expected 1", len(devices.Devices))
	}
}
