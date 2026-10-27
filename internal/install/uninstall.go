package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/cli"
	"github.com/sairaph/mcp-wizard/daemon/lock"
	"github.com/sairaph/mcp-wizard/harness"
	"github.com/sairaph/mcp-wizard/installer"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Uninstall removes everything this project put on the computer, in this
// order: sign out of Hyper3D, stop the syncer and the viewer, remove our
// entry from every AI client config, then the files (planLeftovers), the
// running binary last. It lists all of it first and asks in the terminal;
// --yes skips the question, --dry-run only lists. With --scope project it
// removes that project's registrations only.
func Uninstall(ctx context.Context, env Env, cmd cli.Command) int {
	if cmd.Scope == string(harness.ScopeProject) {
		return uninstallProject(ctx, env, cmd)
	}
	svc := env.Service()
	open := true
	closeService := func() {
		if open {
			svc.Close()
			open = false
		}
	}
	defer closeService()

	paths := svc.Paths()
	self, _ := harness.ResolveExecutable()
	store := systemUserPath()
	lay := defaultLayout(paths.Root, self, store)
	name := serverName(cmd)

	regs, err := findRegistrations(ctx, name, lay)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return 1
	}
	creds, _ := svc.Credentials(ctx)
	revokePending := svc.RevokePending()
	signOut := creds != nil || revokePending
	daemonUp := svc.DaemonRunning()
	viewerUp := viewer.Running(paths)
	files := planLeftovers(lay)

	w := env.Stdout
	if !signOut && !daemonUp && !viewerUp && len(regs) == 0 && len(files) == 0 {
		fmt.Fprintf(w, "  Nothing to remove: %s is not installed on this computer.\n", domain.BinaryName)
		printNotOurs(w, lay)
		return 0
	}

	// --- the list ---
	fmt.Fprintf(w, "\n  %s uninstall removes:\n\n", domain.BinaryName)
	if signOut {
		what := "revoke the Hyper3D grant, delete the tokens"
		if creds != nil && creds.Username != "" {
			what = "sign out " + creds.Username + ": " + what
		} else if creds == nil {
			what = "revoke the grant an earlier logout could not revoke"
		}
		row(w, "Hyper3D sign-in", what)
	}
	if daemonUp {
		row(w, "Background syncer", "stop it")
	}
	if viewerUp {
		row(w, "Viewer window", "close it")
	}
	for _, r := range regs {
		for _, h := range r.harnesses {
			row(w, "AI client", fmt.Sprintf("%s: the %q entry in %s", h.Name, name, h.ConfigPath))
		}
	}
	for _, lo := range files {
		row(w, leftoverLabel(lo), planText(lo, lay))
	}
	fmt.Fprintf(w, "\n  Registrations made with `%s add` live in each project's own files;\n", domain.BinaryName)
	fmt.Fprintf(w, "  remove one first with `%s uninstall --scope project --dir <project>`.\n", domain.BinaryName)
	printNotOurs(w, lay)

	if cmd.DryRun {
		fmt.Fprintln(w, "\n  Dry run: nothing was removed.")
		return 0
	}
	if !cmd.Yes {
		if !tui.IsInteractive() {
			fmt.Fprintf(env.Stderr, "\n  %s uninstall asks before it removes anything: run it in a terminal, or pass --yes.\n", domain.BinaryName)
			return 2
		}
		fmt.Fprint(w, "\n  Remove all of this? [y/N] ")
		line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Fprintln(w, "  Cancelled: nothing was removed.")
			return 0
		}
	}
	fmt.Fprintln(w)
	failures := 0

	// 1. Sign out first, while the tokens are still there to revoke.
	if signOut {
		res, err := svc.SignOut(ctx)
		if err != nil {
			failures++
			row(env.Stderr, "Hyper3D sign-in", "sign-out failed ("+err.Error()+"); the grant is not revoked, and the tokens go with the data folder")
		} else {
			row(w, "Hyper3D sign-in", signOutText(res))
		}
	}

	// 2. Stop what runs from these files.
	if svc.DaemonRunning() {
		if err := stopDaemon(svc); err != nil {
			failures++
			row(env.Stderr, "Background syncer", "could not stop it: "+err.Error())
		} else {
			row(w, "Background syncer", "stopped")
		}
	}
	if viewer.Running(paths) {
		if _, err := svc.CloseViewer(ctx); err != nil {
			failures++
			row(env.Stderr, "Viewer window", "could not close it: "+service.Classify(err).Message)
		} else {
			row(w, "Viewer window", "closed")
		}
	}

	// 3. Our entry in each client config, nothing else in those files.
	var applied []harness.Result
	byName := map[harness.ID]harness.Harness{}
	for _, r := range regs {
		ids := make([]harness.ID, 0, len(r.harnesses))
		for _, h := range r.harnesses {
			ids = append(ids, h.ID)
			byName[h.ID] = h
		}
		for _, res := range r.detector.ApplyIn(ctx, harness.Scope{}, ids, harness.Absent, harness.ConflictError) {
			applied = append(applied, res)
			switch res.State {
			case harness.Applied, harness.ApplyNoop:
				row(w, "AI client", res.Name+": entry removed")
			default:
				failures++
				row(env.Stderr, "AI client", fmt.Sprintf("%s: %s %s", res.Name, res.State, res.Reason))
			}
		}
	}

	// 4-8. The files, the running binary last.
	closeService()
	removals := applyLeftovers(files, store)
	var deferred *removal
	for i, r := range removals {
		if r.Deferred {
			deferred = &removals[i]
		}
	}
	for _, r := range removals {
		label := leftoverLabel(r.Leftover)
		switch {
		case r.Deferred:
		case r.Err != nil:
			failures++
			row(env.Stderr, label, "could not be removed: "+r.Err.Error())
		case deferred != nil && samePath(r.Leftover.Path, deferred.Leftover.Top):
			row(w, label, "emptied "+r.Leftover.Path+" except the running program (below)")
		case r.Leftover.Kind == kindProfile:
			row(w, label, "removed the installer's PATH lines from "+r.Leftover.Path)
		case r.Leftover.Kind == kindUserPath:
			row(w, label, "removed "+r.Leftover.Path+" from your user PATH")
		default:
			row(w, label, "removed "+r.Leftover.Path)
		}
	}
	installer.PrintReloadHints(w, applied, byName)

	if deferred != nil {
		fmt.Fprintf(w, "\n  Left behind: %s\n", deferred.Leftover.Path)
		fmt.Fprintln(w, "  Windows cannot delete a running program. Once this command has ended, delete its folder:")
		fmt.Fprintf(w, "    Remove-Item -Recurse -Force '%s'\n", deferred.Leftover.Top)
	}
	if failures > 0 {
		fmt.Fprintf(w, "\n  Some of it could not be removed (above). Close the programs still using it, such as\n  AI clients that run %s, and run `%s uninstall` again.\n", domain.BinaryName, domain.BinaryName)
	}
	skip := ""
	if deferred != nil {
		skip = deferred.Leftover.Path
	}
	for _, p := range stillOnPath(skip) {
		fmt.Fprintf(w, "\n  Another %s is still on your PATH: %s\n  No installer of this project put it there, so it stays; delete it if you no longer want it.\n", domain.BinaryName, p)
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// uninstallProject removes the registrations of one project.
func uninstallProject(ctx context.Context, env Env, cmd cli.Command) int {
	dir, err := projectDir(cmd)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return 1
	}
	detector, err := NewDetector(serverName(cmd))
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return 1
	}
	return runUnattended(ctx, env, detector, harness.ProjectScopeDir(dir), cmd, harness.Absent)
}

