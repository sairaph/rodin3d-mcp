// Package install registers the MCP server with the AI clients on this
// machine and runs the setup wizard: pick the clients, sign in to Hyper3D,
// set the write permissions, register (mcp-wizard's installer and flow
// steps around two steps of this package). Unattended runs register only.
// Uninstall (uninstall.go) removes everything the installers put here.
package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sairaph/mcp-wizard/cli"
	"github.com/sairaph/mcp-wizard/flow"
	"github.com/sairaph/mcp-wizard/harness"
	"github.com/sairaph/mcp-wizard/installer"
	"github.com/sairaph/mcp-wizard/tui"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// Env is what install needs from the program.
type Env struct {
	Service func() *service.Service
	Stdin   io.Reader // uninstall's question
	Stdout  io.Writer
	Stderr  io.Writer
}

// State is shared by the wizard's steps.
type State struct {
	flow.BaseState
	Harness installer.HarnessState
	Results installer.ResultsState
	// SignedInAs is the account the sign-in step ended with ("" when skipped).
	SignedInAs string
	// PermissionsSaved is true once the permissions step wrote config.toml.
	PermissionsSaved bool
}

func harnessState(s *State) *installer.HarnessState { return &s.Harness }
func resultsState(s *State) *installer.ResultsState { return &s.Results }

// NewDetector finds the AI clients and registers `<this binary> mcp` under
// name.
func NewDetector(name string) (*harness.Detector, error) {
	exe, err := harness.ResolveExecutable()
	if err != nil {
		return nil, err
	}
	return harness.New(harness.ServerSpec{
		Name:    name,
		Command: exe,
		Args:    []string{"mcp"},
		Env:     domain.DefaultEnv(),
	})
}

func serverName(cmd cli.Command) string {
	if cmd.ServerName != "" {
		return cmd.ServerName
	}
	return domain.ServerName
}

// Install is install and configure: the wizard in a terminal, otherwise (or
// with --yes) registration only.
func Install(ctx context.Context, env Env, cmd cli.Command) int {
	if cmd.Scope == string(harness.ScopeProject) {
		return Add(ctx, env, cmd)
	}
	detector, err := NewDetector(serverName(cmd))
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return 1
	}
	if tui.IsInteractive() && !cmd.Yes {
		return runWizard(ctx, env, detector, harness.Scope{}, cmd, true)
	}
	return runUnattended(ctx, env, detector, harness.Scope{}, cmd, harness.Present)
}

func projectDir(cmd cli.Command) (string, error) {
	dir := cmd.Dir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = cwd
	}
	return filepath.Abs(dir)
}

// Add registers the server in one project's client configs. Sign-in and
// permissions belong to the user, not the project, so its wizard registers
// only and names the commands for them.
func Add(ctx context.Context, env Env, cmd cli.Command) int {
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
	scope := harness.ProjectScopeDir(dir)
	if tui.IsInteractive() && !cmd.Yes {
		return runWizard(ctx, env, detector, scope, cmd, false)
	}
	return runUnattended(ctx, env, detector, scope, cmd, harness.Present)
}

// selectIDs picks the harnesses to act on. --clients wins, then --all, and by
// default every selectable client that is not configured yet. The returned
// reason explains an empty selection.
func selectIDs(harnesses []harness.Harness, cmd cli.Command) (ids []harness.ID, reason string) {
	if len(cmd.Clients) > 0 {
		var unknown []string
		for _, want := range cmd.Clients {
			found := false
			for _, h := range harnesses {
				if strings.EqualFold(string(h.ID), want) && h.Selectable() {
					ids = append(ids, h.ID)
					found = true
				}
			}
			if !found {
				unknown = append(unknown, want)
			}
		}
		if len(unknown) > 0 {
			return nil, "no detected client matches --clients " + strings.Join(unknown, ",")
		}
		return ids, ""
	}
	candidates := 0
	for _, h := range harnesses {
		if !h.Selectable() || !h.Relevant() {
			continue
		}
		candidates++
		if cmd.All || !h.Configured {
			ids = append(ids, h.ID)
		}
	}
	if len(ids) == 0 && candidates > 0 {
		return nil, "every detected client is already configured (use --all to re-register)"
	}
	return ids, ""
}

func exitCodeFor(results []harness.Result) int {
	for _, r := range results {
		if r.State == harness.ApplyFailed {
			return 1
		}
	}
	return 0
}

func byID(harnesses []harness.Harness) map[harness.ID]harness.Harness {
	m := make(map[harness.ID]harness.Harness, len(harnesses))
	for _, h := range harnesses {
		m[h.ID] = h
	}
	return m
}

