package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sairaph/mcp-wizard/app"
	"github.com/sairaph/mcp-wizard/app/detail"
	"github.com/sairaph/mcp-wizard/app/menu"
	"github.com/sairaph/mcp-wizard/async"
	"github.com/sairaph/mcp-wizard/cli"
	"github.com/sairaph/mcp-wizard/command"
	"github.com/sairaph/mcp-wizard/doctor"
	"github.com/sairaph/mcp-wizard/flow"
	"github.com/sairaph/mcp-wizard/harness"
	"github.com/sairaph/mcp-wizard/installer"
	"github.com/sairaph/mcp-wizard/tui"
	"github.com/sairaph/mcp-wizard/update"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/commands"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/mcpserver"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// version is set by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

var oneShotCommands = command.New()

func newService() *service.Service {
	exe, _ := harness.ResolveExecutable()
	return service.New(service.Options{
		Paths:       domain.DefaultPaths(),
		Version:     version,
		Executable:  exe,
		OpenBrowser: auth.OpenBrowser,
	})
}

func commandEnv() commands.Env {
	return commands.Env{Service: newService, Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Open: openFile}
}

func init() {
	commands.Register(oneShotCommands, commandEnv())
}

var usageSpecs = []cli.Spec{
	{Name: "mcp", Description: "Run the MCP server over stdio (default when not in a terminal)"},
	{Name: "install", Description: "Register the MCP server with the AI clients on this machine"},
	{Name: "uninstall", Description: "Remove the AI client registrations"},
	{Name: "add", Description: "Register the server in this project's AI client configs"},
	{Name: "login", Description: "Sign in to Hyper3D (login [final-address] [no-browser])"},
	{Name: "daemon", Description: "Run the background syncer (daemon --stop stops it)"},
	{Name: "doctor", Description: "Diagnose the installation"},
	{Name: "update", Description: "Update to the latest release"},
	{Name: "version", Description: "Print the version"},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bare invocation in a terminal opens the app; everywhere else (an AI
	// client spawning us, a pipe) it is the MCP server, which cli.Parse
	// reports as "mcp".
	if opts := updateOptions(); opts.InstallDir != "" {
		update.RemoveStaleBinaries(opts.InstallDir, opts.BinaryName)
	}
	if len(os.Args) == 1 && tui.IsInteractive() {
		os.Exit(runApp(ctx))
	}

	cmd, err := cli.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, cli.ErrUsage) {
			printUsage(os.Stdout)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	switch cmd.Name {
	case "mcp":
		os.Exit(runMCPServer(ctx, cmd))
	case "install", "configure":
		if cmd.Scope == string(harness.ScopeProject) {
			os.Exit(runAdd(ctx, cmd))
		}
		os.Exit(runInstall(ctx, cmd))
	case "uninstall":
		os.Exit(runUninstall(ctx, cmd))
	case "add":
		os.Exit(runAdd(ctx, cmd))
	case "login":
		if len(cmd.Credentials) > 0 {
			fmt.Fprintln(os.Stderr, "Error: Hyper3D sign-in has no token or e-mail option; run `"+domain.BinaryName+" login` and approve in a browser.")
			os.Exit(2)
		}
		os.Exit(commands.Login(ctx, commandEnv(), cmd.Args))
	case "daemon":
		os.Exit(runDaemon(ctx, cmd.Args))
	case "doctor":
		os.Exit(runDoctor(ctx))
	case "update":
		os.Exit(runUpdate(ctx, cmd))
	case "help":
		printUsage(os.Stdout)
	case "version":
		fmt.Println(version)
	default:
		if ok, code := oneShotCommands.Dispatch(ctx, cmd.Name, cmd.Args); ok {
			os.Exit(code)
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd.Name)
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, "Usage: %s <command> [arguments]\n\nCommands:\n", domain.BinaryName)
	width := 0
	for _, s := range usageSpecs {
		width = max(width, len(s.Name))
	}
	for _, h := range oneShotCommands.List() {
		width = max(width, len(h.Name))
	}
	for _, s := range usageSpecs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, s.Name, s.Description)
	}
	for _, h := range oneShotCommands.List() {
		fmt.Fprintf(w, "  %-*s  %s\n", width, h.Name, h.Description)
	}
	fmt.Fprintln(w, "\nCommand forms (options are key=value; json prints the result as JSON):")
	for _, h := range oneShotCommands.List() {
		fmt.Fprintf(w, "  %s %s\n", domain.BinaryName, h.Usage)
	}
	fmt.Fprintln(w, "\nInstall, add and uninstall flags:")
	fmt.Fprintln(w, "  --yes          non-interactive")
	fmt.Fprintln(w, "  --dry-run      show what would change without writing")
	fmt.Fprintln(w, "  --all          register with every detected client")
	fmt.Fprintln(w, "  --clients      comma-separated client ids")
	fmt.Fprintln(w, "  --scope        project (default: global)")
	fmt.Fprintln(w, "  --dir          project directory for --scope project and add")
	fmt.Fprintln(w, "  --name         server name in client configs (default "+domain.ServerName+")")
}

