package e2e

// The other end of the 900 px breakpoint, measured in a browser.
//
// Since the redesign of 2026-09-30 there is ONE navigation element, `#mainnav`, and app.css lays it
// out twice: a bar pinned to the bottom of a phone, and a sidebar from 900 px. Nothing moves it and
// nothing copies it. Before that, script moved the node between a header row and a drawer, and the
// console once shipped with the header navigation AND a menu button that opened an empty drawer —
// so the rule this file holds is still the one that binds the two widths together: **at any one
// width there is exactly one navigation, all of it on screen, and nothing that opens another.**

import (
	"testing"
	"time"
)

// A laptop, not a wide phone. 1280x800 is the smallest common notebook viewport and it is well
// clear of the breakpoint, so nothing here is measuring the boundary by accident; the boundary
// itself is crossed deliberately at the end of the test.
const (
	laptopWidth  = 1280
	laptopHeight = 800
)

// chromeJS reports where the navigation actually is and what is offered to reach it. Facts only —
// every judgement is made in Go, so changing what counts as broken changes this file and not a
// string inside a browser.
const chromeJS = `(() => {
  const shown = (e) => {
    if (!e) return false;
    const cs = getComputedStyle(e);
    const r = e.getBoundingClientRect();
    return cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0
      && r.right <= window.innerWidth + 0.5 && r.bottom <= window.innerHeight + 0.5 && r.left >= -0.5 && r.top >= -0.5;
  };
  const nav = document.getElementById('mainnav');
  const r = nav.getBoundingClientRect();
  const tabs = [...nav.querySelectorAll('.tab')].filter(shown).map((t) => t.getBoundingClientRect());
  return {
    navCount: document.querySelectorAll('nav#mainnav, .mainnav').length,
    tabCount: document.querySelectorAll('.tab').length,
    navLeft: r.left, navTop: r.top, navBottom: r.bottom, navWidth: r.width, navHeight: r.height,
    innerWidth: window.innerWidth, innerHeight: window.innerHeight,
    visibleTabs: tabs.length,
    tabsInARow: tabs.length > 1 && tabs.every((t) => Math.abs(t.top - tabs[0].top) < 1),
    tabsInAColumn: tabs.length > 1 && tabs.every((t) => Math.abs(t.left - tabs[0].left) < 1),
    visiblePills: [...document.querySelectorAll('#child-switcher .pill')].filter(shown).length,
    signoutVisible: shown(document.getElementById('signout')),
    // Any dialog open on its own is a menu nobody asked for.
    dialogOpen: [...document.querySelectorAll('dialog')].some((d) => d.open),
  };
})()`

type chromePlacement struct {
	NavCount       int     `json:"navCount"`
	TabCount       int     `json:"tabCount"`
	NavLeft        float64 `json:"navLeft"`
	NavTop         float64 `json:"navTop"`
	NavBottom      float64 `json:"navBottom"`
	NavWidth       float64 `json:"navWidth"`
	NavHeight      float64 `json:"navHeight"`
	InnerWidth     float64 `json:"innerWidth"`
	InnerHeight    float64 `json:"innerHeight"`
	VisibleTabs    int     `json:"visibleTabs"`
	TabsInARow     bool    `json:"tabsInARow"`
	TabsInAColumn  bool    `json:"tabsInAColumn"`
	VisiblePills   int     `json:"visiblePills"`
	SignoutVisible bool    `json:"signoutVisible"`
	DialogOpen     bool    `json:"dialogOpen"`
}

// oneNavigation is the rule this file exists for, checked at whatever width the page is.
func (c chromePlacement) oneNavigation(t *testing.T, width string) {
	t.Helper()
	if c.NavCount != 1 {
		t.Errorf("%s: %d navigation elements; there must be one, laid out per width, never a copy that drifts", width, c.NavCount)
	}
	if c.TabCount != consoleDestinations {
		t.Errorf("%s: %d destination links in the page, want %d — a second set is a second navigation", width, c.TabCount, consoleDestinations)
	}
	if c.VisibleTabs != consoleDestinations {
		t.Errorf("%s: %d of the %d destinations are on screen", width, c.VisibleTabs, consoleDestinations)
	}
	if c.DialogOpen {
		t.Errorf("%s: a dialog is open that nobody opened", width)
	}
}

