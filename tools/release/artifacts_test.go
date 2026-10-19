package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealArtifacts runs the gate against the artifacts in dist/, when they
// are present. It is skipped elsewhere. The DMG shipped as v0.4.24 must pass;
// its app must fail the identifier check, because that release predates the
// move from com.algobox.foveonlab to com.biopoetic.foveonlab.
func TestRealArtifacts(t *testing.T) {
	rc := os.Getenv("STOCKCHAT_RCODESIGN")
	if rc == "" {
		t.Skip("STOCKCHAT_RCODESIGN not set")
	}
	root := filepath.Join("..", "..", "dist")
	dmgs, _ := filepath.Glob(filepath.Join(root, "v*", "*.dmg"))
	if len(dmgs) == 0 {
		t.Skip("no DMG in dist/")
	}
	// Newest first.
	dmg := dmgs[len(dmgs)-1]
	out, err := output(rc, "print-signature-info", dmg)
	if err != nil {
		t.Fatalf("%s: %v", dmg, err)
	}
	if err := checkSignatureDump(out, ""); err != nil {
		t.Errorf("%s should pass the signature/stable check: %v", filepath.Base(dmg), err)
	}

	// The v0.4.24 app bundle predates the identifier fix (later builds pass
	// the identifier check, so only that one can show the gate refusing).
	apps, _ := filepath.Glob(filepath.Join(root, "v0.4.24", "macos-app", "*.app"))
	if len(apps) == 0 {
		return
	}
	aout, err := output(rc, "print-signature-info", apps[0])
	if err != nil {
		t.Fatalf("%s: %v", apps[0], err)
	}
	// Signature and ticket must be there...
	if err := checkSignatureDump(aout, ""); err != nil {
		t.Errorf("%s: signature/ticket: %v", filepath.Base(apps[0]), err)
	}
	// ...but the old identifier must be reported.
	err = checkSignatureDump(aout, "com.biopoetic.foveonlab")
	if err == nil || !strings.Contains(err.Error(), "bundle identifier is not") {
		t.Errorf("the pre-fix app should fail the identifier check, got %v", err)
	}
}