// runWizard drives the interactive setup. The global install also signs in
// and sets the permissions; a project's setup registers only.
func runWizard(ctx context.Context, env Env, detector *harness.Detector, scope harness.Scope, cmd cli.Command, account bool) int {
	svc := env.Service()
	defer svc.Close()
	title := domain.BinaryName + " setup"
	if scope.IsProject() {
		title = domain.BinaryName + " project setup"
	}
	tui.DefaultTheme.Copy.Title = title

	state := &State{}
	steps := []flow.Step[State]{
		harnessStep{installer.HarnessStep(ctx, detector, harnessState, installer.HarnessStepOptions{AllDetected: true, Scope: scope})},
	}
	if account && !cmd.DryRun {
		steps = append(steps,
			&signInStep{signIn: NewSignIn(ctx, svc, "Skip: sign in later with `"+domain.BinaryName+" login`")},
			&permissionsStep{perms: NewPermissions(svc.Paths().Config)},
		)
	}
	steps = append(steps, installer.ApplyStep(ctx, detector, harnessState, resultsState, installer.ApplyStepOptions{Scope: scope, DryRun: cmd.DryRun}))
	f := flow.New(steps, state)
	code := tui.Run(ctx, f, tui.Options{Title: title})
	return finishWizard(env, svc, state, scope, cmd, code)
}

// finishWizard is what the wizard leaves in the terminal once its screens
// are gone, and its exit code: the guide skill for the clients it
// registered, then the next steps or why nothing was registered.
func finishWizard(env Env, svc *service.Service, state *State, scope harness.Scope, cmd cli.Command, code int) int {
	if state.Failure != nil {
		fmt.Fprintln(env.Stderr, state.Failure)
	}
	// The guide skill goes to the clients the wizard registered (a dry run
	// lists where), once the registration step has run.
	skillCode := 0
	if state.Results.Done {
		skillCode = installSkills(env.Stdout, scope, selectedIDs(state.Harness.Selected), cmd.DryRun, false)
	}
	switch {
	case cmd.DryRun:
	case !state.Results.Done:
		fmt.Fprintln(env.Stdout, "\n  Setup cancelled: no AI client was registered.")
		if state.SignedInAs != "" || state.PermissionsSaved {
			fmt.Fprintln(env.Stdout, "  The sign-in and permissions made before cancelling are kept.")
		}
	case code == 0:
		printNext(env.Stdout, svc)
	}
	return max(code, skillCode)
}

// printNext tells what is still to do after a registration: sign in, and
// where the permissions are set.
func printNext(w io.Writer, svc *service.Service) {
	if c, err := svc.Credentials(context.Background()); err != nil || c.State() != auth.StateSignedIn {
		fmt.Fprintf(w, "\n  Sign in to Hyper3D with:\n    %s login\n", domain.BinaryName)
	}
	cfg, err := svc.Config()
	if err != nil {
		fmt.Fprintf(w, "\n  %s\n", err)
		return
	}
	if len(cfg.Permission.Denied()) > 0 {
		return
	}
	fmt.Fprintf(w, "\n  Every paid or changing operation is allowed. Deny any of them under\n  Permissions in `%s` (the app), or edit %s.\n", domain.BinaryName, svc.Paths().Config)
}

func runUnattended(ctx context.Context, env Env, detector *harness.Detector, scope harness.Scope, cmd cli.Command, desired harness.DesiredState) int {
	enabling := desired == harness.Present
	harnesses := detector.DetectIn(ctx, scope)

	var ids []harness.ID
	if enabling {
		var reason string
		ids, reason = selectIDs(harnesses, cmd)
		if reason != "" {
			fmt.Fprintf(env.Stderr, "  %s\n", reason)
			if len(cmd.Clients) > 0 {
				return 2
			}
			// Every client is registered already; the guide skill is
			// still written for them.
			return installSkills(env.Stdout, scope, skillClients(harnesses, nil), cmd.DryRun, true)
		}
	} else {
		for _, h := range harnesses {
			if h.Configured {
				ids = append(ids, h.ID)
			}
		}
	}
	if len(ids) == 0 {
		if enabling {
			installer.PrintNoClients(env.Stdout, domain.BinaryName, false)
		} else {
			fmt.Fprintln(env.Stdout, "  No clients are configured.")
		}
		return 0
	}
	// The guide skill follows the registrations: written for every client
	// that runs this program after an install, removed with a project's
	// registrations (ids is then every client configured in the scope, so
	// no client stays to read a copy).
	skills := func() int {
		if enabling {
			return installSkills(env.Stdout, scope, skillClients(harnesses, ids), cmd.DryRun, true)
		}
		return removeSkills(env.Stdout, scope, ids, nil, cmd.DryRun)
	}
	if cmd.DryRun {
		code := printPlan(ctx, env, detector, scope, ids, desired)
		return max(code, skills())
	}
	policy := harness.ConflictReplace
	if !enabling {
		policy = harness.ConflictError
	}
	results := detector.ApplyIn(ctx, scope, ids, desired, policy)
	installer.PrintResultsWithScope(env.Stdout, results, scope, enabling, false)
	installer.PrintReloadHints(env.Stdout, results, byID(harnesses))
	code := max(exitCodeFor(results), skills())
	if enabling {
		svc := env.Service()
		printNext(env.Stdout, svc)
		svc.Close()
	}
	return code
}

