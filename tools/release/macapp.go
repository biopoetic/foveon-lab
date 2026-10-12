package main

// macOS packaging: a universal, signed, notarized and stapled
// "Foveon Lab.app", which a GitHub Actions macOS runner then wraps in a DMG
// (hdiutil exists only on macOS); the DMG comes back here to be signed,
// notarized and stapled too. Signing keys never leave this machine.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
)

const appName = "Foveon Lab.app"

func infoPlist(version string) string {
	v := strings.TrimPrefix(version, "v")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Foveon Lab</string>
	<key>CFBundleDisplayName</key><string>Foveon Lab</string>
	<key>CFBundleIdentifier</key><string>com.algobox.foveonlab</string>
	<key>CFBundleExecutable</key><string>foveon-lab</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + v + `</string>
	<key>CFBundleVersion</key><string>` + v + `</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.photography</string>
	<key>NSHighResolutionCapable</key><true/>
	<!-- A web app: it runs in the background and lives in the browser. -->
	<key>LSUIElement</key><true/>
	<key>NSDesktopFolderUsageDescription</key><string>Foveon Lab looks for your X3F photos and SPP preset files on the Desktop.</string>
	<key>NSDocumentsFolderUsageDescription</key><string>Foveon Lab reads X3F photos and SPP preset files from folders you choose.</string>
	<key>NSDownloadsFolderUsageDescription</key><string>Foveon Lab reads X3F photos and SPP preset files from folders you choose.</string>
	<key>NSRemovableVolumesUsageDescription</key><string>Foveon Lab reads X3F photos straight from your camera's memory card.</string>
	<key>NSHumanReadableCopyright</key><string>© 2026 Saša Peršić. MIT License.</string>
</dict>
</plist>
`
}

// drawIcon draws the app icon: three offset translucent layers (blue,
// green, red top to bottom) on a dark rounded square — the Foveon sensor's
// stacked photodiodes.
func drawIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	inRound := func(x, y, x0, y0, x1, y1, r float64) bool {
		if x < x0 || x > x1 || y < y0 || y > y1 {
			return false
		}
		cx, cy := math.Max(x0+r, math.Min(x, x1-r)), math.Max(y0+r, math.Min(y, y1-r))
		return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
	}
	layers := []struct {
		c      color.NRGBA
		y0, y1 float64
		dx     float64
	}{
		{color.NRGBA{80, 150, 235, 230}, 0.26, 0.48, -0.05},
		{color.NRGBA{90, 205, 120, 230}, 0.40, 0.62, 0.0},
		{color.NRGBA{235, 90, 80, 230}, 0.54, 0.76, 0.05},
	}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			x, y := (float64(px)+0.5)/s, (float64(py)+0.5)/s
			if !inRound(x, y, 0.08, 0.08, 0.92, 0.92, 0.2) {
				continue
			}
			// Background: dark gradient.
			g := 30 + 18*(1-y)
			r, gg, b, a := g, g, g+4, 1.0
			for _, l := range layers {
				if inRound(x, y, 0.23+l.dx, l.y0, 0.77+l.dx, l.y1, 0.05) {
					al := float64(l.c.A) / 255
					r = r*(1-al) + float64(l.c.R)*al
					gg = gg*(1-al) + float64(l.c.G)*al
					b = b*(1-al) + float64(l.c.B)*al
				}
			}
			img.Set(px, py, color.NRGBA{uint8(r), uint8(gg), uint8(b), uint8(255 * a)})
		}
	}
	return img
}

