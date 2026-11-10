package main

import "testing"

// isolateUserDirs points every per-user folder the app writes to (config
// and cache) at a throwaway folder, on all three systems, and returns it.
// os.UserConfigDir and os.UserCacheDir read different variables on each:
// Windows %AppData% / %LocalAppData%, Linux $XDG_CONFIG_HOME /
// $XDG_CACHE_HOME, macOS $HOME/Library/…. A test that set only one of
// them passed on Windows and wrote into the real folders elsewhere.
func isolateUserDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}
