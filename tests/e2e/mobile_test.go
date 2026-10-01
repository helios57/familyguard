package e2e

// FR-13.2 and FR-13.3, measured on a rendered page rather than in the source that hopes to produce
// one.
//
// Everything else that guards the console reads text: the viewport meta is present, the stylesheet
// declares `--tap: 44px`, the manifest would install. Those are worth having and they are all
// satisfiable by a page that is unusable on a phone. Only a browser can answer whether the thumb
// has 44 px to hit, whether the page scrolls sideways, whether the top of a view is reachable or
// parked under the header, and whether the bar that holds the whole navigation on a phone is pinned
// where a thumb can reach it.
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
	DeadStyles  []string           `json:"deadStyles"`

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

  const overflowing = [], sideways = [], tiny = [], smallFont = [], deadStyles = [];
  let elements = 0;

  // A style attribute the CSP dropped: present in the DOM, empty declaration behind it.
  //
  // style-src 'self' is the fallback for style-src-attr, so setAttribute('style', …) applies
  // NOTHING and says nothing — no exception, no page error, just an element drawn with whatever the
  // stylesheet gave it. Hidden elements are scanned too, because the one screen behind a <dialog>
  // is exactly where this would next hide.
  for (const e of document.querySelectorAll('[style]')) {
    if ((e.getAttribute('style') || '').trim() && !e.style.cssText) deadStyles.push(describe(e));
  }

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
    overflowing, sideways, tiny, smallFont, deadStyles, interactive, elements,
  };
})()`

// measure lays the current screen out and reports it. The name of the screen is carried through so
// a failure says which one, which is the difference between a finding and a hunt.
func (b *browser) measure(t *testing.T, screen string) layout {
	t.Helper()
	return b.measureAt(t, screen, phoneWidth)
}

// measureAt is measure with the width it is supposed to be measuring named by the caller, so the
// calibration below still discriminates when the caller is not a phone. It takes the width as an
// argument rather than reading it back out of the page: a check that asserts the viewport equals
// whatever the viewport happens to be is the shape of a control that evaluates nothing.
func (b *browser) measureAt(t *testing.T, screen string, wantWidth int) layout {
	t.Helper()
	var out layout
	b.eval(fmt.Sprintf(measureJS, minTapPx, minInputFontPx), &out)

	// Calibration, on every screen rather than once: the emulation override is the only reason any
	// of this is a measurement of the width it claims, and an override that silently stopped
	// applying would turn the whole file into a check of some other layout that passes.
	if out.ViewportWidth != float64(wantWidth) {
		t.Fatalf("%s: the page laid out at %.0f px, not %d — the viewport emulation is not in "+
			"effect, so nothing this reports is a measurement of that width",
			screen, out.ViewportWidth, wantWidth)
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
		t.Errorf("%s: %d element(s) stick out past the %.0f px viewport:\n%s",
			screen, len(l.Overflowing), l.ViewportWidth, formatOverflow(l.Overflowing))
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
	// The console computes a handful of dimensions per screen — a quota meter's width, an hour
	// column's height, a swatch's colour — and every one of them is a number that MEANS something.
	// Delivered as a style attribute they are dropped by the console's own CSP, silently, and the
	// element renders with the stylesheet's value instead: the quota meter drew 100% full at every
	// level of usage for the whole life of this console, because `.meter > span` is `display:block`
	// with no width of its own. It is not visible to any assertion about text, and it is not visible
	// to a person either — a full meter is a plausible meter.
	if len(l.DeadStyles) > 0 {
		t.Errorf("%s: %d element(s) carry a style attribute the CSP dropped, so they are drawn with "+
			"the stylesheet's value and not the computed one. Set computed dimensions through CSSOM "+
			"(`el()`'s object form), never as an attribute:\n  %s",
			screen, len(l.DeadStyles), strings.Join(l.DeadStyles, "\n  "))
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
// It also navigates the way a thumb does: through the link in the navigation, which it first
// requires to be visible — a link that is in the DOM but off screen still follows on `.click()`,
// and a test that did not look would keep passing after the bar stopped being reachable.
func (b *browser) switchTab(t *testing.T, tab, ready string) {
	t.Helper()
	// A sub-page ("rules/agenda") is reached the way a parent reaches it: the view's own link in
	// the navigation, then the sub-page's link on that view.
	view, sub, _ := strings.Cut(tab, "/")
	b.eval(fmt.Sprintf(`(() => {
  const marker = document.createElement('div');
  marker.id = 'stale-view-marker';
  document.getElementById('view').appendChild(marker);
  const shown = (e) => {
    const cs = getComputedStyle(e);
    const r = e.getBoundingClientRect();
    return cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0;
  };
  // Already here. Clicking the link you are on changes no hash, fires no hashchange and re-renders
  // nothing, so the marker would never clear and this helper would time out saying the view was
  // never rendered afresh — a sentence about a broken page describing a no-op click.
  if (location.hash === '#/%s') { refresh(); return; }
  const link = document.querySelector('.tab[data-tab=%q]');
  if (!link || !shown(link)) throw new Error('the %q link is not visible');
  if (location.hash.split('/')[1] !== %q || %q === '') link.click();
})()`, tab, view, view, view, sub), nil)
	if sub != "" {
		b.waitFor(fmt.Sprintf("location.hash.startsWith('#/%s') && document.querySelector('.subtabs a[data-sub=%q]') !== null", view, sub),
			20*time.Second, "the "+view+" view's sub-pages")
		b.eval(fmt.Sprintf(`document.querySelector('.subtabs a[data-sub=%q]').click()`, sub), nil)
	}
	b.waitFor(fmt.Sprintf(
		"location.hash === '#/%s' && document.getElementById('stale-view-marker') === null && "+
			"document.querySelector(%q) !== null", tab, ready),
		20*time.Second, "the "+tab+" view to be rendered afresh")
}

// press sends one key to the page. Used for Escape, which is the sheet behaviour that comes from
// <dialog> rather than from our code — and is therefore the one that silently disappears the day
// somebody reimplements the sheet as a <div>.
func (b *browser) press(key string, code int) {
	for _, kind := range []string{"rawKeyDown", "keyUp"} {
		b.call("Input.dispatchKeyEvent", map[string]any{
			"type":                  kind,
			"key":                   key,
			"code":                  key,
			"windowsVirtualKeyCode": code,
			"nativeVirtualKeyCode":  code,
		})
	}
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
	// With an application catalog, because the FR-16 card is only drawn when the deployment has
	// one. Without it the Apps screen renders a single line of prose where the switches are, and
	// this file would report a measured layout for a card it never saw.
	h, _ := catalogHarness(t)
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

	// The complaint this rework started from, made into a rule. The sign-in card used to be centred
	// in a `min-height: 100dvh` grid: on a laptop that is one small box alone in an empty viewport,
	// and on a phone it pushes the only control on the page towards the middle of a screen that now
	// scrolls. The property is the same at both widths — the thing a parent came here to do is on
	// screen when the page loads, without a swipe first.
	var signin struct {
		ButtonBottom float64 `json:"buttonBottom"`
		InnerHeight  float64 `json:"innerHeight"`
	}
	b.eval(`(() => {
  window.scrollTo(0, 0);
  const btn = document.querySelector('#signin a.btn-primary');
  if (!btn) throw new Error('there is no sign-in button on the signed-out screen');
  return { buttonBottom: btn.getBoundingClientRect().bottom, innerHeight: window.innerHeight };
})()`, &signin)
	if signin.ButtonBottom > signin.InnerHeight+0.5 {
		t.Errorf("the sign-in button ends at %.0f in a %.0f px viewport, so it is below the fold: "+
			"the first thing the console asks a parent to do is scroll to find the only control on "+
			"the page.", signin.ButtonBottom, signin.InnerHeight)
	}

	// Sign in the way a phone does: tap the button and let the redirects happen in the browser.
	// Handing the token to localStorage directly would skip the flow whose final hop is the one
	// that has to land on a page laid out for a phone.
	h.issuer.setNextLogin(primaryParent)
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#child-switcher .pill').length >= 2", 15*time.Second,
		"both children to appear in the switcher")

	for _, screen := range []struct{ tab, ready string }{
		{"overview", "#view .child-card"},
		{"rules", "#view .switch"},
		{"rules/protection", "#view .switch"},
		{"rules/agenda", "#view .agenda-card"},
		{"apps", "#view .list li"},
		{"activity", "#view .card"},
		{"family", "#view .list li"},
	} {
		t.Run(screen.tab, func(t *testing.T) {
			defer b.focus(t)()
			b.switchTab(t, screen.tab, screen.ready)

			b.measure(t, screen.tab).check(t, screen.tab)

			// The header is sticky over the content, so "it is on screen when the page loads" is
			// not the same as "it is on screen when you want it". Two facts, two measurements, and
			// each one taken at the scroll position where a broken header would look different:
			//
			//  1. Scrolled to the end of a long page, the header is still at the top of the
			//     viewport.
			//  2. Unscrolled, the first card starts BELOW the header rather than behind it.
			//
			// The first is measured at the end on purpose, and it is the exact mirror of the
			// mistake this file made while the navigation was at the bottom. There, "pinned" was
			// measured after scrolling to the end — where even a `position: static` bar has flowed
			// to the bottom edge — and the calibration script found the assertion staying green
			// with the property gone. For a header the useless moment is the top of the page, where
			// a static header also sits at y=0. Same defect, opposite end of the page.
			var chrome struct {
				ScrollHeight   float64 `json:"scrollHeight"`
				InnerHeight    float64 `json:"innerHeight"`
				TopbarTopAtEnd float64 `json:"topbarTopAtEnd"`
				TopbarHeight   float64 `json:"topbarHeight"`
				TopbarBottom   float64 `json:"topbarBottomAtTop"`
				FirstCardTop   float64 `json:"firstCardTopAtTop"`
			}
			b.eval(`(() => {
  const bar = document.querySelector('.topbar');
  window.scrollTo(0, document.scrollingElement.scrollHeight);
  const atEnd = bar.getBoundingClientRect();
  window.scrollTo(0, 0);
  const atTop = bar.getBoundingClientRect();
  const cards = [...document.querySelectorAll('#view > *')];
  const first = cards.length ? cards[0].getBoundingClientRect().top : 0;
  return {
    scrollHeight: document.scrollingElement.scrollHeight,
    innerHeight: window.innerHeight,
    topbarTopAtEnd: atEnd.top,
    topbarHeight: atTop.height,
    topbarBottomAtTop: atTop.bottom,
    firstCardTopAtTop: first,
  };
})()`, &chrome)

			// Rule 1 is only a measurement on a page long enough to scroll. On a short one
			// "still at the top after scrolling" is true of a static header too, and reporting it
			// as a pass would be the green that means nothing.
			if chrome.ScrollHeight < chrome.InnerHeight+80 {
				t.Logf("%s: the page is %.0f px in a %.0f px viewport, so there is nothing to "+
					"scroll and whether the header is sticky is NOT MEASURED on this screen",
					screen.tab, chrome.ScrollHeight, chrome.InnerHeight)
			} else if chrome.TopbarTopAtEnd < -0.5 || chrome.TopbarTopAtEnd > 0.5 {
				t.Errorf("%s: scrolled to the end of the page, the header's top edge is at %.0f "+
					"instead of 0 — it scrolled away with the content, so the navigation and the "+
					"child switcher can only be reached by scrolling all the way back up first.",
					screen.tab, chrome.TopbarTopAtEnd)
			}
			if chrome.FirstCardTop < chrome.TopbarBottom-0.5 {
				t.Errorf("%s: unscrolled, the header ends at %.0f and the first card starts at "+
					"%.0f — the top %.0f px of the view is underneath the header and cannot be "+
					"scrolled out from behind it.",
					screen.tab, chrome.TopbarBottom, chrome.FirstCardTop,
					chrome.TopbarBottom-chrome.FirstCardTop)
			}
			// The complaint that started this rework was "the login took the whole space and I had
			// to scroll for everything". Chrome that grows is how that comes back: two stacked rows
			// of header on a 800px screen is a tenth of the phone spent before any content.
			if max := float64(phoneHeight) * 0.15; chrome.TopbarHeight > max {
				t.Errorf("%s: the header is %.0f px tall on a %d px screen (%.0f%%), over the %.0f "+
					"px budget. Every pixel of permanent chrome is one the content scrolls past "+
					"forever.", screen.tab, chrome.TopbarHeight, phoneHeight,
					chrome.TopbarHeight/float64(phoneHeight)*100, max)
			}
			b.eval("window.scrollTo(0, 0)", nil)
		})
	}

	// The bottom bar IS the navigation below 900px: one element, `#mainnav`, pinned to the bottom
	// edge where a thumb reaches it. Everything above measures screens it has already been used to
	// reach, which proves the links work; this proves the rest of the contract — that it is pinned,
	// that it holds every destination at full size, that it covers neither the last thing on a long
	// page nor the toast that reports what a tap did, and that following a link goes there.
	t.Run("bottom bar", func(t *testing.T) {
		defer b.focus(t)()
		// Routed by setting the hash rather than through switchTab, which uses the bar under test.
		b.eval("location.hash = '#/overview'", nil)
		b.waitFor("location.hash === '#/overview' && document.querySelector('#view .child-card') !== null",
			20*time.Second, "the overview")

		var bar struct {
			ScrollHeight    float64 `json:"scrollHeight"`
			InnerHeight     float64 `json:"innerHeight"`
			BottomAtTop     float64 `json:"bottomAtTop"`
			TopAtEnd        float64 `json:"topAtEnd"`
			LastBottomAtEnd float64 `json:"lastBottomAtEnd"`
			VisibleTabs     int     `json:"visibleTabs"`
			ToastBottom     float64 `json:"toastBottom"`
			BarTop          float64 `json:"barTop"`
		}
		b.eval(`(() => {
  const nav = document.getElementById('mainnav');
  const shown = (e) => {
    const cs = getComputedStyle(e);
    const r = e.getBoundingClientRect();
    return cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0
      && r.bottom <= window.innerHeight + 0.5 && r.top >= -0.5;
  };
  // Pinned is measured at the TOP of a long page: a bar that merely flows at the end of the
  // content also sits at the bottom edge once you have scrolled there, which is the moment a
  // check would pass having measured nothing.
  window.scrollTo(0, 0);
  const atTop = nav.getBoundingClientRect();
  window.scrollTo(0, document.scrollingElement.scrollHeight);
  const atEnd = nav.getBoundingClientRect();
  const kids = [...document.querySelectorAll('#view > *')];
  const last = kids.length ? kids[kids.length - 1].getBoundingClientRect().bottom : 0;
  toast('Messung');
  const t = document.getElementById('toast').getBoundingClientRect();
  window.scrollTo(0, 0);
  return {
    scrollHeight: document.scrollingElement.scrollHeight,
    innerHeight: window.innerHeight,
    bottomAtTop: atTop.bottom,
    topAtEnd: atEnd.top,
    lastBottomAtEnd: last,
    visibleTabs: [...nav.querySelectorAll('.tab')].filter(shown).length,
    toastBottom: t.bottom,
    barTop: atTop.top,
  };
})()`, &bar)

		if bar.ScrollHeight < bar.InnerHeight+80 {
			t.Fatalf("the overview is %.0f px in a %.0f px viewport: nothing scrolls, so whether the "+
				"bar is pinned is NOT MEASURED — the seed has to fill more than a screen",
				bar.ScrollHeight, bar.InnerHeight)
		}
		if bar.BottomAtTop < bar.InnerHeight-0.5 || bar.BottomAtTop > bar.InnerHeight+0.5 {
			t.Errorf("at the top of a long page the navigation ends at %.0f in a %.0f px viewport: "+
				"it is not pinned to the bottom edge, so the destinations are only reachable after "+
				"scrolling to the end", bar.BottomAtTop, bar.InnerHeight)
		}
		if bar.VisibleTabs != consoleDestinations {
			t.Errorf("the bottom bar shows %d of the %d destinations on screen", bar.VisibleTabs, consoleDestinations)
		}
		if bar.LastBottomAtEnd > bar.TopAtEnd+0.5 {
			t.Errorf("scrolled to the end, the last card ends at %.0f and the bar starts at %.0f: "+
				"the bottom %.0f px of every long page is underneath the navigation and cannot be "+
				"scrolled out from behind it", bar.LastBottomAtEnd, bar.TopAtEnd, bar.LastBottomAtEnd-bar.TopAtEnd)
		}
		if bar.ToastBottom > bar.BarTop+0.5 {
			t.Errorf("the toast ends at %.0f and the bar starts at %.0f: the one line that says "+
				"whether a tap worked is drawn underneath the navigation", bar.ToastBottom, bar.BarTop)
		}

		b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
		b.waitFor(`location.hash === '#/rules' && document.querySelector('.tab[data-tab="rules"]').getAttribute('aria-current') === 'page'`,
			10*time.Second, "the Regeln link to open Regeln and mark itself current")
	})

	// The sheet is the only full-screen surface, and it is where the QR a parent has to point a
	// second phone at lives. It is measured separately because it is not in the DOM until it opens.
	t.Run("provisioning sheet", func(t *testing.T) {
		defer b.focus(t)()
		// Waited for by the button this subtest is about to click, not by "a card exists". Scoped
		// to the phone that has never enrolled: on the enrolled one the same button revokes the
		// device, and that path is measured in "replace phone" below.
		b.switchTab(t, "overview", "#view .child-card")
		b.waitFor(deviceCardButton("The spare phone", `/einrichten/i`)+" !== null",
			20*time.Second, "the set-up button on the spare phone's row")
		b.eval(deviceCardButton("The spare phone", `/einrichten/i`)+".click()", nil)
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
		// Escape closes it, and that comes from <dialog>, not from a key handler of ours.
		b.press("Escape", 27)
		b.waitFor("!document.getElementById('sheet').open", 10*time.Second, "Escape to close the sheet")
	})

	// FR-1.7, rendered. The single most expensive button in this console: on an enrolled phone it
	// does not "show the code again", it REVOKES the device — the phone stops reporting at once and
	// only comes back if somebody picks it up and types the new code into it (FR-1.8), which a
	// phone on an older build cannot do at all. That is how the first real phone in this family was
	// disconnected, by a parent tapping a button labelled "Setup QR" expecting to re-read a code.
	//
	// Three things are measured here and each one failed in a different way before: the button says
	// what it does, the confirmation is drawn where a thumb is (a window.confirm is at the top of
	// the screen, cannot mark the destructive answer, and blocks the page so hard that headless
	// Chrome cannot see past it), and backing out leaves the phone enrolled.
	t.Run("replace phone", func(t *testing.T) {
		defer b.focus(t)()
		b.switchTab(t, "overview", "#view .child-card")
		openPhoneSheet(t, b, enrolledPhoneName)

		var label string
		b.eval(`document.querySelector('#sheet-body [data-action="replace"]').textContent.trim()`, &label)
		if !strings.Contains(strings.ToLower(label), "ersetzen") {
			t.Errorf("the enrolled phone's provisioning button reads %q; it revokes the device, and "+
				"a label that reads like \"show me that code again\" is what disconnected the first "+
				"real phone", label)
		}

		b.eval(`document.querySelector('#sheet-body [data-action="replace"]').click()`, nil)
		b.waitFor("document.getElementById('sheet').dataset.kind === 'confirm'", 20*time.Second, "the confirmation in the sheet")
		b.waitFor("document.getElementById('sheet').open", 20*time.Second, "the confirmation sheet")
		// No QR: this is the confirmation, and a sheet that went straight to the code would mean
		// the device had already been revoked by the time the parent read anything.
		var sheet struct {
			Title   string   `json:"title"`
			Body    string   `json:"body"`
			Buttons []string `json:"buttons"`
			HasQR   bool     `json:"hasQR"`
		}
		b.eval(`(() => {
  const body = document.getElementById('sheet-body');
  return {
    title: document.getElementById('sheet-title').textContent.trim(),
    body: body.textContent,
    buttons: [...body.querySelectorAll('button')].map((x) => x.textContent.trim()),
    hasQR: body.querySelector('svg') !== null,
  };
})()`, &sheet)
		if sheet.HasQR {
			t.Error("tapping Replace phone went straight to a QR code: the device is revoked before " +
				"the parent has been asked anything")
		}
		// The consequence in the parent's own terms, and both halves of it. "Revokes" is what
		// happens to the phone; the re-link sentence is what it costs to undo — somebody has to
		// physically pick the phone up. A sheet that mentioned only the second would read as
		// reassurance for an action that is still destructive.
		for _, phrase := range []string{"widerruft", "neu verbinden", "älteren version"} {
			if !strings.Contains(strings.ToLower(sheet.Body), phrase) {
				t.Errorf("the confirmation never says %q, so the parent is not told what this "+
					"costs. It reads: %q", phrase, sheet.Body)
			}
		}
		if len(sheet.Buttons) != 2 {
			t.Fatalf("the confirmation offers %v; it needs exactly one way forward and one way out",
				sheet.Buttons)
		}

		b.measure(t, "replace phone confirmation").check(t, "replace phone confirmation")

		// Back out. The way most parents will leave this sheet, and the one that must not have
		// touched the phone.
		b.eval(`(() => {
  const cancel = [...document.querySelectorAll('#sheet-body button')].pop();
  cancel.click();
})()`, nil)
		b.waitFor("!document.getElementById('sheet').open", 20*time.Second, "the confirmation to close")
		// Re-read the server's answer rather than the DOM that was already on screen: the question
		// is whether the phone is still enrolled, and only a fresh fetch can tell.
		b.eval("(async () => { await refresh(); return true; })()", nil)

		// An enrolled phone's row is the button that opens its sheet; a phone that is not enrolled
		// has a row with an Einrichten button instead.
		var stillEnrolled bool
		b.eval(`[...document.querySelectorAll('#view button.device-row')].some((r) => r.textContent.includes(`+jsString(enrolledPhoneName)+`))`, &stillEnrolled)
		if !stillEnrolled {
			t.Error("cancelling the confirmation revoked the phone anyway: its row no longer opens " +
				"the phone sheet, so the console believes it is not enrolled")
		}
	})

	// The catalog sheet (FR-16). It is measured separately for the same reason the provisioning one
	// is — it is not in the DOM until it opens — and it is worth measuring because it holds the two
	// controls in the whole console that a phone lays out worst: a native `<input type="file">`,
	// whose button is drawn by the browser and ignores most of what the stylesheet asks for, and a
	// list whose rows carry a package name, a build number, a size and a minimum SDK on one line.
	// FR-18's card, on the screen it lives on. The layout is already covered by the apps-tab
	// measurement above; what this adds is that the card is populated at all — a fetch that failed
	// renders an empty list and a reassuring heading, which looks identical to "nothing is blocked"
	// and is the state a parent would most want to be told about.
	t.Run("family blocklist card", func(t *testing.T) {
		defer b.focus(t)()
		b.switchTab(t, "apps", "#view .list li")
		var blocklist struct {
			Heading string   `json:"heading"`
			Entries []string `json:"entries"`
			Notes   int      `json:"notes"`
		}
		b.eval(`(() => {
  const card = [...document.querySelectorAll('#view .card')]
    .find((c) => /für alle gesperrt/i.test(c.querySelector('h2')?.textContent || ''));
  if (!card) throw new Error('no family blocklist card on the apps view: ' +
    [...document.querySelectorAll('#view .card h2')].map((h) => h.textContent.trim()).join(' | '));
  return {
    heading: card.querySelector('h2').textContent.trim(),
    entries: [...card.querySelectorAll('li .label small')].map((s) => s.textContent.trim()),
    notes: document.querySelectorAll('#view .applist li .label small').length,
  };
})()`, &blocklist)
		if len(blocklist.Entries) == 0 {
			t.Error("the family blocklist card rendered with no entries; the curated set should be there")
		}
		var sawFacebook bool
		for _, e := range blocklist.Entries {
			if strings.Contains(e, "com.facebook.katana") {
				sawFacebook = true
			}
		}
		if !sawFacebook {
			t.Errorf("the card lists %v and none of it is the curated Facebook entry", blocklist.Entries)
		}

		// FR-18.6, rendered: the entry has to say what the PHONE reports, and the phone in this
		// seed reports com.facebook.katana as hidden. The state line is the whole answer to "is the
		// bloatware actually gone?", and it is the line that silently lied: the inventory request
		// filtered system apps, so this read "Not installed on any phone here." about a package
		// sitting on the phone in front of the parent. Asserting the true string rather than the
		// absence of the false one — a card that renders no state line at all would pass the
		// negative form.
		var state string
		b.eval(`(() => {
  const card = [...document.querySelectorAll('#view .card')]
    .find((c) => /für alle gesperrt/i.test(c.querySelector('h2')?.textContent || ''));
  const row = [...card.querySelectorAll('li')]
    .find((li) => /com\.facebook\.katana/.test(li.textContent));
  if (!row) throw new Error('no com.facebook.katana row in the blocklist card');
  return [...row.querySelectorAll('.label small')].map((s) => s.textContent.trim()).join(' ~ ');
})()`, &state)
		if !strings.Contains(state, "Auf dem Handy versteckt") {
			t.Errorf("the phone reports com.facebook.katana hidden and the blocklist card says %q; "+
				"a parent reading this cannot tell a block that worked from one that never reached "+
				"the phone", state)
		}
	})

	t.Run("app catalog sheet", func(t *testing.T) {
		defer b.focus(t)()
		b.switchTab(t, "apps", "#view .list li")
		b.waitFor(`[...document.querySelectorAll('#view button')]`+
			`.some((x) => /katalog|app hinzufügen/i.test(x.textContent))`,
			20*time.Second, "the catalog button on the apps view")
		b.eval(`(() => {
  const buttons = [...document.querySelectorAll('#view button')];
  const open = buttons.find((x) => /katalog|app hinzufügen/i.test(x.textContent));
  if (!open) throw new Error('no catalog button on the apps view: ' +
    buttons.map((x) => x.textContent.trim()).join(' | '));
  open.click();
})()`, nil)
		b.waitFor("document.getElementById('sheet').open", 20*time.Second, "the catalog sheet")
		b.waitFor("document.querySelector('#sheet-body input[type=file]') !== null",
			20*time.Second, "the upload control to render")

		b.measure(t, "app catalog sheet").check(t, "app catalog sheet")

		// The file input, named. `check` above already refuses a tap target under 44 px, but it
		// reports the offender by tag and class; a browser-drawn file picker is the one control most
		// likely to be it, and a failure that says "input.something is 32 px" is a slower read than
		// one that says the upload button is too small to hit.
		var upload struct {
			Height float64 `json:"height"`
			Right  float64 `json:"right"`
			Font   float64 `json:"font"`
		}
		b.eval(`(() => {
  const f = document.querySelector('#sheet-body input[type=file]');
  const r = f.getBoundingClientRect();
  return { height: r.height, right: r.right, font: parseFloat(getComputedStyle(f).fontSize) };
})()`, &upload)
		if upload.Height < minTapPx {
			t.Errorf("the APK file input is %.0f px tall, under the %.0f px this console promises; "+
				"picking a file is the first act of adding an app", upload.Height, minTapPx)
		}
		if upload.Right > phoneWidth+0.5 {
			t.Errorf("the APK file input reaches %.0f px on a %d px screen: the browser's own "+
				"'Choose file' button is drawn at its intrinsic width and has pushed it off",
				upload.Right, phoneWidth)
		}
		if upload.Font < minInputFontPx {
			t.Errorf("the APK file input's font is %.0f px, under %.0f: iOS Safari zooms the whole "+
				"page in on focus below that", upload.Font, minInputFontPx)
		}

		// The catalog list has to have something in it, or everything above measured an empty sheet.
		var rows int
		b.eval("document.querySelectorAll('#sheet-body .list li').length", &rows)
		if rows < 1 {
			t.Fatalf("the catalog sheet lists %d builds; the seed registered one, so this "+
				"measured a sheet with no content in it and the row layout is NOT MEASURED", rows)
		}

		b.eval("document.getElementById('sheet-close').click()", nil)
	})
}

