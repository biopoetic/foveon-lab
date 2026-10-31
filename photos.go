package main

// The Photos hand-off. On macOS a finished batch can be added to Apple
// Photos, which then syncs it with iCloud Photos itself. The step is behind
// a build tag and a variable so a test can replace the real importer with a
// fake one. It was never run on a Mac (see REPORT-16.md).

import (
	"errors"
	"time"
)

// photosImport adds the finished files to an album in Apple Photos.
var photosImport = importToPhotosOSAScript

// photosSupported is false everywhere but macOS. A test sets it to true with
// a fake importer.
var photosSupported = photosAvailable()

// photosAlbumName names the album by the day the batch was made.
func photosAlbumName(t time.Time) string {
	return "Foveon Lab " + t.Format("2006-01-02")
}

// photosRefusal turns the macOS automation refusal into one plain sentence.
func photosRefusal(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("macOS did not allow Foveon Lab to add photos to Photos. Allow it in System Settings › Privacy & Security › Automation, then try again.")
}
