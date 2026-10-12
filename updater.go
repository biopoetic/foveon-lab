package main

// Self-update. Each GitHub release carries update-manifest.json: a payload
// (version + per-platform asset name, size and SHA-256) signed with an
// Ed25519 key that only the maintainer holds; the public half is below. The
// app checks the latest release now and then, the UI offers "Update &
// restart", and the app then downloads the asset, checks size + hash,
// swaps its files in place and restarts itself on the same port, so the
// open browser tab simply reloads.
//
// Swapping: on Windows a running .exe can't be overwritten but can be
// renamed, so the old one is moved to *.old (deleted on the next start); on
// macOS/Linux the new file is renamed over the old one. The macOS .app is
// replaced as a whole bundle (it is signed, notarized and stapled, and a
// file downloaded by the app itself gets no quarantine flag).

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// updatePublicKeyB64 verifies update manifests (Ed25519, base64).
const updatePublicKeyB64 = "DDDFI8ffMDrHS3XXXoWilYC0PVi6Ji39xIjurFJxQRk="

const updateManifestURL = "https://github.com/biopoetic/foveon-lab/releases/latest/download/update-manifest.json"
const releaseDownloadURL = "https://github.com/biopoetic/foveon-lab/releases/download/"

// FOVEON_UPDATE_MANIFEST points the updater at a test server instead (the
// manifest must still carry a valid signature); assets are fetched from the
// same folder as the manifest.
func manifestURL() string {
	if u := os.Getenv("FOVEON_UPDATE_MANIFEST"); u != "" {
		return u
	}
	return updateManifestURL
}

func assetURL(ver, name string) string {
	if u := os.Getenv("FOVEON_UPDATE_MANIFEST"); u != "" {
		return u[:strings.LastIndex(u, "/")+1] + name
	}
	return releaseDownloadURL + ver + "/" + name
}

// UpdateManifest is the signed payload of update-manifest.json.
type UpdateManifest struct {
	App     string                 `json:"app"`
	Version string                 `json:"version"`
	Notes   string                 `json:"notes,omitempty"`
	Assets  map[string]UpdateAsset `json:"assets"` // key: updatePlatform()
}

type UpdateAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SignedManifest is the file as published.
type SignedManifest struct {
	Payload   string `json:"payload"`   // base64 of the UpdateManifest JSON
	Signature string `json:"signature"` // base64 Ed25519 signature of the payload bytes
}

// verifyManifest checks the signature and decodes the payload.
func verifyManifest(raw []byte, pub ed25519.PublicKey) (*UpdateManifest, error) {
	var sm SignedManifest
	if err := json.Unmarshal(raw, &sm); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	payload, err1 := base64.StdEncoding.DecodeString(sm.Payload)
	sig, err2 := base64.StdEncoding.DecodeString(sm.Signature)
	if err1 != nil || err2 != nil || !ed25519.Verify(pub, payload, sig) {
		return nil, errors.New("manifest signature is not valid")
	}
	var m UpdateManifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, fmt.Errorf("manifest payload: %w", err)
	}
	if m.App != "foveon-lab" {
		return nil, fmt.Errorf("manifest is for %q", m.App)
	}
	return &m, nil
}

// parseVersion turns "v1.2.3" into comparable numbers.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newerVersion(latest, current string) bool {
	a, ok1 := parseVersion(latest)
	b, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// install describes how this copy is installed.
type install struct {
	platform string // manifest asset key
	dir      string // folder whose files get replaced (or the .app's parent)
	app      string // macOS: the .app bundle path, else ""
	reason   string // why it can't self-update ("" = it can)
}

func detectInstall() install {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return install{reason: "can't locate the program file"}
	}
	in := install{platform: runtime.GOOS + "-" + runtime.GOARCH, dir: filepath.Dir(exe)}
	if runtime.GOOS == "darwin" {
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
			in.app = exe[:i+len(".app")]
			in.dir = filepath.Dir(in.app)
			in.platform = "darwin-app"
			if strings.Contains(in.app, "/AppTranslocation/") || strings.HasPrefix(in.app, "/Volumes/") {
				in.reason = "move Foveon Lab to the Applications folder first (it is running from the disk image or a temporary location)"
				return in
			}
		}
	}
	if version == "dev" {
		in.reason = "this is a development build"
		return in
	}
	// Can we write there?
	probe := filepath.Join(in.dir, fmt.Sprintf(".foveon-write-test-%d", os.Getpid()))
	if f, err := os.Create(probe); err != nil {
		in.reason = "no permission to write to " + in.dir
	} else {
		f.Close()
		os.Remove(probe)
	}
	return in
}