// writeICNS writes an .icns with PNG entries at the standard sizes.
func writeICNS(path string) error {
	big := drawIcon(1024)
	entries := []struct {
		typ  string
		size int
	}{{"icp4", 16}, {"icp5", 32}, {"icp6", 64}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}}
	var body bytes.Buffer
	for _, e := range entries {
		img := image.Image(big)
		if e.size != 1024 {
			small := image.NewRGBA(image.Rect(0, 0, e.size, e.size))
			xdraw.CatmullRom.Scale(small, small.Bounds(), big, big.Bounds(), xdraw.Src, nil)
			img = small
		}
		var p bytes.Buffer
		if err := png.Encode(&p, img); err != nil {
			return err
		}
		body.WriteString(e.typ)
		binary.Write(&body, binary.BigEndian, uint32(8+p.Len()))
		body.Write(p.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// buildMacApp assembles, signs, notarizes and staples Foveon Lab.app from the
// per-architecture binaries, returning the app's path.
func buildMacApp(dist, version, fooArm, fooAmd, x3fArm, x3fAmd string) (string, error) {
	rc, p12, key := os.Getenv("STOCKCHAT_RCODESIGN"), os.Getenv("STOCKCHAT_MACOS_P12"), os.Getenv("STOCKCHAT_NOTARY_API_KEY")
	if rc == "" || p12 == "" || key == "" {
		return "", errors.New("macOS signing needs STOCKCHAT_RCODESIGN, STOCKCHAT_MACOS_P12 and STOCKCHAT_NOTARY_API_KEY")
	}
	root := filepath.Join(dist, "macos-app")
	os.RemoveAll(root)
	app := filepath.Join(root, appName)
	macos := filepath.Join(app, "Contents", "MacOS")
	res := filepath.Join(app, "Contents", "Resources")
	for _, d := range []string{macos, filepath.Join(res, "third_party", "x3fuse-core")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	// Universal (arm64 + x86_64) binaries.
	if err := run(rc, "macho-universal-create", "--output", filepath.Join(macos, "foveon-lab"), fooArm, fooAmd); err != nil {
		return "", fmt.Errorf("universal foveon-lab: %w", err)
	}
	if err := run(rc, "macho-universal-create", "--output", filepath.Join(macos, "x3f_extract"), x3fArm, x3fAmd); err != nil {
		return "", fmt.Errorf("universal x3f_extract: %w", err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(infoPlist(version)), 0o644); err != nil {
		return "", err
	}
	if err := writeICNS(filepath.Join(res, "AppIcon.icns")); err != nil {
		return "", err
	}
	for dst, src := range map[string]string{"README.md": "README.md", "LICENSE": "LICENSE"} {
		if err := copyTo(src, filepath.Join(res, dst)); err != nil {
			return "", err
		}
	}
	for _, f := range []string{"LICENSE", "NOTICE", "README.md", "fix-type2-camf-key.patch"} {
		if err := copyTo(filepath.Join(thirdPartyDir, f), filepath.Join(res, "third_party", "x3fuse-core", f)); err != nil {
			return "", err
		}
	}

	args := []string{"sign", "--p12-file", p12,
		"--code-signature-flags", "runtime",
		"--code-signature-flags", "Contents/MacOS/x3f_extract:runtime"}
	if pw := os.Getenv("STOCKCHAT_MACOS_P12_PASSWORD"); pw != "" {
		args = append(args, "--p12-password", pw)
	}
	log.Printf("rcodesign sign %s…", appName)
	if err := run(rc, append(args, app)...); err != nil {
		return "", fmt.Errorf("sign app: %w", err)
	}
	log.Printf("notarizing + stapling %s (Apple, waiting up to 30 min)…", appName)
	if err := run(rc, "notary-submit", "--api-key-file", key, "--staple", "--max-wait-seconds", "1800", app); err != nil {
		return "", fmt.Errorf("notarize app: %w", err)
	}
	return app, nil
}

// zipApp zips the .app keeping executable bits (ditto -x -k honours them).
func zipApp(app, dst string) error {
	files := map[string]string{}
	base := filepath.Dir(app)
	err := filepath.Walk(app, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(base, p)
		files[filepath.ToSlash(rel)] = p
		return nil
	})
	if err != nil {
		return err
	}
	return zipFiles(dst, "", files)
}

// buildDMG has the mac-dmg workflow wrap the stapled, zipped app in a DMG (and check
// it with Apple's own codesign/stapler/spctl), then signs, notarizes and
// staples the DMG here.
func buildDMG(version, dist, zipPath string) (string, error) {
	tag := fmt.Sprintf("macapp-%s-%d", version, time.Now().Unix())
	if err := run("gh", "release", "create", tag, zipPath, "--draft", "--title", "temp: "+tag, "--notes", "Temporary: input for the mac-dmg workflow. Deleted automatically."); err != nil {
		return "", fmt.Errorf("upload app for DMG build: %w", err)
	}
	defer run("gh", "release", "delete", tag, "--yes", "--cleanup-tag")

	started := time.Now().UTC().Add(-10 * time.Second)
	if err := run("gh", "workflow", "run", "mac-dmg.yml", "-f", "release="+tag, "-f", "version="+version); err != nil {
		return "", fmt.Errorf("start mac-dmg workflow: %w", err)
	}
	var runID string
	for i := 0; i < 30 && runID == ""; i++ {
		time.Sleep(4 * time.Second)
		out, _ := output("gh", "run", "list", "--workflow", "mac-dmg.yml", "--limit", "1", "--json", "databaseId,createdAt", "--jq", ".[0] | \"\\(.databaseId) \\(.createdAt)\"")
		if f := strings.Fields(out); len(f) == 2 {
			if t, err := time.Parse(time.RFC3339, f[1]); err == nil && t.After(started) {
				runID = f[0]
			}
		}
	}
	if runID == "" {
		return "", errors.New("mac-dmg workflow run did not appear")
	}
	log.Printf("waiting for mac-dmg run %s…", runID)
	if err := run("gh", "run", "watch", runID, "--exit-status"); err != nil {
		return "", fmt.Errorf("mac-dmg workflow failed (gh run view %s --log-failed): %w", runID, err)
	}
	dl := filepath.Join(dist, "dmg-download")
	os.RemoveAll(dl)
	if err := run("gh", "run", "download", runID, "-n", "foveon-lab-dmg", "-D", dl); err != nil {
		return "", err
	}
	name := fmt.Sprintf("Foveon-Lab-%s.dmg", version)
	dmg := filepath.Join(dist, name)
	if err := os.Rename(filepath.Join(dl, name), dmg); err != nil {
		return "", err
	}

	rc, p12, key := os.Getenv("STOCKCHAT_RCODESIGN"), os.Getenv("STOCKCHAT_MACOS_P12"), os.Getenv("STOCKCHAT_NOTARY_API_KEY")
	args := []string{"sign", "--p12-file", p12}
	if pw := os.Getenv("STOCKCHAT_MACOS_P12_PASSWORD"); pw != "" {
		args = append(args, "--p12-password", pw)
	}
	if err := run(rc, append(args, dmg)...); err != nil {
		return "", fmt.Errorf("sign DMG: %w", err)
	}
	log.Printf("notarizing + stapling %s…", name)
	if err := run(rc, "notary-submit", "--api-key-file", key, "--staple", "--max-wait-seconds", "1800", dmg); err != nil {
		return "", fmt.Errorf("notarize DMG: %w", err)
	}
	return dmg, nil
}
