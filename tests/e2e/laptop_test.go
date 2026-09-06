package e2e

// The other end of the 900 px breakpoint, measured in a browser.
//
// `#mainnav` and `#child-switcher` are single nodes that MOVE between the header row and the
// drawer as the viewport crosses 900 px (app.js `placeChrome`). Everything that guarded that
// arrangement measured the phone: TestConsoleRendersOnAPhone asserts the header holds no links and
// the drawer holds all five. Both statements can be true while the wide layout is broken, and they
// were — the console shipped with the header navigation AND a menu button that opens an empty
// drawer, because at this width the nodes are in the header and nothing is left to put in it.
//
// The rule this file adds is the one that binds the two halves together: **at any one width there
// is exactly one navigation, and it is the one on screen.** A control that opens the other one is
// a defect even when the other one is correctly empty.

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
    return cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0;
  };
  const nav = document.getElementById('mainnav');
  const kids = document.getElementById('child-switcher');
  return {
    navInHeader: document.getElementById('topbar-row').contains(nav),
    navInDrawer: document.getElementById('drawer-nav').contains(nav),
    kidsInHeader: document.getElementById('topbar-row').contains(kids),
    kidsInDrawer: document.getElementById('drawer-children').contains(kids),
    visibleTabs: [...document.querySelectorAll('.tab')].filter(shown).length,
    visiblePills: [...document.querySelectorAll('#child-switcher .pill')].filter(shown).length,
    menuVisible: shown(document.getElementById('menu-open')),
    menuDisplay: getComputedStyle(document.getElementById('menu-open')).display,
    signoutVisible: shown(document.getElementById('signout')),
    drawerOpen: document.getElementById('drawer').open,
  };
})()`

type chromePlacement struct {
	NavInHeader    bool   `json:"navInHeader"`
	NavInDrawer    bool   `json:"navInDrawer"`
	KidsInHeader   bool   `json:"kidsInHeader"`
	KidsInDrawer   bool   `json:"kidsInDrawer"`
	VisibleTabs    int    `json:"visibleTabs"`
	VisiblePills   int    `json:"visiblePills"`
	MenuVisible    bool   `json:"menuVisible"`
	MenuDisplay    string `json:"menuDisplay"`
	SignoutVisible bool   `json:"signoutVisible"`
	DrawerOpen     bool   `json:"drawerOpen"`
}

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

	var wide chromePlacement
	b.eval(chromeJS, &wide)

	if !wide.NavInHeader {
		t.Errorf("at %d px the navigation is not in the header row (in drawer: %v): the layout "+
			"this width is designed around never happened", laptopWidth, wide.NavInDrawer)
	}
	if wide.VisibleTabs != 5 {
		t.Errorf("at %d px %d of the 5 navigation links are visible in the header",
			laptopWidth, wide.VisibleTabs)
	}
	if !wide.KidsInHeader {
		t.Errorf("at %d px the child switcher is not in the header row (in drawer: %v)",
			laptopWidth, wide.KidsInDrawer)
	}
	if wide.VisiblePills < 3 {
		t.Errorf("at %d px %d child pills are visible; the fixture has two children and the "+
			"add button", laptopWidth, wide.VisiblePills)
	}
	if !wide.SignoutVisible {
		t.Errorf("at %d px there is no visible sign-out control: the drawer holds the other one "+
			"and the drawer is not reachable here", laptopWidth)
	}

	// The assertion the shipped console failed.
	//
	// `placeChrome` puts both nodes in the header at this width, so the drawer's two slots are
	// empty by construction — correctly so. A menu button offered anyway opens a blank panel, and
	// the parent's reading of it is not "this width has no drawer" but "the menu is broken". Both
	// halves of the report that started this were this one fact: an empty side menu, and a top
	// menu still there.
	//
	// It is stated as "not offered" rather than "display is none" because the cause was neither
	// obvious nor in app.js: `.menu-btn { display: none }` inside the 900 px media query was
	// overridden by `.btn-icon { display: inline-grid }` 200 lines further down the stylesheet —
	// equal specificity, later in the file, so the media query lost. A source-level guard over the
	// media query would have read as satisfied.
	if wide.MenuVisible {
		t.Errorf("at %d px the menu button is visible (computed display %q) while the navigation "+
			"it opens is in the header: tapping it opens an empty drawer. There must be exactly "+
			"one navigation at any width, and at this one it is the header",
			laptopWidth, wide.MenuDisplay)
	}

	// A negative control on the sentence above: the drawer's slots really are empty here, so the
	// button is not merely redundant — it is a control that shows nothing.
	var slots struct {
		Nav      int `json:"nav"`
		Children int `json:"children"`
	}
	b.eval(`({
  nav: document.getElementById('drawer-nav').childElementCount,
  children: document.getElementById('drawer-children').childElementCount,
})`, &slots)
	if slots.Nav != 0 || slots.Children != 0 {
		t.Errorf("at %d px the drawer slots hold %d nav and %d switcher node(s); the header "+
			"reported holding them too, so one of the two is a copy and they will drift",
			laptopWidth, slots.Nav, slots.Children)
	}

	b.measureAt(t, "laptop", laptopWidth).check(t, "laptop")

	// Crossing the breakpoint both ways, because the relocation is driven by a matchMedia `change`
	// listener and a listener that was never registered looks identical to a correct one until the
	// viewport moves. Narrow first: everything must end up in the drawer and the button must come
	// back.
	t.Run("narrowing to a phone moves the navigation into the drawer", func(t *testing.T) {
		defer b.focus(t)()
		b.phone(phoneWidth, phoneHeight)
		b.waitFor("document.getElementById('drawer-nav').contains(document.getElementById('mainnav'))",
			10*time.Second, "the navigation to move into the drawer")

		var narrow chromePlacement
		b.eval(chromeJS, &narrow)
		if !narrow.MenuVisible {
			t.Error("after narrowing to a phone there is no visible menu button, and the " +
				"navigation is now inside the drawer it opens: every screen is unreachable")
		}
		if narrow.VisibleTabs != 0 {
			t.Errorf("after narrowing, %d navigation link(s) are still visible with the drawer "+
				"shut", narrow.VisibleTabs)
		}
		if !narrow.KidsInDrawer {
			t.Error("after narrowing, the child switcher stayed in the header row")
		}
	})

	t.Run("widening back puts it in the header and takes the button away", func(t *testing.T) {
		defer b.focus(t)()
		// Opened first, so the widening has something to close. `placeChrome` closes the drawer on
		// the way wide for a reason a still picture cannot show: a parent who opens the menu on a
		// phone and rotates into a tablet layout would otherwise be left with a modal dialog whose
		// contents have just been moved out from under it.
		b.eval("document.getElementById('menu-open').click()", nil)
		b.waitFor("document.getElementById('drawer').open", 10*time.Second, "the drawer to open")

		b.laptop(laptopWidth, laptopHeight)
		b.waitFor("document.getElementById('topbar-row').contains(document.getElementById('mainnav'))",
			10*time.Second, "the navigation to move back into the header")

		var back chromePlacement
		b.eval(chromeJS, &back)
		if back.DrawerOpen {
			t.Error("widening past the breakpoint left the drawer open over the page, with its " +
				"navigation moved out into the header behind it")
		}
		if back.MenuVisible {
			t.Errorf("after widening the menu button is visible again (computed display %q)",
				back.MenuDisplay)
		}
		if back.VisibleTabs != 5 {
			t.Errorf("after widening, %d of the 5 links are visible in the header", back.VisibleTabs)
		}
	})
}
