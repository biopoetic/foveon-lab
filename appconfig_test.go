package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Two requests that each change one setting must both land. Before
// updateConfig seven handlers read and saved the file without the lock, so
// one of two concurrent changes could vanish.
func TestUpdateConfigKeepsConcurrentChanges(t *testing.T) {
	isolateUserDirs(t)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := updateConfig(func(c *appConfig) bool {
				c.Favorites = append(c.Favorites, fmt.Sprintf("photo-%02d", i))
				return true
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	c, ok := loadConfig()
	if !ok {
		t.Fatal("the config did not load back")
	}
	if len(c.Favorites) != n {
		t.Fatalf("%d of %d concurrent changes survived", len(c.Favorites), n)
	}
}

// A save leaves the finished file and nothing else: no temporary file, and
// the file is whole (the old in-place write could leave it cut short).
func TestSaveConfigReplacesInOneStep(t *testing.T) {
	isolateUserDirs(t)
	if err := saveConfig(appConfig{Presets: "first", Favorites: []string{"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(appConfig{Presets: "second"}); err != nil {
		t.Fatal(err)
	}
	c, ok := loadConfig()
	if !ok || c.Presets != "second" || len(c.Favorites) != 0 {
		t.Fatalf("after the second save: ok=%v %+v", ok, c)
	}
	entries, err := os.ReadDir(filepath.Dir(configPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

// A config file that is not JSON is set aside, not silently overwritten.
func TestUpdateConfigKeepsADamagedFile(t *testing.T) {
	isolateUserDirs(t)
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	damaged := `{"photos": ["C:\\Photos"], "ai": {"key": "sk-keep-me"`
	if err := os.WriteFile(p, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateConfig(func(c *appConfig) bool {
		c.Presets = "new"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(p + ".bad")
	if err != nil || string(kept) != damaged {
		t.Fatalf("the damaged file was not kept: %v %q", err, kept)
	}
	if c, ok := loadConfig(); !ok || c.Presets != "new" {
		t.Fatalf("the fresh config: ok=%v %+v", ok, c)
	}
}

// change returning false writes nothing: a missing file stays missing.
func TestUpdateConfigNoChangeWritesNothing(t *testing.T) {
	isolateUserDirs(t)
	if err := updateConfig(func(c *appConfig) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Fatalf("a config file appeared: %v", err)
	}
	if !strings.HasSuffix(configPath(), filepath.Join("foveon-lab", "config.json")) {
		t.Fatalf("unexpected config path %s", configPath())
	}
}
