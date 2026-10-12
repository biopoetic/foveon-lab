package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.2.1", true}, {"v0.2.10", "v0.2.9", true}, {"v1.0.0", "v0.99.99", true},
		{"v0.2.1", "v0.2.1", false}, {"v0.2.0", "v0.2.1", false}, {"v0.3.0", "dev", false}, {"junk", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func signed(t *testing.T, priv ed25519.PrivateKey, m any) []byte {
	payload, _ := json.Marshal(m)
	out, _ := json.Marshal(SignedManifest{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	})
	return out
}

func TestVerifyManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	m := UpdateManifest{App: "foveon-lab", Version: "v9.9.9", Assets: map[string]UpdateAsset{"linux-amd64": {Name: "x.tar.gz", Size: 3, SHA256: "ab"}}}
	got, err := verifyManifest(signed(t, priv, m), pub)
	if err != nil || got.Version != "v9.9.9" || got.Assets["linux-amd64"].Name != "x.tar.gz" {
		t.Fatalf("good manifest: %v %+v", err, got)
	}
	if _, err := verifyManifest(signed(t, other, m), pub); err == nil {
		t.Fatal("accepted a manifest signed by another key")
	}
	// Tampered payload.
	var sm SignedManifest
	json.Unmarshal(signed(t, priv, m), &sm)
	m.Version = "v10.0.0"
	p2, _ := json.Marshal(m)
	sm.Payload = base64.StdEncoding.EncodeToString(p2)
	raw, _ := json.Marshal(sm)
	if _, err := verifyManifest(raw, pub); err == nil {
		t.Fatal("accepted a tampered manifest")
	}
	m.App = "something-else"
	if _, err := verifyManifest(signed(t, priv, m), pub); err == nil {
		t.Fatal("accepted another app's manifest")
	}
}

func TestEmbeddedKeyDecodes(t *testing.T) {
	if newUpdater().pub == nil {
		t.Fatal("updatePublicKeyB64 is not a valid Ed25519 public key")
	}
}

func TestRestartArgs(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"x", "-photos", "C:/a", "-port", "9000", "-no-open", "--port=9001", "-presets=D:/p"}
	want := []string{"-photos", "C:/a", "-presets=D:/p", "-no-open", "-port", "8780"}
	if got := restartArgs(8780); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExtractAndReplace(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"foveon-lab-v1/foveon-lab.exe": "NEW", "foveon-lab-v1/x3f_extract.exe": "X3F",
		"foveon-lab-v1/README.md": "r", "foveon-lab-v1/third_party/x3fuse-core/foveon-lab.exe": "decoy",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	files, err := extractFiles("a.zip", buf.Bytes(), map[string]bool{"foveon-lab.exe": true, "x3f_extract.exe": true})
	if err != nil || string(files["foveon-lab.exe"]) != "NEW" || string(files["x3f_extract.exe"]) != "X3F" || len(files) != 2 {
		t.Fatalf("extract: %v %q", err, files)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "foveon-lab.exe")
	os.WriteFile(dst, []byte("OLD"), 0o755)
	if err := replaceFile(dst, files["foveon-lab.exe"]); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "NEW" {
		t.Fatalf("dst = %q", b)
	}
	cleanupOldUpdate(install{dir: dir})
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestLensDesc(t *testing.T) {
	cases := map[[2]string]string{
		{"18 to 35", "1.8 to 16"}:  "18–35mm f/1.8–16",
		{"16.6", "4.0"}:            "16.6mm f/4",
		{"50 to 150", "22 to 2.8"}: "50–150mm f/2.8–22",
		{"", ""}:                   "",
	}
	for in, want := range cases {
		if got := lensDesc(map[string]string{"LENSFRANGE": in[0], "LENSARANGE": in[1]}); got != want {
			t.Errorf("lensDesc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRotationFromJPEG(t *testing.T) {
	// SOI, APP1 "Exif" with a little-endian TIFF holding Orientation=6.
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	b = append(b, seg...)
	b = append(b, 0xFF, 0xDA, 0, 2)
	if r := rotationFromJPEG(b); r != 90 {
		t.Fatalf("rotation = %d, want 90", r)
	}
	if r := rotationFromJPEG([]byte{0xFF, 0xD8, 0xFF, 0xDA, 0, 2}); r != 0 {
		t.Fatalf("no EXIF: rotation = %d", r)
	}
}
