package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installShAppend is what install.sh appends to a startup file:
// printf '\n# added by %s installer\n%s\n' "$BIN" "$line".
func installShAppend(home string) string {
	return fmt.Sprintf("\n# added by %s installer\n%s\n", "rodin3d-mcp", `export PATH="`+home+`/.rodin3d-mcp/bin:$PATH"`)
}

func TestProfileEdit(t *testing.T) {
	ours := installShAppend("/home/u")
	export := `export PATH="/home/u/.rodin3d-mcp/bin:$PATH"`
	cases := []struct {
		name, before, after string
		removed             int
	}{
		{"file ending in a newline is restored exactly", "alias ll='ls -l'\n" + ours, "alias ll='ls -l'\n", 2},
		{"file without a final newline", "alias ll='ls -l'" + ours, "alias ll='ls -l'\n", 2},
		{"empty file", ours, "", 2},
		{"entry between user lines", "a\n" + ours + "b\n", "a\nb\n", 2},
		{"two entries", "a\n" + ours + ours, "a\n", 4},
		{"other home", "a\n" + installShAppend("/Users/x"), "a\n", 2},
		{"CRLF lines", strings.ReplaceAll("a\n"+ours, "\n", "\r\n"), "a\r\n", 2},
		{"marker over a line that is not ours", "a\n\n# added by rodin3d-mcp installer\nexport PATH=\"/opt/x:$PATH\"\n", "a\nexport PATH=\"/opt/x:$PATH\"\n", 1},
		{"marker as the last line", "a\n\n# added by rodin3d-mcp installer", "a", 1},
		{"our directory without the marker stays", "a\n" + export + "\n", "a\n" + export + "\n", 0},
		{"another program's marker stays", "a\n\n# added by other-mcp installer\nexport PATH=\"/home/u/.other-mcp/bin:$PATH\"\n", "a\n\n# added by other-mcp installer\nexport PATH=\"/home/u/.other-mcp/bin:$PATH\"\n", 0},
		{"no entry", "a\nb\n", "a\nb\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, removed := profileEdit(strings.Split(c.before, "\n"))
			if got := strings.Join(kept, "\n"); got != c.after {
				t.Errorf("kept %q, want %q", got, c.after)
			}
			if len(removed) != c.removed {
				t.Errorf("removed %q, want %d lines", removed, c.removed)
			}
		})
	}
}

func TestStripProfileFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(file, []byte("a\n"+installShAppend("/home/u")), 0o640); err != nil {
		t.Fatal(err)
	}
	if lines := profileAdditions(file); len(lines) != 2 || lines[0] != profileMarker {
		t.Fatalf("additions %q", lines)
	}
	if err := stripProfile(file); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if string(b) != "a\n" {
		t.Fatalf("after strip %q", b)
	}
	if info, _ := os.Stat(file); runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, want 0640", info.Mode().Perm())
	}
	if profileAdditions(filepath.Join(dir, "none")) != nil {
		t.Fatal("a missing file has additions")
	}
}

