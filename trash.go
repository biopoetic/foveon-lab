package main

// Deleting a photo moves it to the system's Recycle Bin / Trash, never a
// permanent delete: a slip is always recoverable. The path goes to the
// helper as an environment variable or argument, never inside a script.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

// trashFile moves path to the Recycle Bin / Trash (a variable so tests
// don't fill the real one).
var trashFile = moveToTrash

func moveToTrash(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			`Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile($env:FOVEON_TRASH, 'OnlyErrorDialogs', 'SendToRecycleBin')`)
		cmd.Env = append(os.Environ(), "FOVEON_TRASH="+path)
	case "darwin":
		cmd = exec.Command("osascript", "-e", "on run argv", "-e",
			`tell application "Finder" to delete (POSIX file (item 1 of argv))`, "-e", "end run", path)
	default:
		if _, err := exec.LookPath("gio"); err != nil {
			return errors.New("no trash available (gio not found); not deleting")
		}
		cmd = exec.Command("gio", "trash", path)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(err.Error() + ": " + string(out))
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("the file is still there")
	}
	return nil
}

// handleDelete moves a photo to the Recycle Bin / Trash (POST {"id"}) and
// drops it from the list and the favourites.
func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil {
		httpErr(w, 400, errors.New("POST {id}"))
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	if err := trashFile(ph.path); err != nil {
		httpErr(w, 500, err)
		return
	}
	s.photoMu.Lock()
	delete(s.byID, ph.ID)
	for i, p := range s.photos {
		if p == ph {
			s.photos = append(s.photos[:i:i], s.photos[i+1:]...)
			break
		}
	}
	s.photoMu.Unlock()
	if ph.Favorite {
		cfgMu.Lock()
		if c, ok := loadConfig(); ok {
			var keep []string
			for _, f := range c.Favorites {
				if f != ph.path {
					keep = append(keep, f)
				}
			}
			c.Favorites = keep
			saveConfig(c)
		}
		cfgMu.Unlock()
	}
	writeJSON(w, map[string]string{"deleted": ph.Name})
}
