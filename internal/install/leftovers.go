package install

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// What uninstall removes from disk, and in which order. The shipped
// installers put files in exactly these places:
//
//   - install.sh: the binary in ~/.rodin3d-mcp/bin (inside the data root), and
//     a marker line plus an export line appended to the first existing one of
//     ~/.zshrc, ~/.bashrc, ~/.profile, ~/.bash_profile.
//   - install.ps1: the binary in %LOCALAPPDATA%\rodin3d-mcp\bin (with its
//     .new download and .old-* copies moved aside), and that bin folder
//     prepended to the user PATH in the registry.
//
// Nothing else is looked for: a copy somewhere else is reported, not removed.

type leftoverKind int

const (
	kindData     leftoverKind = iota // the data root
	kindProgram                      // the Windows program folder of install.ps1
	kindProfile                      // the PATH lines install.sh added to a startup file
	kindUserPath                     // the Windows user PATH entry of install.ps1
	kindSelf                         // the running binary, removed last
)

// leftover is one thing uninstall removes.
type leftover struct {
	Kind leftoverKind
	// Path is the folder or file; for kindUserPath, the PATH entry.
	Path string
	// Lines are the lines a kindProfile removal takes out, trimmed.
	Lines []string
	// Top is the folder the running binary is in (data root or program
	// folder): emptied folders up to it go after the binary.
	Top string
}

// layout is where this machine's installation lives.
type layout struct {
	Root       string   // the data root, ~/.rodin3d-mcp
	ProgramDir string   // Windows: %LOCALAPPDATA%\rodin3d-mcp; "" elsewhere
	Profiles   []string // the startup files install.sh appends to
	Self       string   // the running executable, symlinks resolved
	// UserPathDir is the entry install.ps1 adds to the user PATH ("" when
	// not on Windows) and UserPath the user PATH value read at plan time.
	UserPathDir string
	UserPath    string
}

// defaultLayout is the layout of the shipped installers on this OS. A user
// PATH that cannot be read is treated as empty (nothing to remove there).
func defaultLayout(root, self string, store userPathStore) layout {
	l := layout{Root: root, Self: self}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		// install.ps1: $env:LOCALAPPDATA, else $env:USERPROFILE\AppData\Local.
		local := os.Getenv("LOCALAPPDATA")
		if local == "" && home != "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		if local != "" {
			l.ProgramDir = filepath.Join(local, domain.Repo)
			l.UserPathDir = filepath.Join(l.ProgramDir, "bin")
			if store != nil {
				l.UserPath, _ = store.read()
			}
		}
		return l
	}
	if home != "" {
		for _, name := range []string{".zshrc", ".bashrc", ".profile", ".bash_profile"} {
			l.Profiles = append(l.Profiles, filepath.Join(home, name))
		}
	}
	return l
}

// installedBinaries are the paths the shipped installers write the binary
// to, whether or not a file is there now: a client entry naming one of them
// is ours.
func (l layout) installedBinaries() []string {
	var out []string
	if runtime.GOOS == "windows" {
		if l.ProgramDir != "" {
			out = append(out, filepath.Join(l.ProgramDir, "bin", domain.BinaryName+".exe"))
		}
		return out
	}
	return append(out, filepath.Join(l.Root, "bin", domain.BinaryName))
}

// planLeftovers lists what is on disk to remove, in removal order, the
// running binary last.
func planLeftovers(l layout) []leftover {
	var plan []leftover
	if exists(l.Root) {
		plan = append(plan, leftover{Kind: kindData, Path: l.Root})
	}
	if l.ProgramDir != "" && exists(l.ProgramDir) {
		plan = append(plan, leftover{Kind: kindProgram, Path: l.ProgramDir})
	}
	for _, file := range l.Profiles {
		if lines := profileAdditions(file); len(lines) > 0 {
			plan = append(plan, leftover{Kind: kindProfile, Path: file, Lines: lines})
		}
	}
	if l.UserPathDir != "" {
		if _, changed := stripPathList(l.UserPath, l.UserPathDir); changed {
			plan = append(plan, leftover{Kind: kindUserPath, Path: l.UserPathDir})
		}
	}
	if top := l.selfTop(); top != "" && exists(l.Self) {
		plan = append(plan, leftover{Kind: kindSelf, Path: l.Self, Top: top})
	}
	return plan
}

// selfTop is the installation folder the running binary is in, or "" when
// it runs from somewhere no installer wrote (a build of its own).
func (l layout) selfTop() string {
	if l.Self == "" {
		return ""
	}
	for _, top := range []string{l.Root, l.ProgramDir} {
		if top != "" && within(l.Self, top) {
			return top
		}
	}
	return ""
}

// removal is what happened to one leftover.
type removal struct {
	Leftover leftover
	Err      error
	// Deferred is the running binary on Windows, which cannot delete a
	// running program: it stays, and the summary names it.
	Deferred bool
}