// --- Daemon ---

func runDaemon(ctx context.Context, args []string) int {
	svc := newService()
	defer svc.Close()
	if len(args) > 0 && (args[0] == "--stop" || args[0] == "stop") {
		if err := svc.StopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			return 1
		}
		fmt.Println("  Daemon stopped.")
		return 0
	}
	if err := svc.RunDaemon(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
		return 1
	}
	return 0
}

// --- Shared install helpers ---

// AppState is shared across install wizard steps.
type AppState struct {
	flow.BaseState
	Harness installer.HarnessState
	Results installer.ResultsState
}

func harnessState(s *AppState) *installer.HarnessState { return &s.Harness }
func resultsState(s *AppState) *installer.ResultsState { return &s.Results }

func serverName(cmd cli.Command) string {
	if cmd.ServerName != "" {
		return cmd.ServerName
	}
	return domain.ServerName
}

func newDetector(name string) (*harness.Detector, error) {
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

// --- Install / add ---

func runInstall(ctx context.Context, cmd cli.Command) int {
	detector, err := newDetector(serverName(cmd))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if tui.IsInteractive() && !cmd.Yes {
		return runWizard(ctx, detector, harness.Scope{}, cmd, domain.BinaryName+" setup")
	}
	return runUnattended(ctx, detector, harness.Scope{}, cmd, harness.Present)
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

func runAdd(ctx context.Context, cmd cli.Command) int {
	dir, err := projectDir(cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	detector, err := newDetector(serverName(cmd))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	scope := harness.ProjectScopeDir(dir)
	if tui.IsInteractive() && !cmd.Yes {
		return runWizard(ctx, detector, scope, cmd, domain.BinaryName+" project setup")
	}
	return runUnattended(ctx, detector, scope, cmd, harness.Present)
}

// runWizard drives the interactive install: pick clients, register. Sign-in
// is the login command (the installer's sign-in step comes with the app).
func runWizard(ctx context.Context, detector *harness.Detector, scope harness.Scope, cmd cli.Command, title string) int {
	state := &AppState{}
	steps := []flow.Step[AppState]{
		installer.HarnessStep(ctx, detector, harnessState, installer.HarnessStepOptions{AllDetected: true, Scope: scope}),
		installer.ApplyStep(ctx, detector, harnessState, resultsState, installer.ApplyStepOptions{Scope: scope, DryRun: cmd.DryRun}),
	}
	f := flow.New(steps, state)
	code := tui.Run(ctx, f, tui.Options{Title: title})
	if state.Failure != nil {
		fmt.Fprintln(os.Stderr, state.Failure)
	}
	if code == 0 && !cmd.DryRun {
		printLoginHint()
	}
	return code
}

func printLoginHint() {
	svc := newService()
	defer svc.Close()
	if c, err := svc.Credentials(context.Background()); err == nil && c.State() == auth.StateSignedIn {
		return
	}
	fmt.Printf("\n  Sign in to Hyper3D with:\n    %s login\n", domain.BinaryName)
}

func runUnattended(ctx context.Context, detector *harness.Detector, scope harness.Scope, cmd cli.Command, desired harness.DesiredState) int {
	enabling := desired == harness.Present
	harnesses := detector.DetectIn(ctx, scope)

	var ids []harness.ID
	if enabling {
		var reason string
		ids, reason = selectIDs(harnesses, cmd)
		if reason != "" {
			fmt.Fprintf(os.Stderr, "  %s\n", reason)
			if len(cmd.Clients) > 0 {
				return 2
			}
			return 0
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
			installer.PrintNoClients(os.Stdout, domain.BinaryName, false)
		} else {
			fmt.Println("  No clients are configured.")
		}
		return 0
	}
	if cmd.DryRun {
		return printPlan(ctx, detector, scope, ids, desired)
	}
	policy := harness.ConflictReplace
	if !enabling {
		policy = harness.ConflictError
	}
	results := detector.ApplyIn(ctx, scope, ids, desired, policy)
	installer.PrintResultsWithScope(os.Stdout, results, scope, enabling, false)
	installer.PrintReloadHints(os.Stdout, results, byID(harnesses))
	if enabling {
		printLoginHint()
	}
	return exitCodeFor(results)
}

func printPlan(ctx context.Context, detector *harness.Detector, scope harness.Scope, ids []harness.ID, desired harness.DesiredState) int {
	policy := harness.ConflictReplace
	if desired == harness.Absent {
		policy = harness.ConflictError
	}
	changes, err := detector.PlanResultsIn(ctx, scope, ids, desired, policy)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	installer.PrintChanges(os.Stdout, changes, scope)
	return 0
}

// --- Uninstall ---

func runUninstall(ctx context.Context, cmd cli.Command) int {
	detector, err := newDetector(serverName(cmd))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	scope := harness.Scope{}
	if cmd.Scope == string(harness.ScopeProject) {
		dir, err := projectDir(cmd)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		scope = harness.ProjectScopeDir(dir)
	}
	return runUnattended(ctx, detector, scope, cmd, harness.Absent)
}

// --- Doctor ---

func updateOptions() update.Options {
	opts := update.Options{
		Owner:          domain.Owner,
		Repo:           domain.Repo,
		CurrentVersion: version,
		AssetName:      domain.AssetName,
		BinaryName:     domain.BinaryName,
	}
	if exe, err := harness.ResolveExecutable(); err == nil {
		opts.InstallDir = filepath.Dir(exe)
		opts.BinaryName = filepath.Base(exe)
	}
	return opts
}

func newDoctor(svc *service.Service) *doctor.Runner {
	opts := updateOptions()
	r := doctor.New(
		executableCheck{},
		doctor.PathCheck{Dir: opts.InstallDir},
		configCheck{svc},
		credentialsCheck{svc},
		serviceCheck{svc},
		storeCheck{svc},
		daemonCheck{svc},
		clientsCheck{},
	)
	if version != "dev" {
		r.Add(doctor.UpdateCheck{Opts: opts})
	}
	return r
}

func runDoctor(ctx context.Context) int {
	svc := newService()
	defer svc.Close()
	return newDoctor(svc).Run(ctx, os.Stdout)
}

// executableCheck is doctor.ExecutableCheck, except on Windows: there Go
// reports no execute permission bits, so the library check always fails, and
// a file that exists and is running is executable by definition.
type executableCheck struct{}

func (executableCheck) Name() string { return doctor.ExecutableCheck{}.Name() }

func (c executableCheck) Run(ctx context.Context) doctor.Result {
	if runtime.GOOS != "windows" {
		return doctor.ExecutableCheck{}.Run(ctx)
	}
	path, err := os.Executable()
	if err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: fmt.Sprintf("cannot determine executable: %v", err)}
	}
	if _, err := os.Stat(path); err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: fmt.Sprintf("cannot stat %s: %v", path, err)}
	}
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: path}
}