// updater tracks the newest release and installs it on request.
type updater struct {
	pub     ed25519.PublicKey
	inst    install
	client  *http.Client
	restart func() // set by main: hand the port over and start the new program

	mu         sync.Mutex
	latest     *UpdateManifest
	checked    time.Time
	checkErr   string
	installing bool
}

func newUpdater() *updater {
	u := &updater{inst: detectInstall(), client: &http.Client{Timeout: 10 * time.Minute}}
	if k, err := base64.StdEncoding.DecodeString(updatePublicKeyB64); err == nil && len(k) == ed25519.PublicKeySize {
		u.pub = k
	}
	return u
}

func (u *updater) enabled() bool {
	return u.pub != nil && version != "dev" && os.Getenv("FOVEON_NO_UPDATE") == ""
}

// loop checks shortly after start and then twice a day.
func (u *updater) loop() {
	if !u.enabled() {
		return
	}
	time.Sleep(5 * time.Second)
	for {
		if err := u.check(); err != nil {
			log.Printf("update check: %v", err)
		}
		time.Sleep(12 * time.Hour)
	}
}

func (u *updater) check() error {
	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(manifestURL())
	if err == nil && resp.StatusCode != 200 {
		resp.Body.Close()
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var m *UpdateManifest
	if err == nil {
		var raw []byte
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err == nil {
			m, err = verifyManifest(raw, u.pub)
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checked = time.Now()
	if err != nil {
		u.checkErr = err.Error()
		return err
	}
	u.checkErr = ""
	u.latest = m
	if newerVersion(m.Version, version) {
		log.Printf("update available: %s (running %s)", m.Version, version)
	}
	return nil
}

func (u *updater) status() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := map[string]any{"current": version, "enabled": u.enabled(), "installing": u.installing}
	if u.latest != nil {
		st["latest"] = u.latest.Version
		st["notes"] = u.latest.Notes
		avail := newerVersion(u.latest.Version, version)
		st["available"] = avail
		if avail {
			_, has := u.latest.Assets[u.inst.platform]
			switch {
			case u.inst.reason != "":
				st["reason"] = u.inst.reason
			case !has:
				st["reason"] = "no update file for this system in that release"
			default:
				st["canInstall"] = true
			}
		}
	}
	if u.checkErr != "" {
		st["error"] = u.checkErr
	}
	return st
}

// handleUpdate: GET = status; POST = install the newest release and restart.
func (u *updater) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		if r.URL.Query().Get("check") == "1" && u.enabled() {
			u.check()
		}
		writeJSON(w, u.status())
		return
	}
	u.mu.Lock()
	m := u.latest
	if u.installing || m == nil || !newerVersion(m.Version, version) || u.inst.reason != "" {
		u.mu.Unlock()
		httpErr(w, 409, errors.New("no update to install"))
		return
	}
	asset, ok := m.Assets[u.inst.platform]
	if !ok {
		u.mu.Unlock()
		httpErr(w, 409, errors.New("no update file for this system"))
		return
	}
	u.installing = true
	u.mu.Unlock()

	err := u.install(m.Version, asset)
	if err != nil {
		u.mu.Lock()
		u.installing = false
		u.mu.Unlock()
		log.Printf("update failed: %v", err)
		httpErr(w, 500, err)
		return
	}
	log.Printf("updated to %s — restarting", m.Version)
	writeJSON(w, map[string]any{"ok": true, "version": m.Version})
	go func() {
		time.Sleep(300 * time.Millisecond)
		u.restart()
	}()
}

// download fetches a release asset and checks its size and hash.
func (u *updater) download(ver string, a UpdateAsset) ([]byte, error) {
	resp, err := u.client.Get(assetURL(ver, a.Name))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != a.Size {
		return nil, fmt.Errorf("download %s: got %d bytes, expected %d", a.Name, len(data), a.Size)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, fmt.Errorf("download %s: checksum mismatch", a.Name)
	}
	return data, nil
}