// applyLeftovers removes the plan in its order; the running binary is
// skipped inside the folders and removed after everything else. store is
// the user PATH (nil when not on Windows).
func applyLeftovers(plan []leftover, store userPathStore) []removal {
	keep := ""
	for _, lo := range plan {
		if lo.Kind == kindSelf {
			keep = lo.Path
		}
	}
	out := make([]removal, 0, len(plan))
	var self *leftover
	for i := range plan {
		lo := plan[i]
		var err error
		switch lo.Kind {
		case kindSelf:
			self = &plan[i]
			continue
		case kindData, kindProgram:
			err = removeTreeExcept(lo.Path, keep)
		case kindProfile:
			err = stripProfile(lo.Path)
		case kindUserPath:
			err = removeUserPathEntry(store, lo.Path)
		}
		out = append(out, removal{Leftover: lo, Err: err})
	}
	if self != nil {
		out = append(out, removeSelf(*self))
	}
	return out
}

// removeSelf deletes the running binary, then the folders it leaves empty up
// to its installation folder.
func removeSelf(lo leftover) removal {
	err := os.Remove(lo.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return removal{Leftover: lo, Err: err, Deferred: runtime.GOOS == "windows"}
	}
	for dir := filepath.Dir(lo.Path); ; dir = filepath.Dir(dir) {
		if !within(dir, lo.Top) && !samePath(dir, lo.Top) {
			break
		}
		if os.Remove(dir) != nil || samePath(dir, lo.Top) {
			break
		}
	}
	return removal{Leftover: lo}
}

// removeTreeExcept removes dir and everything in it except keep (and the
// folders holding keep).
func removeTreeExcept(dir, keep string) error {
	if keep == "" || !within(keep, dir) {
		return os.RemoveAll(dir)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case samePath(p, keep):
			continue
		case within(keep, p):
			err = removeTreeExcept(p, keep)
		default:
			err = os.RemoveAll(p)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// --- shell startup files (install.sh) ---

// profileMarker is the comment install.sh writes above the PATH line it
// adds; keep the two in step.
const profileMarker = "# added by " + domain.BinaryName + " installer"

// isOurExport is the line install.sh writes under its marker:
// export PATH="<home>/.rodin3d-mcp/bin:$PATH".
func isOurExport(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, `export PATH="`) && strings.Contains(t, "/."+domain.BinaryName+"/bin:")
}

// profileEdit splits a startup file's lines into those that stay and those
// install.sh added: each marker line, the export line right under it, and
// the blank line install.sh writes before the marker. Listing and rewriting
// share it, so what uninstall promises is what it removes.
func profileEdit(lines []string) (kept, removed []string) {
	kept = make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != profileMarker {
			kept = append(kept, lines[i])
			continue
		}
		if n := len(kept); n > 0 && strings.TrimSpace(kept[n-1]) == "" {
			kept = kept[:n-1]
		}
		removed = append(removed, strings.TrimSpace(lines[i]))
		if i+1 < len(lines) && isOurExport(lines[i+1]) {
			removed = append(removed, strings.TrimSpace(lines[i+1]))
			i++
		}
	}
	return kept, removed
}

// profileAdditions are the lines install.sh added to file (none when it
// cannot be read).
func profileAdditions(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	_, removed := profileEdit(strings.Split(string(b), "\n"))
	return removed
}

// stripProfile rewrites file without install.sh's lines, keeping its mode.
func stripProfile(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	kept, removed := profileEdit(strings.Split(string(b), "\n"))
	if len(removed) == 0 {
		return nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(strings.Join(kept, "\n")), info.Mode().Perm())
}

// --- the Windows user PATH (install.ps1) ---

// userPathStore reads and writes the persisted user PATH value.
type userPathStore interface {
	read() (string, error)
	// write stores the value; "" deletes it.
	write(string) error
}

// stripPathList removes every entry equal to dir (case-insensitive, any
// trailing separator) from a ;-separated PATH value, keeping every other
// entry as it was.
func stripPathList(value, dir string) (string, bool) {
	if value == "" {
		return value, false
	}
	want := strings.TrimRight(dir, `\/`)
	parts := strings.Split(value, ";")
	kept := parts[:0:0]
	changed := false
	for _, p := range parts {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(p), `\/`), want) {
			changed = true
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ";"), changed
}

// removeUserPathEntry takes dir out of the user PATH, read again now.
func removeUserPathEntry(store userPathStore, dir string) error {
	if store == nil {
		return errors.New("the user PATH cannot be changed on this system")
	}
	value, err := store.read()
	if err != nil {
		return err
	}
	next, changed := stripPathList(value, dir)
	if !changed {
		return nil
	}
	return store.write(next)
}

// --- paths ---

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// samePath compares cleaned paths, ignoring case on Windows.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether path is strictly inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