type configCheck struct{ svc *service.Service }

func (configCheck) Name() string { return "Config" }

func (c configCheck) Run(context.Context) doctor.Result {
	path := c.svc.Paths().Config
	if _, err := c.svc.Config(); err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: err.Error() + " (every write is denied until it is fixed)"}
	}
	if _, err := os.Stat(path); err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: "no " + path + ": every paid or changing operation is denied"}
	}
	cfg, _ := config.Load(path)
	var allowed []string
	for _, k := range config.Keys {
		if cfg.Permission.Allowed(k.Name) {
			allowed = append(allowed, k.Name)
		}
	}
	detail := path + ": every write denied"
	if len(allowed) > 0 {
		detail = path + ": allows " + strings.Join(allowed, ", ")
	}
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: detail}
}

type credentialsCheck struct{ svc *service.Service }

func (credentialsCheck) Name() string { return "Credentials" }

func (c credentialsCheck) Run(ctx context.Context) doctor.Result {
	creds, err := c.svc.Credentials(ctx)
	if err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: err.Error()}
	}
	switch creds.State() {
	case auth.StateSignedOut:
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: "not signed in; run `" + domain.BinaryName + " login`"}
	case auth.StateNeedsLogin:
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: "the session ended; run `" + domain.BinaryName + " login`"}
	}
	left := time.Until(creds.AccessExpiresAt).Round(time.Minute)
	detail := fmt.Sprintf("signed in as %s; access token valid for %s", creds.Username, left)
	if left <= 0 {
		detail = fmt.Sprintf("signed in as %s; access token expired, the next call refreshes it", creds.Username)
	}
	if creds.RefreshToken == "" {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: detail + "; no refresh token"}
	}
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: detail}
}

