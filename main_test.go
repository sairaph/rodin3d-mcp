package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/cli"

	"github.com/sairaph/rodin3d-mcp/internal/guide"
)

// runMainEnv makes the test binary run main instead of the tests: the
// stand-in for the binary an update installed.
const runMainEnv = "RODIN3D_MCP_TEST_RUN_MAIN"

// TestMain points the home directory at a temp directory for the whole run,
// as internal/install's does: refresh-guide resolves the skill folders from
// it. Started with runMainEnv, the binary is main itself, in the home of the
// test that started it.
func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(isolatedRun(m))
}

func isolatedRun(m *testing.M) int {
	root, err := os.MkdirTemp("", "rodin3d-home")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)
	for k, v := range map[string]string{
		"HOME": root, "USERPROFILE": root,
		"APPDATA": filepath.Join(root, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(root, "AppData", "Local"),
		"XDG_CONFIG_HOME": filepath.Join(root, ".config"), "CONTINUE_GLOBAL_DIR": "",
	} {
		if old, ok := os.LookupEnv(k); ok {
			defer os.Setenv(k, old)
		} else {
			defer os.Unsetenv(k)
		}
		os.Setenv(k, v)
	}
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) != filepath.Clean(root) {
		fmt.Fprintf(os.Stderr, "TestMain: the home directory is %q (%v), not %s\n", home, err, root)
		return 1
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" && strings.EqualFold(filepath.Clean(u.HomeDir), filepath.Clean(home)) {
		fmt.Fprintf(os.Stderr, "TestMain: the home directory is the real one, %s\n", home)
		return 1
	}
	return m.Run()
}

// oldCopy writes an old copy of the guide that rodin3d-mcp wrote into the
// temp home's Claude Code folder and returns its SKILL.md path and text.
func oldCopy(t *testing.T) (path, text string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "skills", guide.Name)
	t.Cleanup(func() { os.RemoveAll(dir) })
	text = "---\nname: rodin3d\nmetadata:\n  " + guide.Generator + "\n---\nold\n"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, text
}

func TestRefreshGuideHonoursDryRun(t *testing.T) {
	path, old := oldCopy(t)
	if code := runRefreshGuide(cli.Command{Name: "refresh-guide", Args: []string{"--dry-run"}}); code != 0 {
		t.Fatal(code)
	}
	if got, _ := os.ReadFile(path); string(got) != old {
		t.Error("--dry-run rewrote the copy")
	}
	if code := runRefreshGuide(cli.Command{Name: "refresh-guide"}); code != 0 {
		t.Fatal(code)
	}
	if got, _ := os.ReadFile(path); string(got) != guide.SkillMarkdown() {
		t.Error("the copy was not refreshed")
	}
}

func TestUpdateRefreshesTheGuideThroughTheNewBinary(t *testing.T) {
	path, _ := oldCopy(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The new binary is this test binary running main: `refresh-guide`.
	t.Setenv(runMainEnv, "1")
	if code := runNewBinaryGuideRefresh(context.Background(), exe); code != 0 {
		t.Fatalf("code %d", code)
	}
	if got, _ := os.ReadFile(path); string(got) != guide.SkillMarkdown() {
		t.Error("the new binary's refresh-guide did not rewrite the copy")
	}
}

func TestUpdateReportsANewBinaryThatCannotRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-rodin3d-mcp")
	if code := runNewBinaryGuideRefresh(context.Background(), missing); code != 1 {
		t.Errorf("code %d, want 1", code)
	}
}
