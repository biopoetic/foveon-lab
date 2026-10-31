//go:build !darwin

package main

// The Photos step exists only on macOS. Everywhere else the option is
// hidden, and a request that asks for it gets one plain sentence.

import "errors"

func photosAvailable() bool { return false }

func importToPhotosOSAScript(paths []string, album string) error {
	return errors.New("adding photos to Apple Photos is only available on macOS")
}
