//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package commands

import "os"

func resizeSignals() []os.Signal {
	return nil
}
