package main

// update-manifest.json: the signed list of update files the app's
// self-updater trusts (see updater.go in the app). The Ed25519 private key
// stays on this machine; the app embeds the public key.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func updateKeyPath() string {
	if p := os.Getenv("FOVEON_UPDATE_KEY"); p != "" {
		return p
	}
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir, _ = os.UserConfigDir()
	}
	return filepath.Join(dir, "foveon-lab", "update_signing_key.b64")
}

// genUpdateKey creates the signing key once and prints its public half.
func genUpdateKey() error {
	p := updateKeyPath()
	if _, err := os.Stat(p); err == nil {
		priv, err := loadUpdateKey()
		if err != nil {
			return err
		}
		fmt.Printf("key already exists: %s\npublic key: %s\n", p, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(priv.Seed())), 0o600); err != nil {
		return err
	}
	fmt.Printf("new key written to %s (back it up; never commit it)\npublic key: %s\n", p, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func loadUpdateKey() (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(updateKeyPath())
	if err != nil {
		return nil, fmt.Errorf("update signing key: %w (create it with -gen-update-key)", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("update signing key is malformed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// embeddedPublicKey reads the key the app ships with, so a release can't go
// out signed by a key the published app would reject.
func embeddedPublicKey() (string, error) {
	src, err := os.ReadFile("updater.go")
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`const updatePublicKeyB64 = "([^"]*)"`).FindSubmatch(src)
	if m == nil {
		return "", errors.New("updatePublicKeyB64 not found in updater.go")
	}
	return string(m[1]), nil
}

type manifestAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// writeManifest signs the manifest for version listing files (platform key →
// asset path) and writes it to dst.
func writeManifest(dst, version, notes string, files map[string]string) error {
	priv, err := loadUpdateKey()
	if err != nil {
		return err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	if emb, err := embeddedPublicKey(); err != nil {
		return err
	} else if emb != pub {
		return fmt.Errorf("updater.go embeds public key %q but the signing key's is %q", emb, pub)
	}
	assets := map[string]manifestAsset{}
	for plat, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		assets[plat] = manifestAsset{Name: filepath.Base(p), Size: st.Size(), SHA256: sum}
	}
	payload, err := json.Marshal(map[string]any{"app": "foveon-lab", "version": version, "notes": notes, "assets": assets})
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, payload)
	out, _ := json.MarshalIndent(map[string]string{
		"payload":   base64.StdEncoding.EncodeToString(payload),
		"signature": base64.StdEncoding.EncodeToString(sig),
	}, "", "  ")
	return os.WriteFile(dst, out, 0o644)
}
