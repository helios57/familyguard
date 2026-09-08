//go:build windows

package main

import (
	"fmt"
	"os"
)

// swap replaces self with the file at staged.
//
// Windows will not let a running executable be deleted or overwritten, so the running file is first
// moved aside -- which IS permitted, because the handle follows the file rather than the name --
// and only then is the new one put in its place. The moved-aside copy usually cannot be deleted
// until this process exits, so its path is returned for the caller to mention rather than silently
// leaked.
func swap(self, staged string) (leftBehind string, err error) {
	old := self + ".old"
	// A previous update's leftover would make the rename below fail, so clear it first. It is
	// removable now precisely because it is no longer running.
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		return "", fmt.Errorf("moving the running binary aside (%s): %w", self, err)
	}
	if err := os.Rename(staged, self); err != nil {
		// Put it back rather than leaving the machine with no fgctl at all.
		if restoreErr := os.Rename(old, self); restoreErr != nil {
			return "", fmt.Errorf("installing %s failed (%w) AND restoring the original failed "+
				"(%v); the previous binary is at %s", self, err, restoreErr, old)
		}
		return "", fmt.Errorf("installing %s: %w", self, err)
	}
	// Try once; it is expected to fail while this process is running.
	if os.Remove(old) == nil {
		return "", nil
	}
	return old, nil
}