type serviceCheck struct{ svc *service.Service }

func (serviceCheck) Name() string { return "Hyper3D" }

func (c serviceCheck) Run(ctx context.Context) doctor.Result {
	creds, err := c.svc.Credentials(ctx)
	if err != nil || creds.State() != auth.StateSignedIn {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: "skipped: not signed in"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := c.svc.Client().GetInfo(ctx)
	if err != nil {
		e := service.Classify(err)
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: e.Message}
	}
	w := service.WalletOf(info)
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: fmt.Sprintf("%s reachable; %s plan, %.1f credits available", c.svc.Client().Base(), info.Meta.Subscriptions.HighestLevel, w.Available)}
}

type storeCheck struct{ svc *service.Service }

func (storeCheck) Name() string { return "Task index" }

func (c storeCheck) Run(context.Context) doctor.Result {
	st, err := c.svc.Store()
	if err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: err.Error()}
	}
	tasks, err := st.Tasks()
	if err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: err.Error()}
	}
	state, _ := st.State()
	detail := fmt.Sprintf("%s: %d tasks", c.svc.Paths().Database, len(tasks))
	if !state.LastSync.IsZero() {
		detail += ", last sync " + state.LastSync.Local().Format("2006-01-02 15:04")
	} else {
		detail += ", never synced"
	}
	if state.Phase == store.PhaseError {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: detail + "; last error: " + state.LastError}
	}
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: detail}
}

type daemonCheck struct{ svc *service.Service }

func (daemonCheck) Name() string { return "Daemon" }

func (c daemonCheck) Run(ctx context.Context) doctor.Result {
	if c.svc.DaemonRunning() {
		return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: "running (log " + c.svc.Paths().DaemonLog + ")"}
	}
	if creds, err := c.svc.Credentials(ctx); err == nil && creds.State() == auth.StateSignedIn {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: "not running; any command or tool call starts it"}
	}
	return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: "not running (starts after login)"}
}

// clientsCheck lists the AI clients that have this server registered.
type clientsCheck struct{}

func (clientsCheck) Name() string { return "AI clients" }

func (clientsCheck) Run(ctx context.Context) doctor.Result {
	detector, err := newDetector(domain.ServerName)
	if err != nil {
		return doctor.Result{Name: "AI clients", Status: doctor.Fail, Detail: err.Error()}
	}
	var configured []string
	for _, h := range detector.Detect(ctx) {
		if h.Configured {
			configured = append(configured, h.Name)
		}
	}
	if len(configured) == 0 {
		return doctor.Result{Name: "AI clients", Status: doctor.Warn, Detail: "no client is configured; run `" + domain.BinaryName + " install`"}
	}
	return doctor.Result{Name: "AI clients", Status: doctor.OK, Detail: strings.Join(configured, ", ")}
}

// --- Update ---