// registration is the clients whose entry runs one of our binaries.
type registration struct {
	detector  *harness.Detector
	harnesses []harness.Harness
}

// findRegistrations finds the client entries that are ours: name with `mcp`
// as its argument and a command that is this binary or a path an installer
// writes the binary to. An entry under our name that runs anything else is
// not ours and stays.
func findRegistrations(ctx context.Context, name string, l layout) ([]registration, error) {
	var commands []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, c := range commands {
			if c == p {
				return
			}
		}
		commands = append(commands, p)
	}
	add(l.Self)
	for _, b := range l.installedBinaries() {
		if r, err := filepath.EvalSymlinks(b); err == nil {
			b = r
		}
		add(b)
	}
	var out []registration
	for _, c := range commands {
		d, err := harness.New(harness.ServerSpec{Name: name, Command: c, Args: []string{"mcp"}, Env: domain.DefaultEnv()})
		if err != nil {
			return nil, err
		}
		var hs []harness.Harness
		for _, h := range d.Detect(ctx) {
			if h.Configured {
				hs = append(hs, h)
			}
		}
		if len(hs) > 0 {
			out = append(out, registration{detector: d, harnesses: hs})
		}
	}
	return out, nil
}

func row(w io.Writer, label, text string) { fmt.Fprintf(w, "  %-20s %s\n", label, text) }

