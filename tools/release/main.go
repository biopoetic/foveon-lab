// Command release builds, signs and publishes a Foveon Lab release.
//
//	go run ./tools/release -version v0.1.0            # build + sign + package into dist/
//	go run ./tools/release -version v0.1.0 -publish   # …and create the GitHub release
//
// Signing runs locally because the keys never leave this machine:
//   - Windows: Authenticode via Azure Trusted Signing — STOCKCHAT_WINSIGN_CMD is the
//     full PowerShell signing command with an {exe} placeholder.
//   - macOS: rcodesign (no Mac needed) — STOCKCHAT_RCODESIGN, STOCKCHAT_MACOS_P12
//     [+ STOCKCHAT_MACOS_P12_PASSWORD], STOCKCHAT_NOTARY_API_KEY.
//
// It refuses to publish anything unsigned unless -allow-unsigned is given.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type target struct {
	goos, goarch, label string
}

var targets = []target{
	{"windows", "amd64", "windows-x64"},
	{"darwin", "arm64", "macos-apple-silicon"},
	{"darwin", "amd64", "macos-intel"},
	{"linux", "amd64", "linux-x64"},
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// signWindows Authenticode-signs exe. Azure's signing service occasionally
// hangs (a normal sign takes 10–130 s), so a run past 4 minutes is killed and
// retried once instead of waited out.
func signWindows(exe string) error {
	tmpl := os.Getenv("STOCKCHAT_WINSIGN_CMD")
	if tmpl == "" {
		return errors.New("STOCKCHAT_WINSIGN_CMD is not set (open a fresh terminal if it was just configured)")
	}
	cmdline := strings.ReplaceAll(tmpl, "{exe}", `"`+exe+`"`) + "; exit $LASTEXITCODE"
	for attempt := 1; attempt <= 2; attempt++ {
		log.Printf("authenticode sign %s (attempt %d)…", filepath.Base(exe), attempt)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", cmdline)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		timedOut := ctx.Err() != nil
		cancel()
		if err == nil {
			break
		}
		if !timedOut || attempt == 2 {
			return fmt.Errorf("signing failed: %w", err)
		}
		log.Printf("signing hung for 4 min — killed, retrying")
	}
	status, err := output("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`(Get-AuthenticodeSignature -LiteralPath '%s').Status`, exe))
	if err != nil || status != "Valid" {
		return fmt.Errorf("signature check: status %q (%v)", status, err)
	}
	log.Printf("signed OK: %s", filepath.Base(exe))
	return nil
}

// signMac signs (hardened runtime) and notarizes a darwin binary with rcodesign.
// A bare executable can't be stapled; Gatekeeper checks notarization online.
func signMac(bin string) error {
	rc, p12, key := os.Getenv("STOCKCHAT_RCODESIGN"), os.Getenv("STOCKCHAT_MACOS_P12"), os.Getenv("STOCKCHAT_NOTARY_API_KEY")
	if rc == "" || p12 == "" || key == "" {
		return errors.New("macOS signing needs STOCKCHAT_RCODESIGN, STOCKCHAT_MACOS_P12 and STOCKCHAT_NOTARY_API_KEY")
	}
	args := []string{"sign", "--p12-file", p12, "--code-signature-flags", "runtime"}
	if pw := os.Getenv("STOCKCHAT_MACOS_P12_PASSWORD"); pw != "" {
		args = append(args, "--p12-password", pw)
	}
	log.Printf("rcodesign sign %s…", bin)
	if err := run(rc, append(args, bin)...); err != nil {
		return fmt.Errorf("rcodesign sign: %w", err)
	}
	zipPath := bin + ".notary.zip"
	if err := zipFiles(zipPath, "", map[string]string{filepath.Base(bin): bin}); err != nil {
		return err
	}
	defer os.Remove(zipPath)
	log.Printf("notarizing %s (Apple, waiting up to 30 min)…", bin)
	// rcodesign's default 10-minute wait has cut short a submission Apple
	// accepted a few minutes later; give it half an hour.
	if err := run(rc, "notary-submit", "--api-key-file", key, "--wait", "--max-wait-seconds", "1800", zipPath); err != nil {
		return fmt.Errorf("notarization: %w", err)
	}
	log.Printf("signed + notarized OK: %s", bin)
	return nil
}

// zipFiles writes the given archive-name → disk-path entries under prefix/.
func zipFiles(dst, prefix string, files map[string]string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, src := range files {
		h := &zip.FileHeader{Name: filepath.ToSlash(filepath.Join(prefix, name)), Method: zip.Deflate, Modified: time.Now()}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if err := copyFile(w, src); err != nil {
			return err
		}
	}
	return zw.Close()
}

// tarGz packs files with the executable bit set on the binary — an archive
// made on Windows otherwise loses it and the program won't start on macOS/Linux.
func tarGz(dst, prefix string, files map[string]string, exe string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, src := range files {
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if name == exe {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: prefix + "/" + name, Mode: mode, Size: st.Size(), ModTime: st.ModTime(), Format: tar.FormatPAX}); err != nil {
			return err
		}
		if err := copyFile(tw, src); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func copyFile(w io.Writer, src string) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkPublishable makes sure the release is cut from pushed, clean main.
func checkPublishable(version string) error {
	if s, _ := output("git", "status", "--porcelain"); s != "" {
		return errors.New("working tree is not clean — commit first")
	}
	if b, _ := output("git", "rev-parse", "--abbrev-ref", "HEAD"); b != "main" {
		return fmt.Errorf("on branch %q, not main", b)
	}
	run("git", "fetch", "-q", "origin", "main")
	head, _ := output("git", "rev-parse", "HEAD")
	remote, _ := output("git", "rev-parse", "origin/main")
	if head != remote {
		return errors.New("HEAD is not pushed to origin/main — push first")
	}
	if _, err := output("gh", "release", "view", version); err == nil {
		return fmt.Errorf("release %s already exists", version)
	}
	return nil
}

func main() {
	version := flag.String("version", "", "release version, e.g. v0.1.0")
	publish := flag.Bool("publish", false, "create the GitHub release")
	allowUnsigned := flag.Bool("allow-unsigned", false, "continue (and allow publishing) if signing is not possible")
	flag.Parse()
	log.SetFlags(log.Ltime)
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(*version) {
		log.Fatal("-version must look like v0.1.0")
	}
	if *publish {
		if err := checkPublishable(*version); err != nil {
			log.Fatal(err)
		}
	}

	dist := filepath.Join("dist", *version)
	os.RemoveAll(dist)
	if err := os.MkdirAll(dist, 0o755); err != nil {
		log.Fatal(err)
	}
	var assets, unsigned []string
	for _, t := range targets {
		exe := "foveon-lab"
		if t.goos == "windows" {
			exe += ".exe"
		}
		bin := filepath.Join(dist, t.label, exe)
		log.Printf("build %s/%s", t.goos, t.goarch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+*version, "-o", bin, ".")
		cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("build %s: %v", t.label, err)
		}

		var err error
		switch t.goos {
		case "windows":
			err = signWindows(bin)
		case "darwin":
			err = signMac(bin)
		}
		if err != nil {
			if !*allowUnsigned {
				log.Fatalf("%s: %v — refusing to ship it unsigned (use -allow-unsigned to override)", t.label, err)
			}
			log.Printf("WARNING %s left UNSIGNED: %v", t.label, err)
			unsigned = append(unsigned, t.label)
		}

		name := fmt.Sprintf("foveon-lab-%s-%s", *version, t.label)
		files := map[string]string{exe: bin, "README.md": "README.md", "LICENSE": "LICENSE"}
		var archive string
		if t.goos == "windows" {
			archive = filepath.Join(dist, name+".zip")
			err = zipFiles(archive, name, files)
		} else {
			archive = filepath.Join(dist, name+".tar.gz")
			err = tarGz(archive, name, files, exe)
		}
		if err != nil {
			log.Fatalf("package %s: %v", t.label, err)
		}
		assets = append(assets, archive)
	}

	var sums strings.Builder
	for _, a := range assets {
		s, err := sha256File(a)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(&sums, "%s  %s\n", s, filepath.Base(a))
	}
	sumsPath := filepath.Join(dist, "SHA256SUMS.txt")
	if err := os.WriteFile(sumsPath, []byte(sums.String()), 0o644); err != nil {
		log.Fatal(err)
	}
	assets = append(assets, sumsPath)
	log.Printf("assets ready in %s", dist)
	if len(unsigned) > 0 {
		log.Printf("UNSIGNED: %v", unsigned)
	}
	if !*publish {
		return
	}

	notes := "Download the file for your system, unpack it and run `foveon-lab` (Windows: double-click `foveon-lab.exe`). It opens in your browser — see the README.\n\n" +
		"The Windows build is Authenticode-signed and the macOS builds are signed with a Developer ID and notarized by Apple."
	if len(unsigned) > 0 {
		notes += fmt.Sprintf("\n\nNote: %s build(s) in this release are NOT signed.", strings.Join(unsigned, ", "))
	}
	args := append([]string{"release", "create", *version, "--title", "Foveon Lab " + *version, "--notes", notes}, assets...)
	if err := run("gh", args...); err != nil {
		log.Fatalf("gh release create: %v", err)
	}
	log.Printf("published %s", *version)
}