// seedAFamilyWorthLookingAt fills the console with the content a layout has to survive: two
// children so the switcher is a real row, a phone that has reported apps and usage, blocked
// domains, a second parent, and — deliberately — the longest plausible values for the fields that
// are drawn as a single line. A layout guard against short strings measures the guard.
// seededFamily is what seedAFamilyWorthLookingAt made, for a caller that adds to it (the tour).
type seededFamily struct {
	parentToken, childID, deviceID, deviceToken string
}

func seedAFamilyWorthLookingAt(t *testing.T, h *harness) seededFamily {
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
			// The preinstall the blocklist exists for, in the shape a real Samsung reports it:
			// system_app, so it cannot be uninstalled, and hidden, because the phone has applied
			// the family entry. Both flags matter to the rendered check below — a console that
			// asks for the inventory without include_system=1 never sees this row at all, and the
			// blocklist card then reports the household's headline entry as not installed.
			{"package_name": "com.facebook.katana", "label": "Facebook", "system_app": true, "hidden": true},
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

	// A second phone, added and never set up. Two reasons. The QR sheet has to be measured on a
	// device that legitimately has a code to show — asking the enrolled one for a new code is the
	// destructive path now, and it is measured separately below. And the home view's two device
	// states, "waiting to be set up" and "reporting", are then both on screen at this width.
	h.newDevice(parent.Token, child.ID, "The spare phone")

	h.patchPolicy(parent.Token, child.ID, map[string]any{
		"bedtime_enabled": true, "bedtime_start": "21:00", "bedtime_end": "07:00",
		"daily_limit_minutes": 120, "youtube_blocked": true, "timezone": "Europe/Zurich",
	})
	for _, domain := range []string{"example.com", "a-domain-long-enough-to-need-wrapping.example.co.uk"} {
		h.call(http.MethodPost, "/children/"+child.ID+"/blocked-domains", parent.Token,
			map[string]any{"domain": domain}).expect(http.StatusCreated)
	}

	// A real application in the catalog, so the FR-16 card has a switch to draw and the row has a
	// package name long enough to be the one that overflows a 360 px column. The fixture is the same
	// APK the catalog suite registers — a hand-made row would have no size, no signer and no minimum
	// SDK, which are three of the four things the row prints.
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"),
		"?label=An+application+with+a+name+long+enough+to+need+the+ellipsis").
		expect(http.StatusCreated)
	h.call(http.MethodPut, "/children/"+child.ID+"/managed-apps/"+fixturePackage, parent.Token, nil).
		expect(http.StatusNoContent)

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
	if len(devices.Devices) != 2 {
		t.Fatalf("the seed left %d devices, expected 2 (one enrolled, one spare)", len(devices.Devices))
	}
	return seededFamily{parentToken: parent.Token, childID: child.ID, deviceID: device.ID, deviceToken: enrolled.DeviceToken}
}

