//go:build darwin

package main

// Add finished exports to Apple Photos through osascript. The first run
// makes macOS ask whether Foveon Lab may control Photos; a refusal becomes
// one clear sentence (photosRefusal).

import (
	"fmt"
	"os/exec"
	"strings"
)

const photosScript = `on run argv
  set albumName to item 1 of argv
  set thePaths to {}
  repeat with i from 2 to count of argv
    set end of thePaths to (POSIX file (item i of argv))
  end repeat
  tell application "Photos"
    activate
    if not (exists album albumName) then
      make new album named albumName
    end if
    set theAlbum to album albumName
    import thePaths into theAlbum skip check duplicates true
  end tell
end run`

func photosAvailable() bool { return true }

func importToPhotosOSAScript(paths []string, album string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-e", photosScript, album}, paths...)
	out, err := exec.Command("osascript", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.ToLower(string(out))
	if strings.Contains(text, "-1743") || strings.Contains(text, "not authorized") ||
		strings.Contains(text, "not allowed") || strings.Contains(text, "automation") {
		return photosRefusal(err)
	}
	return fmt.Errorf("Photos did not take the files: %s", strings.TrimSpace(string(out)))
}
