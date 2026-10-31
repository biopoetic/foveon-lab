package main

import (
	"net/http"
	"os/exec"
	"runtime"
	"strings"
)

// pickFolder opens the operating system's own folder chooser and returns the
// chosen folder ("" if the dialog was cancelled). The in-app list is fine for
// stepping through drives, but the system dialog is what people mean by
// "browse to a folder": a tree of everything, network paths included.
func pickFolder() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// The classic tree dialog (drives, network places, new folder). It is
		// owned by an invisible topmost form: a dialog with no owner opens
		// behind the browser window, which is where it was landing.
		const script = `Add-Type -AssemblyName System.Windows.Forms | Out-Null
Add-Type -AssemblyName System.Drawing | Out-Null
$owner = New-Object System.Windows.Forms.Form
$owner.Text = 'Foveon Lab'
$owner.FormBorderStyle = 'None'
$owner.ShowInTaskbar = $false
$owner.TopMost = $true
$owner.Opacity = 0
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Show()
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Choose a photo folder'
$d.ShowNewFolderButton = $false
$ok = ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK)
$owner.Close()
if ($ok) { $d.SelectedPath }`
		out, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", script).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "darwin":
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose folder with prompt "Choose a photo folder")`).Output()
		if err != nil {
			if cancelled(err) {
				return "", nil
			}
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	default:
		out, err := exec.Command("zenity", "--file-selection", "--directory",
			"--title=Choose a photo folder").Output()
		if err != nil {
			if cancelled(err) {
				return "", nil
			}
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
}

// cancelled reports the exit error of a chooser the user dismissed, which is
// not an error worth showing.
func cancelled(err error) bool {
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	s := strings.ToLower(string(ee.Stderr))
	return strings.Contains(s, "cancel") || strings.Contains(s, "-128")
}

// pickFile opens the operating system's own file chooser for a picture the
// user already made (the other version in a blind A/B). "" if cancelled.
func pickFile() (string, error) {
	switch runtime.GOOS {
	case "windows":
		const script = `Add-Type -AssemblyName System.Windows.Forms | Out-Null
Add-Type -AssemblyName System.Drawing | Out-Null
$owner = New-Object System.Windows.Forms.Form
$owner.Text = 'Foveon Lab'
$owner.FormBorderStyle = 'None'
$owner.ShowInTaskbar = $false
$owner.TopMost = $true
$owner.Opacity = 0
$owner.StartPosition = 'CenterScreen'
$owner.Size = New-Object System.Drawing.Size(1, 1)
$owner.Show()
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = 'Choose the other version of this photo'
$d.Filter = 'Pictures|*.jpg;*.jpeg;*.tif;*.tiff;*.png|All files|*.*'
$d.CheckFileExists = $true
$d.Multiselect = $false
$ok = ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK)
$owner.Close()
if ($ok) { $d.FileName }`
		out, err := exec.Command("powershell.exe", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", script).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "darwin":
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose file with prompt "Choose the other version of this photo" of type {"public.jpeg", "public.tiff", "public.png"})`).Output()
		if err != nil {
			if cancelled(err) {
				return "", nil
			}
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	default:
		out, err := exec.Command("zenity", "--file-selection", "--file-filter=Pictures | *.jpg *.jpeg *.tif *.tiff *.png",
			"--title=Choose the other version of this photo").Output()
		if err != nil {
			if cancelled(err) {
				return "", nil
			}
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
}

// handlePickFile opens the system file chooser and returns its result.
func (s *server) handlePickFile(w http.ResponseWriter, r *http.Request) {
	path, err := pickFile()
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handlePickFolder opens the system folder chooser and returns its result.
func (s *server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	path, err := pickFolder()
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}