// consoleDestinations is how many places the console's navigation leads to: Übersicht (which since
// 2026-09-30 is also the guardian window, FR-21), Regeln, Apps, Aktivität and Familie. A layout test
// that counted the links it could see against a different number would pass on a console that hid one.
const consoleDestinations = 5

func TestTheConsoleHasOneNavigationAtEveryWidth(t *testing.T) {
	h, _ := catalogHarness(t)
	seedAFamilyWorthLookingAt(t, h)

	b := startBrowser(t)
	b.laptop(laptopWidth, laptopHeight)
	b.navigate(h.base + "/")

	var bootType string
	b.eval("typeof boot", &bootType)
	if bootType != "function" {
		t.Fatalf("the console's JavaScript did not run: `typeof boot` is %q, want \"function\".\n%s",
			bootType, b.pageErrorReport())
	}
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")

	h.issuer.setNextLogin(primaryParent)
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#child-switcher .pill').length >= 2", 15*time.Second,
		"both children to appear in the switcher")

	// A page about one child, so the switcher is on screen too.
	b.eval("location.hash = '#/rules'", nil)
	b.waitFor("location.hash === '#/rules' && document.querySelector('#view .card') !== null", 20*time.Second, "Regeln")

	var wide chromePlacement
	b.eval(chromeJS, &wide)
	wide.oneNavigation(t, "laptop")
	// A sidebar: at the left edge, the height of the window, its links stacked.
	if wide.NavLeft > 0.5 || wide.NavHeight < wide.InnerHeight-0.5 || !wide.TabsInAColumn {
		t.Errorf("at %d px the navigation is at x=%.0f, %.0f px tall in a %.0f px window, links "+
			"stacked: %v — the sidebar this width is designed around never happened",
			laptopWidth, wide.NavLeft, wide.NavHeight, wide.InnerHeight, wide.TabsInAColumn)
	}
	if wide.VisiblePills < 3 {
		t.Errorf("at %d px %d child pills are visible; the fixture has two children and the "+
			"add button", laptopWidth, wide.VisiblePills)
	}
	if !wide.SignoutVisible {
		t.Errorf("at %d px there is no visible sign-out control", laptopWidth)
	}

	b.measureAt(t, "laptop", laptopWidth).check(t, "laptop")

	// Crossing the breakpoint both ways: the layout is a media query, and a query that never
	// applies looks identical to a correct one until the viewport moves.
	t.Run("narrowing to a phone turns the sidebar into the bottom bar", func(t *testing.T) {
		defer b.focus(t)()
		b.phone(phoneWidth, phoneHeight)
		b.waitFor("document.getElementById('mainnav').getBoundingClientRect().width <= window.innerWidth + 0.5 && "+
			"document.getElementById('mainnav').getBoundingClientRect().top > window.innerHeight / 2",
			10*time.Second, "the navigation to become a bottom bar")

		var narrow chromePlacement
		b.eval(chromeJS, &narrow)
		narrow.oneNavigation(t, "phone")
		if narrow.NavBottom < narrow.InnerHeight-0.5 || !narrow.TabsInARow {
			t.Errorf("after narrowing the navigation ends at %.0f in a %.0f px window, links in a row: %v",
				narrow.NavBottom, narrow.InnerHeight, narrow.TabsInARow)
		}
		if narrow.SignoutVisible {
			t.Error("after narrowing, sign-out is still in the header, where a phone has no room for it " +
				"(it is on Familie)")
		}
	})

	t.Run("widening back turns it into the sidebar again", func(t *testing.T) {
		defer b.focus(t)()
		b.laptop(laptopWidth, laptopHeight)
		b.waitFor("document.getElementById('mainnav').getBoundingClientRect().left <= 0.5 && "+
			"document.getElementById('mainnav').getBoundingClientRect().height >= window.innerHeight - 0.5",
			10*time.Second, "the navigation to become a sidebar again")
		var back chromePlacement
		b.eval(chromeJS, &back)
		back.oneNavigation(t, "laptop again")
		if !back.TabsInAColumn {
			t.Error("after widening, the links are not stacked in the sidebar")
		}
	})
}
