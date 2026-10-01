package e2e

// A page that is slow to load says so, instead of leaving the previous page standing under the new
// tab (or, on first load, nothing at all). Measured by delaying every request the page makes: the
// console looks `fetch` up when it calls it, so wrapping it in the page is the network being slow,
// not a stub of the console.

import (
	"testing"
	"time"
)

func TestASlowPageSaysItIsLoading(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	h.newChild(primary.Token, "Mira")
	b := signInBrowser(t, h, primaryParent)
	b.waitFor(`!!document.querySelector('#view .child-card')`, 15*time.Second, "the overview")

	b.eval(`(() => {
	  const real = window.fetch;
	  window.fetch = (...a) => new Promise((r) => setTimeout(r, 1500)).then(() => real(...a));
	})()`, nil)

	// The same page refreshed in the background keeps what it shows: nothing to announce.
	// Started, not awaited: eval waits for a returned promise, and by the time this one settles the
	// window being watched is over (measured: awaiting it left this check green with the guard gone).
	b.eval(`setTimeout(refresh, 0), true`, nil)
	for i := 0; i < 14; i++ {
		time.Sleep(100 * time.Millisecond)
		var blanked bool
		b.eval(`!!document.querySelector('#view .view-loading') || !document.querySelector('#view .child-card')`, &blanked)
		if blanked {
			t.Errorf("a background refresh of the page on screen replaced it with a loading state (after %d ms)", (i+1)*100)
			break
		}
	}

	// A different page: the loading state, busy for assistive technology, then the page.
	b.eval(`document.querySelector('.tab[data-tab="apps"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .view-loading')`, time.Second, "the loading state on a slow page")
	var busy string
	b.eval(`document.getElementById('view').getAttribute('aria-busy') || ''`, &busy)
	if busy != "true" {
		t.Errorf("the view is loading but aria-busy is %q", busy)
	}
	b.waitFor(`!document.querySelector('#view .view-loading') && !!document.querySelector('#view .card')`, 10*time.Second, "the page itself")
	b.eval(`document.getElementById('view').getAttribute('aria-busy') || ''`, &busy)
	if busy != "" {
		t.Errorf("the page is drawn but still marked aria-busy=%q", busy)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the page complained: %s", b.pageErrorReport())
	}
}
