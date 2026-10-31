package install

// The guide skill as the commands carry it: install --yes, add --yes, the
// end of the wizard, uninstall and uninstall --scope project, each run with
// mcp-wizard's real detector against the temp home of TestMain (Claude Code
// is detected there by its ~/.claude folder) and a project in a temp dir.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sairaph/mcp-wizard/cli"
	"github.com/sairaph/mcp-wizard/harness"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/guide"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// testEnv is an Env whose service keeps its data in a temp dir, with the
// output it collects.
func testEnv(t *testing.T) (Env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	out := &bytes.Buffer{}
	return Env{
		Service: func() *service.Service {
			return service.New(service.Options{Paths: domain.PathsUnder(root), Version: "test"})
		},
		Stdin:  strings.NewReader(""),
		Stdout: out,
		Stderr: out,
	}, out
}

// noUserPath keeps uninstall away from the real user PATH.
func noUserPath(t *testing.T) {
	failOn(t, &userPath, func() userPathStore { return nil })
}

func isOurs(t *testing.T, dir string) bool {
	t.Helper()
	ours, _ := ownSkill(dir)
	return ours
}

func TestInstallYesAndUninstallCarryTheSkill(t *testing.T) {
	noUserPath(t)
	home := testHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ours := filepath.Dir(skillPath(home, folderClaude))
	foreign := skillPath(home, folderAgents)
	t.Cleanup(func() {
		os.RemoveAll(claudeDir)
		os.RemoveAll(filepath.Join(home, ".agents"))
		os.Remove(filepath.Join(home, ".claude.json"))
	})
	ctx := context.Background()
	env, out := testEnv(t)

	if code := Install(ctx, env, cli.Command{Name: "install", Yes: true, Clients: []string{"claude-code"}}); code != 0 {
		t.Fatalf("install --yes: code %d\n%s", code, out)
	}
	if !isOurs(t, ours) || !strings.Contains(out.String(), "wrote the guide skill to "+ours) {
		t.Fatalf("install --yes did not write the skill:\n%s", out)
	}

	// A foreign copy of the same name in a folder install also writes.
	if err := os.MkdirAll(filepath.Dir(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte(foreignSkill), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := Uninstall(ctx, env, cli.Command{Name: "uninstall", DryRun: true}); code != 0 {
		t.Fatalf("uninstall --dry-run: code %d\n%s", code, out)
	}
	listing := out.String()
	if !strings.Contains(listing, "Guide skill") || !strings.Contains(listing, ours) || strings.Contains(listing, filepath.Dir(foreign)) {
		t.Errorf("uninstall --dry-run lists:\n%s", listing)
	}
	if !isOurs(t, ours) {
		t.Fatal("a dry run removed the skill")
	}

	out.Reset()
	if code := Uninstall(ctx, env, cli.Command{Name: "uninstall", Yes: true}); code != 0 {
		t.Fatalf("uninstall --yes: code %d\n%s", code, out)
	}
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Errorf("uninstall left our copy (%v):\n%s", err, out)
	}
	if !strings.Contains(out.String(), "removed "+ours) || !strings.Contains(out.String(), "Claude Code: entry removed") {
		t.Errorf("uninstall output:\n%s", out)
	}
	if got, _ := os.ReadFile(foreign); string(got) != foreignSkill {
		t.Error("uninstall removed a foreign copy")
	}
}

func TestAddYesAndProjectUninstallCarryTheSkill(t *testing.T) {
	project := t.TempDir()
	target := filepath.Dir(skillPath(project, folderClaude))
	ctx := context.Background()
	env, out := testEnv(t)
	add := cli.Command{Name: "add", Yes: true, Dir: project, Clients: []string{"claude-code"}}

	dry := add
	dry.DryRun = true
	if code := Add(ctx, env, dry); code != 0 || !strings.Contains(out.String(), "would write the guide skill to "+target) {
		t.Fatalf("add --dry-run: code %d\n%s", code, out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("add --dry-run wrote the skill")
	}

	out.Reset()
	if code := Add(ctx, env, add); code != 0 || !isOurs(t, target) {
		t.Fatalf("add --yes: code %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); err != nil {
		t.Fatalf("add --yes registered nothing: %v", err)
	}

	remove := cli.Command{Name: "uninstall", Scope: string(harness.ScopeProject), Dir: project}
	out.Reset()
	remove.DryRun = true
	if code := Uninstall(ctx, env, remove); code != 0 || !strings.Contains(out.String(), "would remove the guide skill "+target) || !isOurs(t, target) {
		t.Fatalf("uninstall --scope project --dry-run: code %d\n%s", code, out)
	}
	out.Reset()
	remove.DryRun, remove.Yes = false, true
	if code := Uninstall(ctx, env, remove); code != 0 || !strings.Contains(out.String(), "removed the guide skill "+target) {
		t.Fatalf("uninstall --scope project: code %d\n%s", code, out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the project's copy is still there (%v)", err)
	}
}

func TestFinishWizardWritesTheSkillOnceTheClientsAreRegistered(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	target := filepath.Dir(skillPath(project, folderClaude))
	env, out := testEnv(t)
	svc := env.Service()
	defer svc.Close()
	state := func(done bool) *State {
		s := &State{}
		s.Harness.Selected = map[harness.ID]bool{"claude-code": true}
		s.Results.Done = done
		return s
	}

	// Left before the registration step: nothing is written.
	if code := finishWizard(env, svc, state(false), scope, cli.Command{}, 0); code != 0 || !strings.Contains(out.String(), "Setup cancelled") {
		t.Fatalf("cancelled: code %d\n%s", code, out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a cancelled wizard wrote the skill")
	}

	out.Reset()
	if code := finishWizard(env, svc, state(true), scope, cli.Command{DryRun: true}, 0); code != 0 || !strings.Contains(out.String(), "would write the guide skill to "+target) {
		t.Fatalf("dry run: code %d\n%s", code, out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote the skill")
	}

	out.Reset()
	if code := finishWizard(env, svc, state(true), scope, cli.Command{}, 0); code != 0 || !isOurs(t, target) {
		t.Fatalf("registered: code %d\n%s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(got) != guide.SkillMarkdown() {
		t.Error("the wizard wrote another SKILL.md than the embedded one")
	}

	// A folder that cannot be written fails the wizard's exit code.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, ".claude"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := finishWizard(env, svc, state(true), harness.ProjectScopeDir(blocked), cli.Command{}, 0); code != 1 {
		t.Errorf("unwritable folder: code %d, want 1\n%s", code, out)
	}
}
