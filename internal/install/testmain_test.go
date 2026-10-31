package install

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// TestMain points the home directory and the per-user folders at a temp
// directory for the whole package run: the guide skill folders (and the
// uninstall layout) are resolved from them, and no test may reach the
// developer's real ~/.claude/skills or client configs.
func TestMain(m *testing.M) { os.Exit(isolatedRun(m)) }

func isolatedRun(m *testing.M) int {
	root, err := os.MkdirTemp("", "rodin3d-home")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)
	env := map[string]string{
		"HOME":            root,
		"USERPROFILE":     root,
		"APPDATA":         filepath.Join(root, "AppData", "Roaming"),
		"LOCALAPPDATA":    filepath.Join(root, "AppData", "Local"),
		"XDG_CONFIG_HOME": filepath.Join(root, ".config"),
		"XDG_DATA_HOME":   filepath.Join(root, ".local", "share"),
		// Moves Continue's skill folder: empty means unset.
		"CONTINUE_GLOBAL_DIR": "",
	}
	for k, v := range env {
		if old, ok := os.LookupEnv(k); ok {
			defer os.Setenv(k, old)
		} else {
			defer os.Unsetenv(k)
		}
		os.Setenv(k, v)
	}
	home, err := os.UserHomeDir()
	if err != nil || !samePath(home, root) {
		fmt.Fprintf(os.Stderr, "TestMain: the home directory is %q (%v), not the temp directory %s\n", home, err, root)
		return 1
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" && samePath(u.HomeDir, home) {
		fmt.Fprintf(os.Stderr, "TestMain: the home directory is the real one, %s\n", home)
		return 1
	}
	return m.Run()
}
