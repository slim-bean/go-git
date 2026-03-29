//go:build wasip1

package git

import (
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

func init() {
	fillSystemInfo = func(e *index.Entry, sys interface{}) {
		// WASI has limited syscall.Stat_t support; skip system info population.
	}
}

func isSymlinkWindowsNonAdmin(err error) bool {
	return false
}
