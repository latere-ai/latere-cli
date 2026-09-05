// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// saveDownload publishes a streamed download only after the transfer succeeds
// and the output closes successfully. Scratch files stay beside the destination for rename
// atomicity; failed transfers leave the existing output intact.
func saveDownload(dest string, src io.Reader) error {
	// Resolve the parent before cleaning: link/../file follows the link before
	// walking up, which can differ from the lexical parent of the input.
	dir, base := filepath.Split(dest)
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	dest = filepath.Join(dir, base)
	info, err := os.Lstat(dest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		dest, err = filepath.EvalSymlinks(dest)
		if err != nil {
			return err
		}
		info, err = os.Stat(dest)
		if err != nil {
			return err
		}
	}
	if info != nil && !info.Mode().IsRegular() {
		return fmt.Errorf("download destination %q is not a regular file", dest)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".latere-download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if info != nil {
		if err := os.Chmod(f.Name(), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(f.Name(), dest)
}
