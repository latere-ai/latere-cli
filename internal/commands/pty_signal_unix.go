//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package commands

import (
	"os"
	"syscall"
)

func resizeSignals() []os.Signal {
	return []os.Signal{syscall.SIGWINCH}
}
