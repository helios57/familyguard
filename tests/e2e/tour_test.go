package e2e

// A picture of every console view, at a phone's width and a laptop's, for a person to look at.
//
// Not a check: nothing here can judge whether a screen is clear, and a test that claimed to would be
// the kind of green this suite exists to avoid. What it gives is the one thing the layout guards
// cannot — the rendered page — so a design review starts from what a parent sees rather than from
// the stylesheet. Run it with E2E_TOUR_DIR set to a directory; without it, it skips.
//
//	E2E_TOUR_DIR=/tmp/tour ./run.sh -run '^TestConsoleTour$'

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// screenshot writes the whole page — not just the viewport — as a PNG.
func (b *browser) screenshot(t *testing.T, path string) {
	t.Helper()
	var metrics struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(b.call("Page.getLayoutMetrics", nil), &metrics); err != nil {
		t.Fatalf("layout metrics: %v", err)
	}
	raw := b.call("Page.captureScreenshot", map[string]any{
		"format":                "png",
		"captureBeyondViewport": true,
		"clip": map[string]any{
			"x": 0, "y": 0, "scale": 1,
			"width":  metrics.CSSContentSize.Width,
			"height": metrics.CSSContentSize.Height,
		},
	})
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &shot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(png) == 0 {
		t.Fatalf("screenshot of %s came back empty: %v", path, err)
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleTour(t *testing.T) {
	dir := os.Getenv("E2E_TOUR_DIR")
	if dir == "" {
		t.Skip("E2E_TOUR_DIR is not set; the tour writes screenshots for a person and asserts nothing")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h, _ := catalogHarness(t)
	seedAFamilyWorthLookingAt(t, h)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.screenshot(t, filepath.Join(dir, "phone-00-signin.png"))
	b.laptop(1440, 900)
	b.screenshot(t, filepath.Join(dir, "laptop-00-signin.png"))

	b.phone(phoneWidth, phoneHeight)
	h.issuer.setNextLogin(primaryParent)
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#child-switcher .pill').length >= 2", 15*time.Second, "the children")

	screens := []struct{ tab, ready string }{
		{"guardian", "#view *"},
		{"home", "#view .card"},
		{"rules", "#view .switch"},
		{"apps", "#view .list li"},
		{"activity", "#view .card"},
		{"family", "#view .list li"},
	}
	for i, size := range []struct {
		name string
		set  func()
	}{
		{"phone", func() { b.phone(phoneWidth, phoneHeight) }},
		{"laptop", func() { b.laptop(1440, 900) }},
	} {
		size.set()
		for n, s := range screens {
			b.switchTab(t, s.tab, s.ready)
			// Let late fetches (usage, energy) land before the picture is taken.
			time.Sleep(1500 * time.Millisecond)
			b.eval("window.scrollTo(0, 0)", nil)
			b.screenshot(t, filepath.Join(dir, fmt.Sprintf("%s-%d%d-%s.png", size.name, i, n+1, s.tab)))
		}
	}
}