// enrolledPhoneName is the device seedAFamilyWorthLookingAt enrolls. Named because two subtests
// have to tell it apart from the spare phone beside it, and a copy of the string in each is a way
// for one of them to keep passing after the seed changes.
const enrolledPhoneName = "Mira's phone — the blue one with the cracked screen"

// deviceCardButton builds a JS expression resolving to one button inside one device's card.
//
// Scoped to the card on purpose. `document.querySelectorAll('#view button')` finds the first match
// anywhere on the view, so with two phones on screen a test asking for "the set-up button" gets
// whichever card was drawn first — which is how the same expression can measure the harmless path
// on one run and the device-revoking one on the next.
//
// Matched on the card's HEADING, not its text. With more than one phone the home view draws a
// status strip above the cards, and that strip names every device — so a search by textContent
// finds the strip first, it holds no buttons, and the answer comes back as "this phone has no such
// button" for a card that is right there. Returns null rather than throwing, so it also works as a
// waitFor condition.
func deviceCardButton(deviceName, pattern string) string {
	return `(() => {
  const row = [...document.querySelectorAll('#view .device-row')]
    .find((r) => (r.querySelector('b')?.textContent || '').includes(` + jsString(deviceName) + `));
  if (!row) return null;
  return [...row.querySelectorAll('button')].find((x) => ` + pattern + `.test(x.textContent)) || null;
})()`
}

// openPhoneSheet taps a phone's row in Übersicht and waits for its sheet — where every fact, warning
// and command about one phone lives since the redesign of 2026-09-30.
func openPhoneSheet(t *testing.T, b *browser, deviceName string) {
	t.Helper()
	row := `[...document.querySelectorAll('#view button.device-row')].find((r) => (r.querySelector('b')?.textContent || '').includes(` + jsString(deviceName) + `))`
	b.waitFor(row+` !== undefined`, 20*time.Second, "the row of "+deviceName)
	b.eval(row+`.click()`, nil)
	b.waitFor(`document.getElementById('sheet').open && document.getElementById('sheet').dataset.kind === 'device' && `+
		`document.getElementById('sheet-title').textContent === `+jsString(deviceName), 10*time.Second, "the phone sheet of "+deviceName)
}

// jsString quotes a Go string as a JavaScript literal. json.Marshal is exactly right for this: JSON
// string syntax is a subset of JavaScript's, and it escapes the quotes and the non-ASCII dash in the
// device name above, which is where a hand-rolled version would break.
func jsString(v string) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out)
}