func (u *updater) install(ver string, a UpdateAsset) error {
	data, err := u.download(ver, a)
	if err != nil {
		return err
	}
	if u.inst.app != "" {
		return replaceApp(u.inst.app, data)
	}
	// Plain install: replace the program and x3f_extract next to it.
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	want := map[string]bool{"foveon-lab" + ext: true, "x3f_extract" + ext: true}
	files, err := extractFiles(a.Name, data, want)
	if err != nil {
		return err
	}
	if files["foveon-lab"+ext] == nil {
		return fmt.Errorf("%s has no foveon-lab%s", a.Name, ext)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	for name, body := range files {
		dst := filepath.Join(u.inst.dir, name)
		if name == "foveon-lab"+ext {
			dst = exe // keep whatever name the user gave the program
		}
		if err := replaceFile(dst, body); err != nil {
			return fmt.Errorf("replace %s: %w", filepath.Base(dst), err)
		}
	}
	return nil
}

// extractFiles pulls the wanted base names out of a .zip or .tar.gz.
func extractFiles(name string, data []byte, want map[string]bool) (map[string][]byte, error) {
	out := map[string][]byte{}
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			base := filepath.Base(f.Name)
			if !want[base] || strings.Contains(f.Name, "third_party") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			out[base] = b
		}
		return out, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		base := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !want[base] || strings.Contains(h.Name, "third_party") {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[base] = b
	}
	return out, nil
}

// replaceFile swaps dst for body, safe while dst is running.
func replaceFile(dst string, body []byte) error {
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := dst + ".old"
		os.Remove(old)
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, old); err != nil {
				os.Remove(tmp)
				return err
			}
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Rename(old, dst)
			return err
		}
		return nil
	}
	return os.Rename(tmp, dst)
}

// replaceApp unpacks the zipped .app beside the current one and swaps them.
func replaceApp(app string, data []byte) error {
	parent := filepath.Dir(app)
	stage, err := os.MkdirTemp(parent, ".foveon-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		p := filepath.Join(stage, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, stage+string(os.PathSeparator)) {
			return fmt.Errorf("bad path in update: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err == nil {
			_, err = io.Copy(out, rc)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err != nil {
			return err
		}
	}
	newApp := filepath.Join(stage, "Foveon Lab.app")
	if _, err := os.Stat(filepath.Join(newApp, "Contents", "MacOS", "foveon-lab")); err != nil {
		return errors.New("the update has no Foveon Lab.app")
	}
	old := filepath.Join(parent, fmt.Sprintf(".%s.old-%d", filepath.Base(app), time.Now().Unix()))
	if err := os.Rename(app, old); err != nil {
		return err
	}
	if err := os.Rename(newApp, app); err != nil {
		os.Rename(old, app)
		return err
	}
	return nil
}

// cleanupOldUpdate removes what the previous update left behind.
func cleanupOldUpdate(in install) {
	if in.dir == "" {
		return
	}
	pats := []string{"*.old", ".foveon-update-*"}
	if in.app != "" {
		pats = append(pats, "."+filepath.Base(in.app)+".old-*")
	}
	for _, p := range pats {
		ms, _ := filepath.Glob(filepath.Join(in.dir, p))
		for _, m := range ms {
			os.RemoveAll(m)
		}
	}
}

// restartArgs keeps the user's flags but pins the port (so the open tab
// reconnects) and stops the new copy opening another browser tab.
func restartArgs(port int) []string {
	var args []string
	skip := false
	for _, a := range os.Args[1:] {
		if skip {
			skip = false
			continue
		}
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if name == "port" || name == "no-open" {
			skip = name == "port" && !strings.Contains(a, "=")
			continue
		}
		args = append(args, a)
	}
	return append(args, "-no-open", "-port", strconv.Itoa(port))
}

var restarting atomic.Bool

// relaunch frees the port and starts the (now replaced) program.
func relaunch(ln net.Listener, in install) {
	port := ln.Addr().(*net.TCPAddr).Port
	restarting.Store(true)
	ln.Close()
	args := restartArgs(port)
	var cmd *exec.Cmd
	if in.app != "" {
		cmd = exec.Command("open", append([]string{"-n", in.app, "--args"}, args...)...)
	} else {
		exe, _ := os.Executable()
		exe, _ = filepath.EvalSymlinks(exe)
		cmd = exec.Command(exe, args...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	}
	if err := cmd.Start(); err != nil {
		log.Printf("restart failed: %v — start Foveon Lab again", err)
	}
	os.Exit(0)
}
