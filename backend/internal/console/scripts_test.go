package console

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// consoleScript is every script the console ships, joined in name order, read from the embedded FS.
//
// The console is six files since the redesign of 2026-09-30 (app.js and one per view). A check that
// reads app.js alone would pass over a feature that moved to overview.js — the button is still
// there, the scan simply stopped looking — which is the shape of green this suite exists to refuse.
// So every source check reads all of them, and fails when there are none.
func consoleScript(t *testing.T) string {
	t.Helper()
	var names []string
	err := fs.WalkDir(assets, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && path.Ext(p) == ".js" {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("could not list the embedded console: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("the embedded console holds %d script(s) %v: this check would scan almost nothing", len(names), names)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		raw, err := assets.ReadFile(n)
		if err != nil {
			t.Fatalf("could not read %s: %v", n, err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}