func runUpdate(ctx context.Context, cmd cli.Command) int {
	opts := updateOptions()
	if len(cmd.Args) >= 2 && cmd.Args[0] == "--from" {
		if err := update.SwapFrom(ctx, cmd.Args[1], opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("  Updated.")
		return 0
	}
	if version == "dev" {
		fmt.Println("  This is a development build; build from source to update.")
		return 0
	}
	latest, available, err := update.Check(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Update check failed: %v\n", err)
		return 1
	}
	if !available {
		fmt.Printf("  %s %s is up to date.\n", domain.BinaryName, version)
		return 0
	}
	fmt.Printf("  Updating %s %s -> %s\n", domain.BinaryName, version, latest)
	if err := update.SelfUpdate(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "  Update failed: %v\n", err)
		return 1
	}
	fmt.Printf("  Updated to %s.\n", latest)
	return 0
}

// --- App ---

// The interactive app opens when the binary is run bare in a terminal. In
// this phase it offers the doctor; the full app comes with phase P4.

const (
	stepMenu app.Step = iota
	stepDoctor
)

type appState struct {
	app.AppModel
	ctx    context.Context
	menu   *menu.Model
	detail *detail.Model
}

func runApp(ctx context.Context) int {
	s := &appState{ctx: ctx}
	s.menu = menu.New(domain.BinaryName+" "+version, func() []menu.Item {
		return []menu.Item{
			{Label: "Run doctor", Action: "doctor"},
			{Label: "Quit", Action: "quit"},
		}
	})
	return app.Run(ctx, s, app.Options{Title: domain.BinaryName, Version: version})
}

func (m *appState) Init() tea.Cmd { return m.menu.Init() }

func (m *appState) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.HandleGlobalKeys(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case app.ActionMsg:
		switch msg.Source {
		case "menu":
			action, _ := msg.Data.(string)
			if msg.Value == "quit" || action == "quit" {
				m.Quit = true
				return m, tea.Quit
			}
			if action == "doctor" {
				m.Step = stepDoctor
				m.Status = "Running checks..."
				m.detail = detail.New("Doctor", m.Status)
				return m, tea.Batch(m.detail.Init(), async.Load(func() (string, error) {
					var buf bytes.Buffer
					svc := newService()
					defer svc.Close()
					newDoctor(svc).Run(m.ctx, &buf)
					return buf.String(), nil
				}))
			}
		case "detail":
			if msg.Value == "back" {
				m.Step = stepMenu
				return m, nil
			}
		}
		return m, nil
	case async.Result[string]:
		m.Status = ""
		if msg.Err != nil {
			m.detail.SetContent("Doctor failed: " + msg.Err.Error())
		} else {
			m.detail.SetContent(msg.Value)
		}
		return m, nil
	}
	switch m.Step {
	case stepMenu:
		return m, m.menu.Update(msg)
	case stepDoctor:
		if m.detail != nil {
			return m, m.detail.Update(msg)
		}
	}
	return m, nil
}

func (m *appState) View() string {
	if m.Step == stepDoctor && m.detail != nil {
		return m.detail.View()
	}
	return m.menu.View()
}

// --- MCP Server ---

func runMCPServer(ctx context.Context, cmd cli.Command) int {
	if cmd.Remote != "" {
		fmt.Fprintln(os.Stderr, "Error: "+domain.BinaryName+" serves its own tools; --remote is not supported.")
		return 2
	}
	transport := os.Getenv("TRANSPORT")
	if transport == "" {
		transport = "stdio"
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	svc := newService()
	defer svc.Close()
	srv := mcpserver.New(mcpserver.Config{Version: version, Transport: transport, HTTPAddr: addr, Service: svc})
	err := srv.Run(ctx)
	if err == nil || errors.Is(err, context.Canceled) || isClientHangup(err) {
		return 0
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// isClientHangup reports whether the server stopped because the client closed
// the transport (stdin EOF for stdio), which is how MCP sessions normally end.
func isClientHangup(err error) bool {
	return strings.HasSuffix(err.Error(), io.EOF.Error())
}

// openFile shows path with the operating system's default viewer.
func openFile(path string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		c = exec.Command("open", path)
	default:
		c = exec.Command("xdg-open", path)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
