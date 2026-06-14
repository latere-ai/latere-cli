//go:build unix

package upgrade

import (
	"os"
	"syscall"
)

// reExec replaces the current process image with a fresh exec of the
// (now upgraded) binary, preserving the original arguments. The sentinel env
// var stops the new image from auto-upgrading again. On success it does not
// return.
func reExec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	env := append(os.Environ(), sentinelEnv+"=1")
	return syscall.Exec(exe, os.Args, env)
}
