package install

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sairaph/mcp-wizard/harness"

	"github.com/sairaph/rodin3d-mcp/internal/guide"
)

func testHome(t *testing.T) string {
	t.Helper()
	home, err := skillHome()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func skillPath(root string, folder skillFolder) string {
	return filepath.Join(root, filepath.FromSlash(string(folder)), guide.Name, "SKILL.md")
}

const foreignSkill = "---\nname: rodin3d\n---\nmine\n"

func TestSkillFoldersMapEachClientToTheFolderItReads(t *testing.T) {
	home := testHome(t)
	rel := func(dirs []string) []string {
		var out []string
		for _, d := range dirs {
			r, _ := filepath.Rel(home, d)
			out = append(out, filepath.ToSlash(r))
		}
		return out
	}
	cases := []struct {
		name    string
		ids     []harness.ID
		want    []string
		desktop bool
	}{
		{"claude code", []harness.ID{"claude-code"}, []string{".claude/skills"}, false},
		{"codex shares .agents", []harness.ID{"codex", "gemini"}, []string{".agents/skills"}, false},
		{"cursor beside claude code is served by .claude", []harness.ID{"claude-code", "cursor"}, []string{".claude/skills"}, false},
		{"cursor alone reads .agents", []harness.ID{"cursor"}, []string{".agents/skills"}, false},
		{"kiro", []harness.ID{"amazon-q"}, []string{".kiro/skills"}, false},
		{"continue reads its own folder", []harness.ID{"continue"}, []string{".continue/skills"}, false},
		{"claude desktop has no folder", []harness.ID{"claude-desktop"}, nil, true},
	}
	for _, c := range cases {
		dirs, desktop := skillFolders(c.ids, harness.Scope{}, home)
		if got := rel(dirs); strings.Join(got, ",") != strings.Join(c.want, ",") || desktop != c.desktop {
			t.Errorf("%s: folders %v desktop %v, want %v %v", c.name, got, desktop, c.want, c.desktop)
		}
	}
	// Project scope roots the folders in the project.
	project := t.TempDir()
	dirs, _ := skillFolders([]harness.ID{"claude-code", "continue"}, harness.ProjectScopeDir(project), home)
	if len(dirs) != 1 || !strings.HasPrefix(dirs[0], project) {
		t.Errorf("project folders = %v", dirs)
	}
}

func TestInstallSkillsWritesReplacesAndLeavesForeignFoldersAlone(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	target := skillPath(project, folderClaude)
	var out bytes.Buffer

	if code := installSkills(&out, scope, []harness.ID{"claude-code"}, true, false); code != 0 {
		t.Fatalf("dry run: code %d", code)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a dry run wrote the skill")
	}

	if code := installSkills(&out, scope, []harness.ID{"claude-code"}, false, false); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	data, err := os.ReadFile(target)
	if err != nil || !guide.IsOurs(string(data)) {
		t.Fatalf("skill not written, or not marked as ours: %v", err)
	}
	for _, topic := range guide.Topics() {
		if _, err := os.Stat(filepath.Join(filepath.Dir(target), topic+".md")); err != nil {
			t.Errorf("topic %s not written: %v", topic, err)
		}
	}

	// A stale file inside our own copy goes when the folder is replaced.
	stale := filepath.Join(filepath.Dir(target), "old-topic.md")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	installSkills(&out, scope, []harness.ID{"claude-code"}, false, false)
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file of the previous copy survived the rewrite")
	}

	// A skill of the same name that somebody else wrote is left alone.
	if err := os.WriteFile(target, []byte(foreignSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	installSkills(&out, scope, []harness.ID{"claude-code"}, false, false)
	if got, _ := os.ReadFile(target); string(got) != foreignSkill {
		t.Error("a foreign skill was overwritten")
	}
	if !strings.Contains(out.String(), "[skip]") {
		t.Errorf("no [skip] line:\n%s", out.String())
	}
}

func TestInstallSkillsNamesClaudeDesktopsLimit(t *testing.T) {
	var out bytes.Buffer
	if code := installSkills(&out, harness.ProjectScopeDir(t.TempDir()), []harness.ID{"claude-desktop"}, false, false); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "Claude Desktop takes skills only as an upload") || !strings.Contains(out.String(), "get_guide") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRemoveSkillsRemovesOnlyOurCopiesAndKeepsFoldersOthersStillRead(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	var out bytes.Buffer
	installSkills(&out, scope, []harness.ID{"claude-code", "codex"}, false, false)
	claude, agents := skillPath(project, folderClaude), skillPath(project, folderAgents)
	for _, p := range []string{claude, agents} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Removing codex removes .agents; claude-code stays registered and keeps .claude.
	removeSkills(&out, scope, []harness.ID{"codex"}, []harness.ID{"claude-code"}, false)
	if _, err := os.Stat(agents); err == nil {
		t.Error("the removed client's copy is still there")
	}
	if _, err := os.Stat(claude); err != nil {
		t.Error("the remaining client's copy was removed")
	}

	// A dry run removes nothing.
	removeSkills(&out, scope, []harness.ID{"claude-code"}, nil, true)
	if _, err := os.Stat(claude); err != nil {
		t.Error("a dry run removed a copy")
	}

	// A foreign copy is never removed.
	if err := os.WriteFile(claude, []byte(foreignSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	removeSkills(&out, scope, []harness.ID{"claude-code"}, nil, false)
	if _, err := os.Stat(claude); err != nil {
		t.Error("a foreign copy was removed")
	}
}

func TestRemoveSkillsGivesARemainingClientACopyWhereItLooks(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	var out bytes.Buffer
	// Cursor beside Claude Code reads the .claude copy.
	installSkills(&out, scope, []harness.ID{"claude-code", "cursor"}, false, false)
	removeSkills(&out, scope, []harness.ID{"claude-code"}, []harness.ID{"cursor"}, false)
	if _, err := os.Stat(skillPath(project, folderClaude)); err == nil {
		t.Error("the removed client's copy is still there")
	}
	if _, err := os.Stat(skillPath(project, folderAgents)); err != nil {
		t.Errorf("cursor, left alone, got no copy in .agents:\n%s", out.String())
	}
}

func TestPruneRemovesACopyNoRegisteredClientReadsAnyMore(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	var out bytes.Buffer
	installSkills(&out, scope, []harness.ID{"codex"}, false, false)
	agents := skillPath(project, folderAgents)
	installSkills(&out, scope, []harness.ID{"claude-code"}, false, true)
	if _, err := os.Stat(agents); err == nil {
		t.Error("with prune, the copy in an unread folder was kept")
	}
	if _, err := os.Stat(skillPath(project, folderClaude)); err != nil {
		t.Error("the copy for the registered client is missing")
	}
}

func TestRefreshAndOwnUserSkillsTouchOnlyOurUserCopies(t *testing.T) {
	home := testHome(t)
	ours := skillPath(home, folderClaude)
	foreign := skillPath(home, folderAgents)
	t.Cleanup(func() {
		os.RemoveAll(filepath.Dir(ours))
		os.RemoveAll(filepath.Dir(foreign))
	})
	oursOld := "---\nname: rodin3d\nmetadata:\n  " + guide.Generator + "\n---\nold\n"
	for _, p := range []string{ours, foreign} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(ours, []byte(oursOld), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte(foreignSkill), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ownUserSkills(home); len(got) != 1 || got[0] != filepath.Dir(ours) {
		t.Errorf("own user skills = %v, want only %s", got, filepath.Dir(ours))
	}

	var out bytes.Buffer
	if code := RefreshSkills(&out, true); code != 0 {
		t.Fatal(code)
	}
	if got, _ := os.ReadFile(ours); string(got) != oursOld {
		t.Error("a dry run rewrote the copy")
	}
	if code := RefreshSkills(&out, false); code != 0 {
		t.Fatal(code)
	}
	if got, _ := os.ReadFile(ours); string(got) != guide.SkillMarkdown() {
		t.Error("the copy was not refreshed to the embedded guide")
	}
	if got, _ := os.ReadFile(foreign); string(got) != foreignSkill {
		t.Error("a foreign copy was refreshed")
	}
	if !strings.Contains(out.String(), "updated the guide skill in "+filepath.Dir(ours)) || strings.Contains(out.String(), filepath.Dir(foreign)) {
		t.Errorf("refresh output:\n%s", out.String())
	}
}

func planRowFor(plan skillPlan, dir string) (skillRow, bool) {
	for _, r := range plan.Rows {
		if strings.HasSuffix(filepath.ToSlash(r.Dir), filepath.ToSlash(dir)) {
			return r, true
		}
	}
	return skillRow{}, false
}

func TestPlanSkillsDryRunWritesNothing(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	plan := planSkills(scope, []harness.ID{"claude-code", "codex"}, true)
	if plan.HomeErr != nil || plan.Desktop || len(plan.Rows) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	for _, folder := range []string{".claude/skills/rodin3d", ".agents/skills/rodin3d"} {
		row, ok := planRowFor(plan, folder)
		if !ok || row.State != skillWouldWrite || row.Clients != 1 || !strings.HasPrefix(row.Dir, project) {
			t.Errorf("%s: %+v", folder, row)
		}
	}
	entries, err := os.ReadDir(project)
	if err != nil || len(entries) != 0 {
		t.Errorf("a dry run wrote %v (%v)", entries, err)
	}
}

func TestPlanSkillsSharesOneCopyPerFolder(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	plan := planSkills(scope, []harness.ID{"claude-code", "cursor", "opencode"}, false)
	if len(plan.Rows) != 1 {
		t.Fatalf("clients that read the .claude folder share one copy: %+v", plan.Rows)
	}
	row := plan.Rows[0]
	if row.State != skillWritten || row.Clients != 3 || row.Err != nil {
		t.Errorf("row = %+v", row)
	}
	// A second run replaces the copy it wrote.
	again := planSkills(scope, []harness.ID{"claude-code"}, false)
	if len(again.Rows) != 1 || again.Rows[0].State != skillWritten {
		t.Errorf("rewriting its own copy: %+v", again.Rows)
	}
}

func TestPlanSkillsReportsAFolderThatCannotBeWritten(t *testing.T) {
	project := t.TempDir()
	// A file where the skills folder should be: nothing can be created in it.
	if err := os.WriteFile(filepath.Join(project, ".claude"), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := planSkills(harness.ProjectScopeDir(project), []harness.ID{"claude-code", "codex"}, false)
	claude, _ := planRowFor(plan, ".claude/skills/rodin3d")
	agents, _ := planRowFor(plan, ".agents/skills/rodin3d")
	if claude.State != skillFailed || claude.Err == nil {
		t.Errorf(".claude row = %+v", claude)
	}
	if agents.State != skillWritten {
		t.Errorf("one failed folder stopped the others: %+v", agents)
	}
	var out bytes.Buffer
	if code := installSkills(&out, harness.ProjectScopeDir(project), []harness.ID{"claude-code"}, false, false); code != 1 {
		t.Errorf("a folder that cannot be written: code %d, want 1", code)
	}
}

func TestPlanSkillsForClaudeDesktopHasNoFolder(t *testing.T) {
	plan := planSkills(harness.ProjectScopeDir(t.TempDir()), []harness.ID{"claude-desktop"}, false)
	if !plan.Desktop || len(plan.Rows) != 0 {
		t.Errorf("plan = %+v", plan)
	}
	if plan := planSkills(harness.Scope{}, nil, false); plan.Desktop || len(plan.Rows) != 0 || plan.HomeErr != nil {
		t.Errorf("no clients: %+v", plan)
	}
}

func TestPlanSkillsInTheIsolatedHome(t *testing.T) {
	home := testHome(t)
	plan := planSkills(harness.Scope{}, []harness.ID{"claude-code"}, false)
	if len(plan.Rows) != 1 || plan.Rows[0].State != skillWritten || !strings.HasPrefix(plan.Rows[0].Dir, home) {
		t.Fatalf("plan = %+v (home %s)", plan.Rows, home)
	}
	t.Cleanup(func() { os.RemoveAll(plan.Rows[0].Dir) })
	if _, err := os.Stat(skillPath(home, folderClaude)); err != nil {
		t.Errorf("the guide is not in the isolated home: %v", err)
	}
}

func TestSkillClientsAddEveryClientThatRunsThisProgram(t *testing.T) {
	hs := []harness.Harness{{ID: "claude-code", Configured: true}, {ID: "codex"}, {ID: "cursor", Configured: true}}
	got := skillClients(hs, []harness.ID{"codex", "claude-code"})
	if strings.Join(idStrings(got), ",") != "codex,claude-code,cursor" {
		t.Errorf("skill clients = %v", got)
	}
	if got := selectedIDs(map[harness.ID]bool{"b": true, "a": true, "c": false}); strings.Join(idStrings(got), ",") != "a,b" {
		t.Errorf("selected = %v", got)
	}
}

func idStrings(ids []harness.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// failingFS serves its files except one, which fails to read.
type failingFS struct {
	fstest.MapFS
	bad string
}

func (f failingFS) ReadFile(name string) ([]byte, error) {
	if name == f.bad {
		return nil, errors.New("disk full")
	}
	return f.MapFS.ReadFile(name)
}

func (f failingFS) Open(name string) (fs.File, error) {
	if name == f.bad {
		return nil, errors.New("disk full")
	}
	return f.MapFS.Open(name)
}

func TestWriteSkillFromKeepsThePreviousCopyOnFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rodin3d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(old, []byte("good copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := failingFS{MapFS: fstest.MapFS{"SKILL.md": {Data: []byte("new")}, "topic.md": {Data: []byte("t")}}, bad: "topic.md"}
	if err := writeSkillFrom(src, dir); err == nil {
		t.Fatal("a failing write reported success")
	}
	if got, _ := os.ReadFile(old); string(got) != "good copy" {
		t.Errorf("the previous copy was damaged: %q", got)
	}
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Error("the temporary folder was left behind")
	}

	// A good source replaces the copy and drops what it no longer has.
	if err := os.WriteFile(filepath.Join(dir, "gone.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := fstest.MapFS{"SKILL.md": {Data: []byte("new")}, "sub/topic.md": {Data: []byte("t")}}
	if err := writeSkillFrom(good, dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(old); string(got) != "new" {
		t.Errorf("SKILL.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "topic.md")); err != nil {
		t.Error("a nested file was not written")
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.md")); err == nil {
		t.Error("a file of the previous copy survived")
	}
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Error("the temporary folder was left behind")
	}
}

func TestTestMainNeutralisesTheGuideFolderEnvironment(t *testing.T) {
	if got := os.Getenv("CONTINUE_GLOBAL_DIR"); got != "" {
		t.Errorf("CONTINUE_GLOBAL_DIR = %q under test: it would reach the real machine", got)
	}
}

// failOn replaces the file operation op with fail for the rest of the test.
func failOn[F any](t *testing.T, op *F, fail F) {
	t.Helper()
	saved := *op
	*op = fail
	t.Cleanup(func() { *op = saved })
}

var errLocked = errors.New("the file is used by another process")

// writeOurs writes an old copy of ours, with one more topic, at dir.
func writeOurs(t *testing.T, dir string) string {
	t.Helper()
	old := "---\nname: rodin3d\nmetadata:\n  " + guide.Generator + "\n---\nold\n"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"SKILL.md": old, "gone.md": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return old
}

func assertOurs(t *testing.T, dir, wantSkill string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || string(got) != wantSkill {
		t.Fatalf("%s/SKILL.md = %q (%v), want %q", dir, got, err, wantSkill)
	}
	if !ownSkill(dir) {
		t.Fatalf("%s is no longer recognised as ours", dir)
	}
}

func TestAFailedRenameLeavesThePreviousCopyWhole(t *testing.T) {
	for _, step := range []string{"aside", "in"} {
		t.Run(step, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "rodin3d")
			old := writeOurs(t, dir)
			failOn(t, &renamePath, func(from, to string) error {
				if (step == "aside" && strings.HasSuffix(to, ".old")) || (step == "in" && strings.HasSuffix(from, ".new")) {
					return errLocked
				}
				return os.Rename(from, to)
			})
			if err := writeSkillFolder(dir); !errors.Is(err, errLocked) {
				t.Fatalf("err = %v, want the rename's", err)
			}
			assertOurs(t, dir, old)
			for _, side := range []string{".new", ".old"} {
				if _, err := os.Stat(dir + side); !os.IsNotExist(err) {
					t.Errorf("%s was left behind", dir+side)
				}
			}
		})
	}
}

func TestAnOldCopyThatCannotGoIsClearedByTheNextWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rodin3d")
	writeOurs(t, dir)
	restore := removeTree
	failOn(t, &removeTree, func(p string) error {
		if strings.HasSuffix(p, ".old") {
			return errLocked
		}
		return restore(p)
	})
	if err := writeSkillFolder(dir); err != nil {
		t.Fatalf("the new copy is in place, so the write succeeded: %v", err)
	}
	assertOurs(t, dir, guide.SkillMarkdown())
	if _, err := os.Stat(filepath.Join(dir, "gone.md")); err == nil {
		t.Error("a file of the previous copy is in the new one")
	}
	// The copy aside lost its SKILL.md first: no client lists it as a skill.
	if _, err := os.Stat(filepath.Join(dir+".old", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("the copy aside still has its SKILL.md (%v)", err)
	}
	removeTree = restore
	if err := writeSkillFolder(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + ".old"); !os.IsNotExist(err) {
		t.Error("the next write did not clear the copy aside")
	}
}

func TestAForeignFolderAsideIsLeftAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rodin3d")
	writeOurs(t, dir)
	aside := filepath.Join(dir+".old", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(aside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aside, []byte(foreignSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	clearAside(dir + ".old")
	if got, _ := os.ReadFile(aside); string(got) != foreignSkill {
		t.Error("a foreign folder named like a copy aside was removed")
	}
}

func TestALockedFileKeepsTheCopyOursUntilTheNextRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rodin3d")
	old := writeOurs(t, dir)
	restore := removeTree
	failOn(t, &removeTree, func(p string) error {
		if filepath.Base(p) == "gone.md" {
			return errLocked
		}
		return restore(p)
	})
	var out bytes.Buffer
	if code := removeOwnSkills(&out, []string{dir}, false); code != 1 || !strings.Contains(out.String(), "[fail]") {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	assertOurs(t, dir, old)
	removeTree = restore
	out.Reset()
	if code := removeOwnSkills(&out, []string{dir}, false); code != 0 {
		t.Fatalf("second removal: code %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the copy is still there after the second removal (%v)", err)
	}
}

// makeLink makes link point to the folder target as kind (symlink, or on
// Windows junction), and skips the test where this system does not let it.
func makeLink(t *testing.T, kind, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "symlink":
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("cannot create a symbolic link here: %v", err)
		}
	case "junction":
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Skipf("cannot create a junction here: %v %s", err, out)
		}
	}
	if !isLink(link) {
		t.Fatalf("%s is not seen as a link", link)
	}
}

func linkKinds() []string {
	if runtime.GOOS == "windows" {
		return []string{"symlink", "junction"}
	}
	return []string{"symlink"}
}

// A copy kept in the user's own folder (dotfiles) and linked into a skills
// folder: removing or replacing it touches the link only.
func TestALinkedCopyLosesOnlyTheLink(t *testing.T) {
	for _, kind := range linkKinds() {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "dotfiles", "rodin3d")
			old := writeOurs(t, target)
			intact := func(step string) {
				t.Helper()
				assertOurs(t, target, old)
				if _, err := os.Stat(filepath.Join(target, "gone.md")); err != nil {
					t.Errorf("%s: a file in the link's target was removed: %v", step, err)
				}
			}
			link := filepath.Join(root, "skills", "rodin3d")

			makeLink(t, kind, target, link)
			var out bytes.Buffer
			if code := removeOwnSkills(&out, []string{link}, false); code != 0 {
				t.Fatalf("removal: code %d\n%s", code, out.String())
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Errorf("the link is still there (%v)", err)
			}
			intact("removal")

			makeLink(t, kind, target, link)
			if err := writeSkillFolder(link); err != nil {
				t.Fatal(err)
			}
			if isLink(link) {
				t.Error("the write left the link in place of a copy")
			}
			assertOurs(t, link, guide.SkillMarkdown())
			if _, err := os.Lstat(link + ".old"); !os.IsNotExist(err) {
				t.Errorf("the link renamed aside is still there (%v)", err)
			}
			intact("write")

			aside := filepath.Join(root, "other", "rodin3d.old")
			makeLink(t, kind, target, aside)
			clearAside(aside)
			if _, err := os.Lstat(aside); !os.IsNotExist(err) {
				t.Errorf("the link aside is still there (%v)", err)
			}
			intact("clearing aside")
		})
	}
}

// writeFiles makes dir with the named files in it.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const markerText = guide.Generator + "\n"

// A folder is ours, free (empty), foreign or absent by its content only.
func TestOwnerOfGoesByContentOnly(t *testing.T) {
	root := t.TempDir()
	ours := "---\nname: rodin3d\nmetadata:\n  " + guide.Generator + "\n---\nx\n"
	for _, c := range []struct {
		name  string
		files map[string]string // nil: no folder
		want  skillOwner
	}{
		{"absent", nil, skillAbsent},
		{"our SKILL.md", map[string]string{"SKILL.md": ours, "topic.md": "t"}, skillOurs},
		{"our SKILL.md beside the marker", map[string]string{"SKILL.md": ours, skillMarker: markerText}, skillOurs},
		{"only the marker", map[string]string{skillMarker: markerText}, skillOurs},
		{"empty", map[string]string{}, skillFree},
		{"somebody else's SKILL.md", map[string]string{"SKILL.md": foreignSkill}, skillForeign},
		{"somebody else's SKILL.md beside the marker", map[string]string{"SKILL.md": foreignSkill, skillMarker: markerText}, skillForeign},
		{"the marker beside another file", map[string]string{skillMarker: markerText, "notes.txt": "mine"}, skillForeign},
		{"a marker file with other text", map[string]string{skillMarker: "mine\n"}, skillForeign},
		{"files without a SKILL.md", map[string]string{"notes.txt": "mine"}, skillForeign},
	} {
		dir := filepath.Join(root, strings.ReplaceAll(c.name, " ", "_"), guide.Name)
		if c.files != nil {
			writeFiles(t, dir, c.files)
		}
		if got := ownerOf(dir); got != c.want {
			t.Errorf("%s: owner %d, want %d", c.name, got, c.want)
		}
	}
	// A folder named like the marker is not the marker.
	dir := filepath.Join(root, "marker_folder", guide.Name)
	if err := os.MkdirAll(filepath.Join(dir, skillMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(dir); got != skillForeign {
		t.Errorf("a folder named %s: owner %d, want foreign", skillMarker, got)
	}
}

// A removal that cannot delete the emptied folder itself (a shell's working
// directory) leaves the marker in it and fails: the folder stays ours, the
// next removal retries and goes, and install writes over it.
func TestAFailedFolderRemovalLeavesTheMarker(t *testing.T) {
	project := t.TempDir()
	scope := harness.ProjectScopeDir(project)
	dir := filepath.Dir(skillPath(project, folderClaude))
	var out bytes.Buffer
	lockDir := func() func() {
		restore := removePath
		failOn(t, &removePath, func(p string) error {
			if p == dir {
				return errLocked
			}
			return restore(p)
		})
		return func() { removePath = restore }
	}
	marked := func(step string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != skillMarker {
			t.Fatalf("%s: want only the marker left: %v %v", step, entries, err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, skillMarker)); string(got) != markerText {
			t.Fatalf("%s: marker holds %q", step, got)
		}
		if !ownSkill(dir) {
			t.Fatalf("%s: the marked folder is not ours", step)
		}
	}

	installSkills(&out, scope, []harness.ID{"claude-code"}, false, false)
	unlock := lockDir()
	if code := removeOwnSkills(&out, []string{dir}, false); code != 1 || !strings.Contains(out.String(), "[fail] "+dir) {
		t.Fatalf("code %d, want 1 with a [fail] line:\n%s", code, out.String())
	}
	marked("first removal")
	// A retry while the folder is still locked fails the same way.
	out.Reset()
	if code := removeOwnSkills(&out, []string{dir}, false); code != 1 {
		t.Fatalf("retry: code %d, want 1", code)
	}
	marked("retry")
	unlock()
	out.Reset()
	if code := removeOwnSkills(&out, []string{dir}, false); code != 0 || !strings.Contains(out.String(), "removed the guide skill "+dir) {
		t.Fatalf("removal once unlocked: code %d\n%s", code, out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there (%v)", err)
	}

	// install over the marked folder writes the guide; the marker goes.
	installSkills(&out, scope, []harness.ID{"claude-code"}, false, false)
	unlock = lockDir()
	removeOwnSkills(&out, []string{dir}, false)
	unlock()
	marked("removal before install")
	plan := planSkills(scope, []harness.ID{"claude-code"}, false)
	if len(plan.Rows) != 1 || plan.Rows[0].State != skillWritten || !isOurs(t, dir) {
		t.Fatalf("install over the marked folder: %+v", plan.Rows)
	}
	if _, err := os.Stat(filepath.Join(dir, skillMarker)); !os.IsNotExist(err) {
		t.Errorf("the marker is in the new copy (%v)", err)
	}
	for _, side := range []string{".new", ".old"} {
		if _, err := os.Stat(dir + side); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", dir+side)
		}
	}

	// In the home, update and uninstall find a marked folder.
	home := testHome(t)
	agents := filepath.Dir(skillPath(home, folderAgents))
	writeFiles(t, agents, map[string]string{skillMarker: markerText})
	t.Cleanup(func() { os.RemoveAll(filepath.Join(home, ".agents")) })
	if got := ownUserSkills(home); len(got) != 1 || got[0] != agents {
		t.Errorf("own user skills = %v, want %s", got, agents)
	}
}

// An empty rodin3d folder is anybody's: install and the re-write in
// removeSkills write into it; update, prune and uninstall leave it.
func TestAnEmptyFolderIsFree(t *testing.T) {
	home := testHome(t)
	claude := filepath.Dir(skillPath(home, folderClaude))
	agents := filepath.Dir(skillPath(home, folderAgents))
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(home, ".claude"))
		os.RemoveAll(filepath.Join(home, ".agents"))
	})
	empty := func(dir, step string) {
		t.Helper()
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("%s: %s was touched: %v %v", step, dir, entries, err)
		}
	}
	for _, dir := range []string{claude, agents} {
		writeFiles(t, dir, nil)
	}
	var out bytes.Buffer

	// uninstall lists only the copies that are ours; update rewrites them.
	if got := ownUserSkills(home); len(got) != 0 {
		t.Errorf("own user skills = %v, want none", got)
	}
	if code := RefreshSkills(&out, false); code != 0 || strings.Contains(out.String(), "updated") {
		t.Errorf("refresh: code %d\n%s", code, out.String())
	}
	empty(claude, "refresh")
	empty(agents, "refresh")

	// removeSkills does not remove it.
	out.Reset()
	if code := removeSkills(&out, harness.Scope{}, []harness.ID{"codex"}, nil, false); code != 0 || out.Len() != 0 {
		t.Errorf("removeSkills: code %d\n%s", code, out.String())
	}
	empty(agents, "removeSkills")

	// install writes into an empty folder; prune leaves the empty one in
	// the folder no client reads.
	out.Reset()
	if code := installSkills(&out, harness.Scope{}, []harness.ID{"claude-code"}, false, true); code != 0 {
		t.Fatalf("install: code %d\n%s", code, out.String())
	}
	if !isOurs(t, claude) || strings.Contains(out.String(), "removed") {
		t.Fatalf("install over the empty folder:\n%s", out.String())
	}
	empty(agents, "prune")
	for _, side := range []string{".new", ".old"} {
		if _, err := os.Stat(claude + side); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", claude+side)
		}
	}

	// removeSkills' re-write gives a remaining client a copy in its empty
	// folder (Cursor without Claude Code reads .agents).
	out.Reset()
	if code := removeSkills(&out, harness.Scope{}, []harness.ID{"claude-code"}, []harness.ID{"cursor"}, false); code != 0 {
		t.Fatalf("removeSkills: code %d\n%s", code, out.String())
	}
	if _, err := os.Stat(claude); !os.IsNotExist(err) {
		t.Errorf("the removed client's copy is still there (%v)", err)
	}
	if !isOurs(t, agents) || !strings.Contains(out.String(), "wrote the guide skill to "+agents) {
		t.Errorf("cursor got no copy in its empty folder:\n%s", out.String())
	}
}

// A write clears what an earlier one left aside: an empty folder (the
// empty one a write replaced) with removePath, a copy aside that holds only
// the marker, and a .new that holds only the marker.
func TestAnEmptyOrMarkedFolderAsideIsCleared(t *testing.T) {
	for _, c := range []struct {
		name, side string
		files      map[string]string
	}{
		{"empty .old", ".old", map[string]string{}},
		{"marked .old", ".old", map[string]string{skillMarker: markerText}},
		{"marked .new", ".new", map[string]string{skillMarker: markerText}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "rodin3d")
			writeOurs(t, dir)
			writeFiles(t, dir+c.side, c.files)
			if err := writeSkillFolder(dir); err != nil {
				t.Fatal(err)
			}
			assertOurs(t, dir, guide.SkillMarkdown())
			if _, err := os.Lstat(dir + c.side); !os.IsNotExist(err) {
				t.Errorf("%s is still there (%v)", dir+c.side, err)
			}
		})
	}

	// An empty folder written over is renamed aside and removed.
	dir := filepath.Join(t.TempDir(), "rodin3d")
	writeFiles(t, dir, nil)
	var removed []string
	restore := removePath
	failOn(t, &removePath, func(p string) error {
		removed = append(removed, p)
		return restore(p)
	})
	if err := writeSkillFolder(dir); err != nil {
		t.Fatal(err)
	}
	assertOurs(t, dir, guide.SkillMarkdown())
	if _, err := os.Lstat(dir + ".old"); !os.IsNotExist(err) {
		t.Errorf("the empty folder renamed aside is still there (%v)", err)
	}
	if len(removed) != 1 || removed[0] != dir+".old" {
		t.Errorf("removePath calls %v, want the empty %s only", removed, dir+".old")
	}
	// A folder aside that is no longer empty when it is removed stays.
	aside := filepath.Join(t.TempDir(), "rodin3d.old")
	writeFiles(t, aside, nil)
	failOn(t, &removePath, func(p string) error {
		writeFiles(t, p, map[string]string{"late.txt": "mine"})
		return restore(p)
	})
	clearAside(aside)
	if _, err := os.Stat(filepath.Join(aside, "late.txt")); err != nil {
		t.Errorf("a file that came into the empty folder aside was removed: %v", err)
	}
}
