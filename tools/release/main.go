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
//
// A release is cut from a branch whose HEAD is pushed, by default main:
// pass -branch to release from a release branch without merging it first.
// The git tag is created on the released commit, not on the default branch.
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

// tarGz packs files with the executable bit set on the binaries — an archive
// made on Windows otherwise loses it and the program won't start on macOS/Linux.
func tarGz(dst, prefix string, files map[string]string, exes ...string) error {
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
		for _, e := range exes {
			if name == e {
				mode = 0o755
			}
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

// thirdPartyDir holds x3fuse-core's license, notice and the Foveon Lab patch.
const thirdPartyDir = "third_party/x3fuse-core"

// x3fExtract returns the patched x3f_extract for a target: the local Windows
// build, or the macOS/Linux build from the latest successful x3f-extract
// workflow run (downloaded once into dl).
func x3fExtract(t target, dl string, runID *string) (string, error) {
	if t.goos == "windows" {
		p := filepath.Join(thirdPartyDir, "bin", "x3f_extract.exe")
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s missing — build it (see %s/README.md)", p, thirdPartyDir)
		}
		return p, nil
	}
	name := map[string]string{"darwin/arm64": "macos-arm64", "darwin/amd64": "macos-x86_64", "linux/amd64": "linux-x86_64"}[t.goos+"/"+t.goarch]
	if name == "" {
		return "", fmt.Errorf("no x3f_extract build for %s/%s", t.goos, t.goarch)
	}
	if *runID == "" {
		id, err := output("gh", "run", "list", "--workflow", "x3f-extract.yml", "--status", "success", "--limit", "1", "--json", "databaseId", "--jq", ".[0].databaseId")
		if err != nil || id == "" {
			return "", errors.New("no successful x3f-extract workflow run — push third_party changes or run the workflow first")
		}
		*runID = id
	}
	art := "x3f_extract-" + name
	dst := filepath.Join(dl, art)
	if _, err := os.Stat(filepath.Join(dst, art)); err != nil {
		if err := run("gh", "run", "download", *runID, "-n", art, "-D", dst); err != nil {
			return "", fmt.Errorf("download %s: %w", art, err)
		}
	}
	return filepath.Join(dst, art), nil
}

// copyTo copies src to dst (signing rewrites binaries in place, and the
// downloaded/local originals must stay untouched).
func copyTo(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := copyFile(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// checkPublishable makes sure the release is cut from a clean working tree
// whose HEAD is pushed to the branch being released.
func checkPublishable(version, branch string) error {
	if s, _ := output("git", "status", "--porcelain"); s != "" {
		return errors.New("working tree is not clean — commit first")
	}
	if b, _ := output("git", "rev-parse", "--abbrev-ref", "HEAD"); b != branch {
		return fmt.Errorf("on branch %q, not %s", b, branch)
	}
	// The release is cut from a commit that exists on the remote, so the
	// tagged source can always be fetched again. That commit need not be on
	// main: a release branch is a normal way to ship without merging.
	run("git", "fetch", "-q", "origin", branch)
	head, _ := output("git", "rev-parse", "HEAD")
	remote, _ := output("git", "rev-parse", "origin/"+branch)
	if head != remote {
		return fmt.Errorf("HEAD is not pushed to origin/%s — push first", branch)
	}
	if _, err := output("gh", "release", "view", version); err == nil {
		return fmt.Errorf("release %s already exists", version)
	}
	return nil
}

// mustOutput is output for a value already proven available.
func mustOutput(name string, args ...string) string {
	s, _ := output(name, args...)
	return s
}

func main() {
	version := flag.String("version", "", "release version, e.g. v0.1.0")
	publish := flag.Bool("publish", false, "create the GitHub release")
	allowUnsigned := flag.Bool("allow-unsigned", false, "continue (and allow publishing) if signing is not possible")
	notesFlag := flag.String("notes", "", "one-line summary shown in the app's update notice (manifest notes)")
	notesFile := flag.String("notes-file", "", "Markdown \"What's new\": shown in the app once after updating (signed manifest) and put at the top of the GitHub release notes")
	skipX3F := flag.Bool("skip-x3f-extract", false, "ship without x3f_extract (the app decodes every body natively since v0.4.2; x3f_extract is then only a test reference)")
	skipDMG := flag.Bool("skip-dmg", false, "do not build the macOS .app/.dmg")
	branch := flag.String("branch", "main", "branch to release from (its HEAD must be pushed to origin)")
	checkOnly := flag.Bool("check", false, "only run the publish preflight (branch, clean tree, pushed, tag free) and exit")
	genKey := flag.Bool("gen-update-key", false, "create the self-update signing key (once) and print its public key")
	icoOut := flag.String("write-ico", "", "only write the Windows icon (.ico) to this path")
	sysoOut := flag.String("write-syso", "", "only write the Windows icon resource (.syso) to this path")
	flag.Parse()
	whatsNew := ""
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			log.Fatal(err)
		}
		whatsNew = strings.TrimSpace(string(b))
	}
	if *sysoOut != "" {
		if err := writeSyso(*sysoOut); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *icoOut != "" {
		if err := writeICO(*icoOut); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *genKey {
		if err := genUpdateKey(); err != nil {
			log.Fatal(err)
		}
		return
	}
	var x3fRun string
	log.SetFlags(log.Ltime)
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(*version) {
		log.Fatal("-version must look like v0.1.0")
	}
	if *publish || *checkOnly {
		if err := checkPublishable(*version, *branch); err != nil {
			log.Fatal(err)
		}
		if *checkOnly {
			log.Printf("%s could be released from %s (HEAD %s)", *version, *branch,
				mustOutput("git", "rev-parse", "--short", "HEAD"))
			return
		}
		// Fail now, not after half an hour of signing.
		if _, err := loadUpdateKey(); err != nil {
			log.Fatal(err)
		}
	}

	dist := filepath.Join("dist", *version)
	os.RemoveAll(dist)
	if err := os.MkdirAll(dist, 0o755); err != nil {
		log.Fatal(err)
	}
	var assets, unsigned []string
	updates := map[string]string{} // self-update platform key → asset
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
		if t.goos == "windows" {
			ico := filepath.Join(dist, t.label, "foveon-lab.ico")
			if err := writeICO(ico); err != nil {
				log.Fatal(err)
			}
			files["foveon-lab.ico"] = ico
		}

		// The patched x3fuse-core x3f_extract (SD9/SD10 raw decoding), signed
		// like foveon-lab itself, plus its license, notice and the patch.
		x3fName := "x3f_extract"
		if t.goos == "windows" {
			x3fName += ".exe"
		}
		// The raw decoder and pipeline are Go ports of x3fuse-core: its
		// licence and notice ship either way.
		for _, f := range []string{"LICENSE", "NOTICE"} {
			files["third_party/x3fuse-core/"+f] = filepath.Join(thirdPartyDir, f)
		}
		if *skipX3F {
			log.Printf("%s: shipping WITHOUT x3f_extract (-skip-x3f-extract)", t.label)
		} else {
			src, err := x3fExtract(t, filepath.Join("dist", "x3f-extract-artifacts"), &x3fRun)
			if err != nil {
				log.Fatalf("%s: x3f_extract: %v (use -skip-x3f-extract to ship without it)", t.label, err)
			}
			x3fBin := filepath.Join(dist, t.label, x3fName)
			if err := copyTo(src, x3fBin); err != nil {
				log.Fatal(err)
			}
			switch t.goos {
			case "windows":
				err = signWindows(x3fBin)
			case "darwin":
				err = signMac(x3fBin)
			}
			if err != nil {
				if !*allowUnsigned {
					log.Fatalf("%s x3f_extract: %v — refusing to ship it unsigned", t.label, err)
				}
				unsigned = append(unsigned, t.label+" x3f_extract")
			}
			files[x3fName] = x3fBin
			for _, f := range []string{"README.md", "fix-type2-camf-key.patch"} {
				files["third_party/x3fuse-core/"+f] = filepath.Join(thirdPartyDir, f)
			}
		}

		var archive string
		if t.goos == "windows" {
			archive = filepath.Join(dist, name+".zip")
			err = zipFiles(archive, name, files)
		} else {
			archive = filepath.Join(dist, name+".tar.gz")
			err = tarGz(archive, name, files, exe, x3fName)
		}
		if err != nil {
			log.Fatalf("package %s: %v", t.label, err)
		}
		assets = append(assets, archive)
		updates[t.goos+"-"+t.goarch] = archive
	}

	// The Mac download people should use: a universal, stapled app in a DMG.
	// A bare CLI binary is refused by Gatekeeper when started from Finder.
	if *skipDMG {
		log.Printf("skipping the macOS DMG (-skip-dmg)")
	} else if len(unsigned) > 0 {
		log.Printf("skipping the macOS DMG: it needs signed binaries")
	} else {
		mac := func(label, f string) string { return filepath.Join(dist, label, f) }
		x3fArm, x3fAmd := mac("macos-apple-silicon", "x3f_extract"), mac("macos-intel", "x3f_extract")
		if *skipX3F {
			x3fArm, x3fAmd = "", ""
		}
		app, err := buildMacApp(dist, *version,
			mac("macos-apple-silicon", "foveon-lab"), mac("macos-intel", "foveon-lab"), x3fArm, x3fAmd)
		if err != nil {
			log.Fatalf("macOS app: %v", err)
		}
		appZip := filepath.Join(dist, fmt.Sprintf("Foveon-Lab-%s-macos-app.zip", *version))
		if err := zipApp(app, appZip); err != nil {
			log.Fatal(err)
		}
		dmg, err := buildDMG(*version, dist, appZip)
		if err != nil {
			log.Fatalf("macOS DMG: %v", err)
		}
		assets = append([]string{dmg}, assets...)
		assets = append(assets, appZip) // what the installed app updates itself from
		updates["darwin-app"] = appZip
	}

	if len(unsigned) == 0 {
		mf := filepath.Join(dist, "update-manifest.json")
		if err := writeManifest(mf, *version, *notesFlag, whatsNew, updates); err != nil {
			if *publish {
				log.Fatalf("update manifest: %v", err)
			}
			log.Printf("no update manifest: %v", err)
		} else {
			assets = append(assets, mf)
		}
	} else {
		log.Printf("no update manifest: unsigned builds are never offered as updates")
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

	notes := "**macOS:** download the `.dmg`, open it and drag Foveon Lab to Applications (one app for Apple Silicon and Intel).\n" +
		"**Windows:** unpack the zip and double-click `foveon-lab.exe`. **Linux:** unpack and run `./foveon-lab`.\n" +
		"It opens in your browser — see the README. The `.tar.gz` macOS archives are for command-line use.\n\n" +
		"The Windows build is Authenticode-signed and the macOS builds are signed with a Developer ID and notarized by Apple."
	if len(unsigned) > 0 {
		notes += fmt.Sprintf("\n\nNote: %s build(s) in this release are NOT signed.", strings.Join(unsigned, ", "))
	}
	if whatsNew != "" {
		notes = whatsNew + "\n\n---\n\n" + notes
	}
	// Tag the commit actually being released. Without --target, gh tags the
	// default branch, which on a release branch would point somewhere else.
	head, err := output("git", "rev-parse", "HEAD")
	if err != nil {
		log.Fatalf("cannot resolve HEAD: %v", err)
	}
	args := append([]string{"release", "create", *version, "--target", head,
		"--title", "Foveon Lab " + *version, "--notes", notes}, assets...)
	if err := run("gh", args...); err != nil {
		log.Fatalf("gh release create: %v", err)
	}
	log.Printf("published %s", *version)
}
