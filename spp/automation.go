package spp

import (
	"embed"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The PowerShell scripts that drive SPP's own UI to export calibration
// steps: pick a "[CAL] …" setting in its settings list, Ctrl+S, fill in the
// "Save Images As" dialog, wait for the TIFF. SPP has no command line or
// scripting interface, so this is the only way to automate it.
//
//go:embed automation/*.ps1
var automation embed.FS

// AutoExport exports photo (the file open in SPP's review window, e.g.
// "SDIM0260") once per calibration code into outDir as <photo>_<code>.tif.
// The [CAL] presets must already be in SPP's list, SPP must be open on that
// photo, and nobody may touch mouse or keyboard while it runs.
func AutoExport(photo string, codes []string, outDir string, log io.Writer) error {
	if runtime.GOOS != "windows" {
		return errors.New("automatic export drives Sigma Photo Pro, which is Windows-only here")
	}
	dir, err := os.MkdirTemp("", "foveon-spp-auto-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	entries, _ := automation.ReadDir("automation")
	for _, e := range entries {
		b, _ := automation.ReadFile("automation/" + e.Name())
		// PowerShell 5 reads BOM-less scripts as ANSI; the scripts are ASCII
		// except for the "…" in a menu name, so give them a UTF-8 BOM.
		if err := os.WriteFile(filepath.Join(dir, e.Name()), append([]byte{0xEF, 0xBB, 0xBF}, b...), 0o644); err != nil {
			return err
		}
	}
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
		filepath.Join(dir, "auto.ps1"), "-photo", photo, "-codes", strings.Join(codes, ","), "-outDir", outDir)
	cmd.Stdout, cmd.Stderr = log, log
	return cmd.Run()
}
