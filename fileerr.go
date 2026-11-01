package main

// Plain words for the two ways a save usually fails: the disk is full, or the
// destination is not writable. Both are reported where the export is started,
// in the same sentence the user can act on, and both leave no half file
// behind (exportImage removes the partial file before returning).

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
)

// errDiskFull is Windows ERROR_DISK_FULL (112); ENOSPC covers the Unix side.
const errDiskFull = syscall.Errno(112)

func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, errDiskFull)
}

// plainFileError turns a write error into a sentence about what happened and
// what to do, keeping the original error for the log.
func plainFileError(err error, path string) error {
	if err == nil {
		return nil
	}
	if isDiskFull(err) {
		return fmt.Errorf("There is no space left on the disk. Nothing was replaced; free some space and export again (%w)", err)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("Foveon Lab cannot write to %s: the folder is read-only or permission was refused. Nothing was replaced (%w)", filepath.Dir(path), err)
	}
	return fmt.Errorf("Could not write %s: %w", filepath.Base(path), err)
}