func leftoverLabel(lo leftover) string {
	switch lo.Kind {
	case kindData:
		return "Data folder"
	case kindProgram:
		return "Program folder"
	case kindProfile:
		return "Shell profile"
	case kindUserPath:
		return "Windows PATH"
	default:
		return "This program"
	}
}

func planText(lo leftover, l layout) string {
	switch lo.Kind {
	case kindData:
		what := "task index, downloads, previews, cache, config, sign-in, logs"
		if runtime.GOOS != "windows" {
			what += ", the program in bin"
		}
		return lo.Path + " (" + what + ")"
	case kindProgram:
		return lo.Path + " (the program)"
	case kindProfile:
		return lo.Path + ": " + strings.Join(lo.Lines, " / ")
	case kindUserPath:
		return lo.Path + " (entry of your user PATH)"
	}
	text := lo.Path + " (running now; removed last)"
	if runtime.GOOS == "windows" {
		text += "\n" + strings.Repeat(" ", 23) + "Windows cannot delete a running program: it stays, and the end says how to delete it"
	}
	return text
}

// printNotOurs says that a binary running from where no installer put it
// is not removed.
func printNotOurs(w io.Writer, l layout) {
	if l.Self != "" && l.selfTop() == "" {
		fmt.Fprintf(w, "\n  Not removed: %s, the copy running now (no installer of this project put it there).\n", l.Self)
	}
}

func signOutText(res auth.LogoutResult) string {
	switch {
	case res.RevokePending:
		return fmt.Sprintf("signed out; Hyper3D could not be reached to revoke the grant (%v). Its token goes with the data folder, so nothing here can use it; the grant ends when that token lapses", res.RevokeError)
	case res.RevokeError != nil:
		return fmt.Sprintf("signed out; Hyper3D refused to revoke a grant (%v), which a retry would not change; it ends when its refresh token lapses", res.RevokeError)
	case res.Revoked:
		return "signed out: the grant is revoked and the tokens are deleted"
	}
	return "not signed in"
}

// stopDaemon stops the syncer and waits for it to end (10 s at most), so
// none of its files is still open when they are removed.
func stopDaemon(svc *service.Service) error {
	if err := svc.StopDaemon(); err != nil && !errors.Is(err, lock.ErrNotRunning) {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for svc.DaemonRunning() {
		if time.Now().After(deadline) {
			return errors.New("it is still running after 10 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// stillOnPath lists the copies of the binary still found on PATH, except
// skip (the running binary already named as left behind).
func stillOnPath(skip string) []string {
	name := domain.BinaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || (skip != "" && samePath(p, skip)) {
			continue
		}
		dup := false
		for _, o := range out {
			if samePath(o, p) {
				dup = true
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}
