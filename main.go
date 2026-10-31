package main

import (
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

	"github.com/sairaph/mcp-wizard/cli"
	"github.com/sairaph/mcp-wizard/command"
	"github.com/sairaph/mcp-wizard/doctor"
	"github.com/sairaph/mcp-wizard/harness"
	"github.com/sairaph/mcp-wizard/tui"
	"github.com/sairaph/mcp-wizard/update"

	"github.com/sairaph/rodin3d-mcp/internal/app"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/commands"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/install"
	"github.com/sairaph/rodin3d-mcp/internal/mcpserver"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/store"
	viewerapp "github.com/sairaph/rodin3d-mcp/internal/viewer/app"
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

func installEnv() install.Env {
	return install.Env{Service: newService, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
}

func init() {
	commands.Register(oneShotCommands, commandEnv())
}

var usageSpecs = []cli.Spec{
	{Name: "mcp", Description: "Run the MCP server over stdio (default when not in a terminal)"},
	{Name: "install", Description: "Set up: register with the AI clients, sign in, set the permissions"},
	{Name: "uninstall", Description: "Remove everything rodin3d-mcp put on this computer: sign-in, client entries, data, program, PATH lines (asks first)"},
	{Name: "add", Description: "Register the server in this project's AI client configs"},
	{Name: "login", Description: "Sign in to Hyper3D (login [no-browser] [\"<final address>\"]: quote the address, it holds &)"},
	{Name: "daemon", Description: "Run the background syncer (daemon --stop stops it)"},
	{Name: "viewer", Description: "Run the viewer window (open-viewer or the open_viewer tool starts it)"},
	{Name: "doctor", Description: "Diagnose the installation"},
	{Name: "update", Description: "Update to the latest release"},
	{Name: "licenses", Description: "Print the licenses of the third-party code in this binary"},
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
	// login takes words and a pasted address that the library's flag set
	// would refuse (--no-browser) or misread, so it is dispatched first.
	if len(os.Args) > 1 && os.Args[1] == "login" {
		os.Exit(commands.Login(ctx, commandEnv(), os.Args[2:]))
	}
	// The viewer window's loop must own the main thread (macOS), so it runs
	// here on the main goroutine.
	if len(os.Args) > 1 && os.Args[1] == "viewer" {
		svc := newService()
		code := viewerapp.Run(ctx, svc, version)
		svc.Close()
		os.Exit(code)
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

	// The library parses --token and --email for these commands; Hyper3D
	// sign-in has neither (the browser approves it).
	if len(cmd.Credentials) > 0 {
		fmt.Fprintln(os.Stderr, "Error: "+domain.BinaryName+" takes no --token or --email; sign in with `"+domain.BinaryName+" login` and approve in a browser.")
		os.Exit(2)
	}

	switch cmd.Name {
	case "mcp":
		os.Exit(runMCPServer(ctx, cmd))
	case "install", "configure":
		os.Exit(install.Install(ctx, installEnv(), cmd))
	case "uninstall":
		os.Exit(install.Uninstall(ctx, installEnv(), cmd))
	case "add":
		os.Exit(install.Add(ctx, installEnv(), cmd))
	case "daemon":
		os.Exit(runDaemon(ctx, cmd.Args))
	case "doctor":
		os.Exit(runDoctor(ctx))
	case "update":
		os.Exit(runUpdate(ctx, cmd))
	case "refresh-guide":
		// Not listed in the usage: update runs it with the new binary.
		os.Exit(runRefreshGuide(cmd))
	case "licenses":
		fmt.Print(thirdPartyNotices)
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
	fmt.Fprintln(w, "  --yes          install, add: non-interactive, register only; uninstall: do not ask")
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
	cfg, err := c.svc.Config()
	if err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: err.Error()}
	}
	if _, err := os.Stat(path); err != nil {
		return doctor.Result{Name: c.Name(), Status: doctor.OK, Detail: "no " + path + ": every paid or changing operation is allowed"}
	}
	detail := path + ": every write allowed"
	if denied := cfg.Permission.Denied(); len(denied) > 0 {
		detail = path + ": denies " + strings.Join(denied, ", ")
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
	// A grant an earlier logout could not revoke stays valid on the server
	// until logout reaches Hyper3D.
	pending := ""
	if c.svc.RevokePending() {
		pending = "; an earlier logout could not get Hyper3D to revoke its grant, which stays valid until `" + domain.BinaryName + " logout` runs again"
	}
	switch creds.State() {
	case auth.StateSignedOut:
		if pending != "" {
			return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: "not signed in" + pending}
		}
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: "not signed in; run `" + domain.BinaryName + " login`"}
	case auth.StateNeedsLogin:
		return doctor.Result{Name: c.Name(), Status: doctor.Fail, Detail: "the session ended; run `" + domain.BinaryName + " login`" + pending}
	}
	if pending != "" {
		pending += " (that also signs this session out)"
	}
	left := time.Until(creds.AccessExpiresAt).Round(time.Minute)
	detail := fmt.Sprintf("signed in as %s; access token valid for %s", creds.Username, left)
	if left <= 0 {
		detail = fmt.Sprintf("signed in as %s; access token expired, the next call refreshes it", creds.Username)
	}
	if creds.RefreshToken == "" {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: detail + "; no refresh token" + pending}
	}
	if pending != "" {
		return doctor.Result{Name: c.Name(), Status: doctor.Warn, Detail: detail + pending}
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
	detector, err := install.NewDetector(domain.ServerName)
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
	// `update --from <file>` swaps in a binary that was already downloaded
	// and verified by other means, then refreshes the guide skill, as a
	// download does.
	if len(cmd.Args) >= 2 && cmd.Args[0] == "--from" {
		if err := update.SwapFrom(ctx, cmd.Args[1], opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("  Updated.")
		return runNewBinaryGuideRefresh(ctx, filepath.Join(opts.InstallDir, opts.BinaryName))
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
	// The new binary carries the matching guide; let it refresh the copies.
	return runNewBinaryGuideRefresh(ctx, filepath.Join(opts.InstallDir, opts.BinaryName))
}

// runNewBinaryGuideRefresh runs `refresh-guide` with the binary an update
// just installed, since this process still holds the old guide.
func runNewBinaryGuideRefresh(ctx context.Context, exe string) int {
	cmd := exec.CommandContext(ctx, exe, "refresh-guide")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  Could not update the guide skill (%v); run `%s install --yes` to write it again.\n", err, domain.BinaryName)
		return 1
	}
	return 0
}

// runRefreshGuide rewrites the copies of the guide skill this program wrote
// before with the guide embedded in this binary. --dry-run only lists them.
func runRefreshGuide(cmd cli.Command) int {
	dryRun := false
	for _, a := range cmd.Args {
		if a == "--dry-run" || a == "-dry-run" {
			dryRun = true
		}
	}
	return install.RefreshSkills(os.Stdout, dryRun)
}

// --- App ---

// runApp is the terminal app (internal/app), opened when the binary runs
// bare in a terminal.
func runApp(ctx context.Context) int {
	svc := newService()
	defer svc.Close()
	return app.Run(ctx, app.Options{
		Version: version,
		Service: svc,
		Open:    openFile,
		Doctor: func(ctx context.Context, w io.Writer) {
			newDoctor(svc).Run(ctx, w)
		},
	})
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