func printPlan(ctx context.Context, env Env, detector *harness.Detector, scope harness.Scope, ids []harness.ID, desired harness.DesiredState) int {
	policy := harness.ConflictReplace
	if desired == harness.Absent {
		policy = harness.ConflictError
	}
	changes, err := detector.PlanResultsIn(ctx, scope, ids, desired, policy)
	if err != nil {
		fmt.Fprintln(env.Stderr, err)
		return 1
	}
	installer.PrintChanges(env.Stdout, changes, scope)
	return 0
}

// --- wizard steps ---

// harnessStep is mcp-wizard's client list with a correct view. Its View
// (mcp-wizard v0.1.1 installer/step_harness.go:213) shows every client that
// has an entry in HarnessState.Selected as selected, whether the entry is
// true or false; space and a set entries to false, so a client turned off
// still showed selected while registration (which reads the value) skipped
// it. Dropping the false entries after every update keeps what the map
// means (a missing entry is false) and makes the view show it.
type harnessStep struct{ flow.Step[State] }

func (s harnessStep) Update(msg tea.Msg, state *State) (flow.Directive, tea.Cmd) {
	d, cmd := s.Step.Update(msg, state)
	for id, on := range state.Harness.Selected {
		if !on {
			delete(state.Harness.Selected, id)
		}
	}
	return d, cmd
}

// signInStep is the sign-in screen as a flow step.
type signInStep struct{ signIn *SignIn }

func (s *signInStep) ID() string { return "signin" }

func (s *signInStep) Title(*State) string { return "Sign in to Hyper3D" }

func (s *signInStep) Hints(*State) []struct{ Key, Label string } { return hintPairs(s.signIn.Hints()) }

func (s *signInStep) Init(state *State) tea.Cmd {
	s.signIn.SetSize(state.Width, state.Height)
	return s.signIn.Init()
}

func (s *signInStep) Update(msg tea.Msg, state *State) (flow.Directive, tea.Cmd) {
	if _, ok := msg.(tea.QuitMsg); ok {
		s.signIn.Cancel()
		return flow.Quit, nil
	}
	s.signIn.SetSize(state.Width, state.Height)
	ev, cmd := s.signIn.Update(msg)
	switch ev {
	case SignInDone:
		state.SignedInAs = s.signIn.User()
		return flow.Next, cmd
	case SignInSkipped:
		return flow.Next, cmd
	case SignInBack:
		return flow.Back, cmd
	case SignInQuit:
		return flow.Quit, cmd
	}
	return flow.Continue, cmd
}

func (s *signInStep) View(state *State) string {
	return tui.Section(tui.DefaultTheme, s.Title(state), s.signIn.View())
}

// permissionsStep is the permissions screen as a flow step.
type permissionsStep struct{ perms *Permissions }

func (s *permissionsStep) ID() string { return "permissions" }

func (s *permissionsStep) Title(*State) string { return "Permissions" }

func (s *permissionsStep) Hints(*State) []struct{ Key, Label string } {
	return hintPairs(s.perms.Hints("back"))
}

func (s *permissionsStep) Init(*State) tea.Cmd { return nil }

func (s *permissionsStep) Update(msg tea.Msg, state *State) (flow.Directive, tea.Cmd) {
	switch s.perms.Update(msg) {
	case PermissionsSaved:
		state.PermissionsSaved = true
		return flow.Next, nil
	case PermissionsBack:
		s.perms.Reload()
		return flow.Back, nil
	case PermissionsQuit:
		return flow.Quit, nil
	}
	return flow.Continue, nil
}

func (s *permissionsStep) View(state *State) string {
	return tui.Section(tui.DefaultTheme, s.Title(state), s.perms.View("back"))
}

func hintPairs(h []tui.Hint) []struct{ Key, Label string } {
	out := make([]struct{ Key, Label string }, len(h))
	for i, x := range h {
		out[i] = struct{ Key, Label string }{x.Key, x.Label}
	}
	return out
}
