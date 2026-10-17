package main

import (
	"strings"
	"testing"
)

// Real rcodesign print-signature-info output, trimmed to the lines the check
// reads. The DMG's identifier is its volume name; the .app's is its bundle
// identifier — the difference the check has to respect.
const dumpDMG = `- path: Foveon-Lab-v0.4.24.dmg
  file_sha256: a199d1f7
  entity:
    dmg:
      signature:
        superblob_length: 11095
        blob_count: 4
        blobs:
        - slot: CodeDirectory (0)
        - slot: RequirementSet (2)
        - slot: CMS Signature (65536)
        - slot: Ticket (65538)
          identifier: Foveon-Lab-v0.4.24
          certificates:
            apple_certificate_profile: developer-id-application
            apple_team_id: C7P4CN9P8H
`

const dumpApp = `- path: Foveon Lab.app
  entity:
    bundle_code_signature_file: NotarizationTicket
  entity:
      signature:
        blobs:
        - slot: CodeDirectory (0)
        - slot: CMS Signature (65536)
        - slot: Ticket (65538)
          identifier: com.biopoetic.foveonlab
          certificates:
            apple_certificate_profile: developer-id-application
            apple_team_id: C7P4CN9P8H
`

func TestCheckSignatureDump(t *testing.T) {
	cases := []struct {
		name    string
		dump    string
		wantID  string
		wantErr string
	}{
		{"dmg: signed, stapled", dumpDMG, "", ""},
		{"app: signed, stapled, right id", dumpApp, "com.biopoetic.foveonlab", ""},
		{"app: stale bundle id", dumpApp, "com.algobox.foveonlab", "bundle identifier is not"},
		{"app: wrong project id", dumpApp, "org.example.other", "bundle identifier is not"},
		{"no signature", "- path: x\n  entity: other\n", "", "no code signature"},
		{"signed but not stapled",
			strings.Replace(dumpDMG, "        - slot: Ticket (65538)\n", "", 1),
			"", "not stapled"},
		{"ad-hoc signature",
			strings.Replace(dumpDMG, "developer-id-application", "adhoc", 1),
			"", "Developer ID Application"},
	}
	for _, c := range cases {
		err := checkSignatureDump(c.dump, c.wantID)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", c.name, err)
		case c.wantErr != "" && err == nil:
			t.Errorf("%s: no error, want %q", c.name, c.wantErr)
		case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.wantErr)
		}
	}
}

// The shipped bundle identifier must be the project's, not a leftover from
// another product: macOS keys permissions and app identity on it.
func TestBundleIdentifier(t *testing.T) {
	plist := infoPlist("v9.9.9")
	if !strings.Contains(plist, "<string>com.biopoetic.foveonlab</string>") {
		t.Error("Info.plist does not carry com.biopoetic.foveonlab")
	}
	if strings.Contains(plist, "algobox") {
		t.Error("Info.plist still carries the old com.algobox identifier")
	}
	if !strings.Contains(plist, "<string>9.9.9</string>") {
		t.Error("version is not written into the plist")
	}
}