func TestStripPathList(t *testing.T) {
	dir := `C:\Users\u\AppData\Local\rodin3d-mcp\bin`
	cases := []struct {
		name, value, want string
		changed           bool
	}{
		{"prepended by install.ps1", dir + `;C:\a;C:\b`, `C:\a;C:\b`, true},
		{"only entry", dir, "", true},
		{"middle", `C:\a;` + dir + `;C:\b`, `C:\a;C:\b`, true},
		{"last", `C:\a;` + dir, `C:\a`, true},
		{"trailing backslash and case", `C:\a;c:\users\U\appdata\local\RODIN3D-MCP\bin\`, `C:\a`, true},
		{"twice", dir + `;C:\a;` + dir, `C:\a`, true},
		{"other entries kept as written", dir + `;C:\a;;%USERPROFILE%\x;`, `C:\a;;%USERPROFILE%\x;`, true},
		{"a longer path stays", `C:\a;` + dir + `\more`, `C:\a;` + dir + `\more`, false},
		{"absent", `C:\a;C:\b`, `C:\a;C:\b`, false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := stripPathList(c.value, dir)
			if got != c.want || changed != c.changed {
				t.Fatalf("got %q %v, want %q %v", got, changed, c.want, c.changed)
			}
		})
	}
}

type fakeUserPath struct {
	value   string
	readErr error
	writes  []string
}

func (f *fakeUserPath) read() (string, error) { return f.value, f.readErr }
func (f *fakeUserPath) write(v string) error {
	f.writes = append(f.writes, v)
	f.value = v
	return nil
}

func TestRemoveUserPathEntry(t *testing.T) {
	if err := removeUserPathEntry(nil, `C:\x`); err == nil {
		t.Fatal("no store, no error")
	}
	failing := &fakeUserPath{readErr: errors.New("denied")}
	if err := removeUserPathEntry(failing, `C:\x`); err == nil || len(failing.writes) != 0 {
		t.Fatal("a read error must fail without writing")
	}
	// Gone since the plan: nothing is written.
	gone := &fakeUserPath{value: `C:\a`}
	if err := removeUserPathEntry(gone, `C:\x`); err != nil || len(gone.writes) != 0 {
		t.Fatalf("err %v, writes %q", err, gone.writes)
	}
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is an installation in a temp folder: the data root with a binary
// in bin (install.sh), a program folder (install.ps1), a profile with
// install.sh's lines and one without, and a user PATH with our entry.
func fixture(t *testing.T, selfIn string) (layout, *fakeUserPath) {
	base := t.TempDir()
	root := filepath.Join(base, "home", ".rodin3d-mcp")
	program := filepath.Join(base, "local", "rodin3d-mcp")
	for _, f := range []string{"rodin.db", "credentials.json", "config.toml", "downloads/abc/model.glb", "previews/p.png", "cache/t/s/a.png", "viewer/cameras.json", "bin/rodin3d-mcp"} {
		write(t, filepath.Join(root, filepath.FromSlash(f)))
	}
	for _, f := range []string{"bin/rodin3d-mcp.exe", "bin/rodin3d-mcp.exe.old-1a2b3c4d", "bin/rodin3d-mcp.exe.new"} {
		write(t, filepath.Join(program, filepath.FromSlash(f)))
	}
	ours := filepath.Join(base, "home", ".bashrc")
	other := filepath.Join(base, "home", ".zshrc")
	if err := os.WriteFile(ours, []byte("a\n"+installShAppend("/home/u")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(program, "bin")
	store := &fakeUserPath{value: userDir + `;C:\a`}
	l := layout{
		Root: root, ProgramDir: program,
		Profiles:    []string{other, ours, filepath.Join(base, "home", ".profile")},
		UserPathDir: userDir, UserPath: store.value,
	}
	switch selfIn {
	case "root":
		l.Self = filepath.Join(root, "bin", "rodin3d-mcp")
	case "program":
		l.Self = filepath.Join(program, "bin", "rodin3d-mcp.exe")
	default:
		l.Self = filepath.Join(base, "build", "rodin3d-mcp")
		write(t, l.Self)
	}
	return l, store
}

func kinds(los []leftover) []leftoverKind {
	out := make([]leftoverKind, len(los))
	for i, lo := range los {
		out[i] = lo.Kind
	}
	return out
}

func TestPlanAndApply(t *testing.T) {
	for _, selfIn := range []string{"root", "program", "elsewhere"} {
		t.Run(selfIn, func(t *testing.T) {
			l, store := fixture(t, selfIn)
			plan := planLeftovers(l)
			want := []leftoverKind{kindData, kindProgram, kindProfile, kindUserPath}
			if selfIn != "elsewhere" {
				want = append(want, kindSelf)
			}
			if fmt.Sprint(kinds(plan)) != fmt.Sprint(want) {
				t.Fatalf("plan kinds %v, want %v", kinds(plan), want)
			}
			if p := plan[2]; !samePath(p.Path, l.Profiles[1]) || len(p.Lines) != 2 {
				t.Fatalf("profile leftover %+v", p)
			}
			if selfIn != "elsewhere" {
				self := plan[len(plan)-1]
				top := l.Root
				if selfIn == "program" {
					top = l.ProgramDir
				}
				if !samePath(self.Path, l.Self) || !samePath(self.Top, top) {
					t.Fatalf("self leftover %+v", self)
				}
			}

			removals := applyLeftovers(plan, store)
			if fmt.Sprint(kinds(leftoversOf(removals))) != fmt.Sprint(want) {
				t.Fatalf("removal order %v, want %v", kinds(leftoversOf(removals)), want)
			}
			for _, r := range removals {
				if r.Err != nil || r.Deferred {
					t.Fatalf("%s: err %v deferred %v", r.Leftover.Path, r.Err, r.Deferred)
				}
			}
			for _, gone := range []string{l.Root, l.ProgramDir} {
				if exists(gone) {
					t.Fatalf("%s is still there", gone)
				}
			}
			if b, _ := os.ReadFile(l.Profiles[1]); string(b) != "a\n" {
				t.Fatalf("profile now %q", b)
			}
			if b, _ := os.ReadFile(l.Profiles[0]); string(b) != "b\n" {
				t.Fatalf("other profile changed to %q", b)
			}
			if store.value != `C:\a` || len(store.writes) != 1 {
				t.Fatalf("user PATH %q after writes %q", store.value, store.writes)
			}
			if selfIn == "elsewhere" && !exists(l.Self) {
				t.Fatal("a binary no installer put there was removed")
			}
			// Nothing left: a second plan is empty.
			l.UserPath = store.value
			if again := planLeftovers(l); len(again) != 0 {
				t.Fatalf("second plan %v", kinds(again))
			}
		})
	}
}

func leftoversOf(rs []removal) []leftover {
	out := make([]leftover, len(rs))
	for i, r := range rs {
		out[i] = r.Leftover
	}
	return out
}

// The data folder step leaves the running binary (and the folders holding
// it) for the last step; everything around it goes.
func TestRemoveTreeExceptKeepsSelf(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".rodin3d-mcp")
	self := filepath.Join(root, "bin", "rodin3d-mcp")
	for _, f := range []string{"rodin.db", "downloads/a/m.glb", "bin/rodin3d-mcp", "bin/rodin3d-mcp.old-1"} {
		write(t, filepath.Join(root, filepath.FromSlash(f)))
	}
	if err := removeTreeExcept(root, self); err != nil {
		t.Fatal(err)
	}
	var left []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 1 || !samePath(left[0], self) {
		t.Fatalf("left %q, want only %s", left, self)
	}
	if r := removeSelf(leftover{Kind: kindSelf, Path: self, Top: root}); r.Err != nil || exists(root) {
		t.Fatalf("self removal %+v, root still there: %v", r, exists(root))
	}
	// Already gone is not an error.
	if r := removeSelf(leftover{Kind: kindSelf, Path: self, Top: root}); r.Err != nil {
		t.Fatalf("second removal %v", r.Err)
	}
	if err := removeTreeExcept(root, self); err != nil {
		t.Fatalf("missing tree: %v", err)
	}
}

// Windows refuses to delete a running program: the removal is deferred and
// the file stays. The running program here is this test binary.
func TestRemoveSelfRunningOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("POSIX deletes a running program")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := removeSelf(leftover{Kind: kindSelf, Path: exe, Top: filepath.Dir(exe)})
	if !r.Deferred || r.Err == nil || !exists(exe) {
		t.Fatalf("removal %+v, exists %v", r, exists(exe))
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".rodin3d-mcp")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(root, "bin", "x"), true},
		{root, false},
		{filepath.Dir(root), false},
		{root + "-other", false},
		{filepath.Join(filepath.Dir(root), "..rodin"), false},
		{filepath.Join(root, "..x"), true},
	}
	for _, c := range cases {
		if got := within(c.path, root); got != c.want {
			t.Errorf("within(%s) = %v, want %v", c.path, got, c.want)
		}
	}
	if runtime.GOOS == "windows" && !within(strings.ToUpper(filepath.Join(root, "bin")), root) {
		t.Error("within must ignore case on Windows")
	}
}
