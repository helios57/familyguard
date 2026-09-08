//go:build !windows

package main

import (
	"fmt"
	"os"
)

// swap replaces self with the file at staged.
//
// On unix a rename over a RUNNING executable is fine: the kernel keeps the open inode alive for
// processes that already mapped it, and the name points at the new file immediately. So there is
// nothing to clean up afterwards, and the empty string says so.
func swap(self, staged string) (leftBehind string, err error) {
	if err := os.Rename(staged, self); err != nil {
		return "", fmt.Errorf("replacing %s: %w", self, err)
	}
	return "", nil
}
