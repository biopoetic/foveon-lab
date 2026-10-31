//go:build !windows

package main

// renameNoReplace gives a copied file its final name, and never replaces a
// file that is already there: a hard link fails when the name exists, and
// the temporary name is removed after it succeeds.

import (
	"io/fs"
	"os"
)

func renameNoReplace(old, new string) error {
	if err := os.Link(old, new); err == nil {
		return os.Remove(old)
	}
	if _, statErr := os.Stat(new); statErr == nil {
		return fs.ErrExist
	}
	// A file system without hard links (some network and removable
	// volumes): rename only after the check above.
	return os.Rename(old, new)
}
