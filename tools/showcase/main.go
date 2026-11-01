// tools/showcase runs the app's showcase command from anywhere in the tree.
// The raw development and the SPP reader live in the app's own package, so
// the tool is a thin launcher: it finds the module root, points Go's cache at
// the repository's .gocache/.gotmp, and runs `go run . showcase …`.
//
//	go run ./tools/showcase -manifest zz-out/task22/showcase.json
//
// FOVEON_LAB_BIN=/path/to/foveon-lab skips the build and uses that binary.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "showcase:", err)
		os.Exit(1)
	}
	var cmd *exec.Cmd
	if bin := os.Getenv("FOVEON_LAB_BIN"); bin != "" {
		cmd = exec.Command(bin, append([]string{"showcase"}, os.Args[1:]...)...)
	} else {
		cmd = exec.Command("go", append([]string{"run", ".", "showcase"}, os.Args[1:]...)...)
	}
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	env := os.Environ()
	if os.Getenv("GOCACHE") == "" {
		env = append(env, "GOCACHE="+filepath.Join(root, ".gocache"))
	}
	if os.Getenv("GOTMPDIR") == "" {
		env = append(env, "GOTMPDIR="+filepath.Join(root, ".gotmp"))
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "showcase:", err)
		os.Exit(1)
	}
}

// moduleRoot walks up from the working directory to the go.mod that names
// this module.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.Contains(string(b), "module github.com/biopoetic/foveon-lab") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside the foveon-lab module (no go.mod with module github.com/biopoetic/foveon-lab)")
		}
		dir = parent
	}
}
