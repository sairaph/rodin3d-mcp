// Package commands is the one-shot command line: each command calls the
// service method its MCP tool calls and prints the same front matter and
// body, or JSON of the front matter with the argument json.
//
// Arguments are positional; options are key=value pairs.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sairaph/mcp-wizard/command"
	"github.com/sairaph/mcp-wizard/render"
	"github.com/sairaph/mcp-wizard/tui"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// Env is what the commands need.
type Env struct {
	Service func() *service.Service
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
	// Open shows a file with the operating system's viewer; nil never opens.
	Open func(path string) error
}

// args splits positional arguments from key=value options and the json flag.
type args struct {
	pos  []string
	opts map[string]string
	json bool
}

func parse(raw []string) args {
	a := args{opts: map[string]string{}}
	for _, r := range raw {
		switch {
		case r == "json" || r == "--json":
			a.json = true
		case strings.Contains(r, "=") && !strings.Contains(r, "://") && !strings.HasPrefix(r, "?") && !filepath.IsAbs(r):
			k, v, _ := strings.Cut(r, "=")
			a.opts[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		default:
			a.pos = append(a.pos, r)
		}
	}
	return a
}

func (a args) arg(i int) string {
	if i < len(a.pos) {
		return a.pos[i]
	}
	return ""
}

// Register adds every command to reg.
func Register(reg *command.Registry, env Env) {
	run := func(fn func(context.Context, *service.Service, args) (service.Result, error)) func(context.Context, []string) int {
		return func(ctx context.Context, raw []string) int {
			svc := env.Service()
			defer svc.Close()
			a := parse(raw)
			res, err := fn(ctx, svc, a)
			return env.print(res, err, a.json)
		}
	}
	reg.Register(command.Handler{Name: "account", Description: "Show the account, wallet, permissions and sync state", Usage: "account [json]",
		Run: run(func(ctx context.Context, s *service.Service, _ args) (service.Result, error) { return s.Account(ctx) })})
	reg.Register(command.Handler{Name: "logout", Description: "Sign out: revoke the grant and delete the tokens", Usage: "logout [json]",
		Run: run(func(ctx context.Context, s *service.Service, _ args) (service.Result, error) { return s.Logout(ctx) })})
	reg.Register(command.Handler{Name: "options", Description: "List the generation settings, gates and recipes", Usage: "options [gen-2.5|gen-1.5] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			return s.Options(ctx, a.arg(0))
		})})
	reg.Register(command.Handler{Name: "quote", Description: "Quote the price of an operation", Usage: "quote <operation> [task=<id>] [tier=..] [model=..] [texture_mode=..] [smart_low_poly=true] [resolution=..] [geometry=..] [budget=..] [batch=N] [json]",
		Run: run(quote)})
	reg.Register(command.Handler{Name: "tasks", Description: "List the generations from the local index", Usage: "tasks [query] [stage=..] [state=..] [page=N] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			page, err := intOpt(a, "page", 1)
			if err != nil {
				return service.Result{}, err
			}
			return s.ListTasks(ctx, service.ListInput{Query: strings.Join(a.pos, " "), Stage: a.opts["stage"], State: a.opts["state"], Page: page})
		})})
	reg.Register(command.Handler{Name: "task", Description: "Show one task with its settings, inputs and files", Usage: "task <id> [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			return s.GetTask(ctx, a.arg(0))
		})})
	reg.Register(command.Handler{Name: "wait", Description: "Wait until a running task changes stage", Usage: "wait <id> [seconds] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			secs := 45
			if v := a.arg(1); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 600 {
					return service.Result{}, usage("seconds must be a number from 1 to 600")
				}
				secs = n
			}
			return s.WaitTask(ctx, a.arg(0), time.Duration(secs)*time.Second)
		})})
	reg.Register(command.Handler{Name: "preview", Description: "Save a preview image of a task as PNG", Usage: "preview <id> [mode] [file.png] [stage=..] [size=N] [json]",
		Run: func(ctx context.Context, raw []string) int {
			svc := env.Service()
			defer svc.Close()
			a := parse(raw)
			in := service.PreviewInput{TaskID: a.arg(0), Stage: a.opts["stage"], Mode: a.opts["mode"]}
			for _, p := range a.pos[min(1, len(a.pos)):] {
				switch {
				case isMode(p):
					in.Mode = p
				case strings.HasSuffix(strings.ToLower(p), ".png"):
					abs, err := filepath.Abs(p)
					if err != nil {
						return env.print(service.Result{}, err, a.json)
					}
					in.SaveTo = abs
				default:
					return env.print(service.Result{}, usage("unknown argument "+strconv.Quote(p)+": give a mode ("+strings.Join(assets.AllModes, ", ")+") or a .png file"), a.json)
				}
			}
			size, err := intOpt(a, "size", 0)
			if err != nil {
				return env.print(service.Result{}, err, a.json)
			}
			in.Size = size
			res, err := svc.Preview(ctx, in)
			code := env.print(res, err, a.json)
			// Show the picture when a person asked for it without naming a file.
			if err == nil && in.SaveTo == "" && env.Open != nil && tui.IsInteractive() {
				if f, ok := previewFile(res); ok {
					_ = env.Open(f)
				}
			}
			return code
		}})
	reg.Register(command.Handler{Name: "download", Description: "Download the packed files of a task", Usage: "download <id> [dir] [files=a,b] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.DownloadInput{TaskID: a.arg(0)}
			if d := a.arg(1); d != "" {
				abs, err := filepath.Abs(d)
				if err != nil {
					return service.Result{}, err
				}
				in.OutputDir = abs
			}
			if f := a.opts["files"]; f != "" {
				for _, n := range strings.Split(f, ",") {
					if n = strings.TrimSpace(n); n != "" {
						in.Files = append(in.Files, n)
					}
				}
			}
			return s.Download(ctx, in)
		})})
	reg.Register(command.Handler{Name: "permissions", Description: "Show the [permission] values of config.toml", Usage: "permissions [json]",
		Run: run(func(_ context.Context, s *service.Service, _ args) (service.Result, error) { return permissions(s) })})
	registerFlow(reg, run)
}

func isMode(p string) bool {
	for _, m := range assets.AllModes {
		if p == m {
			return true
		}
	}
	return false
}

func previewFile(res service.Result) (string, bool) {
	data, err := yaml.Marshal(res.Front)
	if err != nil {
		return "", false
	}
	var v struct {
		Preview struct {
			File string `yaml:"file"`
		} `yaml:"preview"`
	}
	if yaml.Unmarshal(data, &v) != nil || v.Preview.File == "" {
		return "", false
	}
	return v.Preview.File, true
}

func intOpt(a args, key string, def int) (int, error) {
	v, ok := a.opts[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, usage(key + " must be a number")
	}
	return n, nil
}

func usage(msg string) error {
	return &service.Failure{E: render.Error{Code: render.CodeInvalidInput, Message: msg + ".", Hint: "Run `" + domain.BinaryName + " help` for the command forms."}}
}

func quote(ctx context.Context, s *service.Service, a args) (service.Result, error) {
	if a.arg(0) == "" {
		return service.Result{}, usage("name the operation, e.g. quote generate_model tier=high")
	}
	batch, err := intOpt(a, "batch", 0)
	if err != nil {
		return service.Result{}, err
	}
	smart := false
	if v := a.opts["smart_low_poly"]; v != "" {
		if smart, err = strconv.ParseBool(v); err != nil {
			return service.Result{}, usage("smart_low_poly must be true or false")
		}
	}
	return s.Quote(ctx, service.QuoteInput{
		Operation: a.arg(0), TaskID: a.opts["task"], Model: a.opts["model"], Tier: a.opts["tier"], TextureMode: a.opts["texture_mode"],
		SmartLowPoly: smart, Resolution: a.opts["resolution"], Geometry: a.opts["geometry"], Budget: a.opts["budget"], Batch: batch, ForkStage: a.opts["stage"],
	})
}

type permissionsFront struct {
	File        string             `yaml:"file"`
	Exists      bool               `yaml:"exists"`
	Permissions config.Permissions `yaml:"permissions"`
	Error       string             `yaml:"error,omitempty"`
}

func permissions(s *service.Service) (service.Result, error) {
	cfg, err := s.Config()
	path := s.Paths().Config
	_, statErr := os.Stat(path)
	front := permissionsFront{File: path, Exists: statErr == nil, Permissions: cfg.Permission}
	if err != nil {
		front.Error = err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Each key allows or denies one paid or changing operation; reads need none. Edit %s (values \"allow\" or \"deny\"); every call reads it again.\n\n", path)
	for _, k := range config.Keys {
		v, _ := cfg.Permission.Get(k.Name)
		fmt.Fprintf(&b, "- %s = %s (%s)\n", k.Name, v, k.Tools)
	}
	return service.Result{Front: front, Body: strings.TrimRight(b.String(), "\n")}, nil
}

// print writes a result (front matter and body, or JSON) or an error, and
// returns the exit code.
func (env Env) print(res service.Result, err error, asJSON bool) int {
	if err != nil {
		e := service.Classify(err)
		if asJSON {
			data, _ := json.MarshalIndent(map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "hint": e.Hint}}, "", "  ")
			fmt.Fprintln(env.Stdout, string(data))
		} else {
			fmt.Fprintf(env.Stderr, "Error (%s): %s\n", e.Code, e.Message)
			if e.Hint != "" {
				fmt.Fprintf(env.Stderr, "%s\n", e.Hint)
			}
		}
		if e.Code == render.CodeInvalidInput {
			return 2
		}
		return 1
	}
	if asJSON {
		data, err := toJSON(res.Front)
		if err != nil {
			fmt.Fprintf(env.Stderr, "Error: %v\n", err)
			return 1
		}
		fmt.Fprintln(env.Stdout, string(data))
		return 0
	}
	text, err := render.Document{Front: res.Front, Body: res.Body}.String()
	if err != nil {
		fmt.Fprintf(env.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprint(env.Stdout, text)
	return 0
}

// toJSON converts a YAML-tagged front matter struct to JSON with the same
// field names.
func toJSON(front any) ([]byte, error) {
	data, err := yaml.Marshal(front)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// loginArgs reads the login arguments: json, no-browser (with or without
// dashes) and the pasted final address in any form parseRedirect takes
// (full address, path with query, or the query alone). Anything carrying a
// code= or error= is the address, never an option.
func loginArgs(raw []string) (a args, open bool, address string, err error) {
	a, open = args{opts: map[string]string{}}, true
	for _, r := range raw {
		switch strings.ToLower(strings.TrimLeft(r, "-")) {
		case "json":
			a.json = true
			continue
		case "no-browser":
			open = false
			continue
		}
		if strings.Contains(r, "code=") || strings.Contains(r, "error=") || strings.Contains(r, "://") {
			address = r
			continue
		}
		if strings.HasPrefix(r, "-") {
			return a, open, "", usage("login takes no option " + strconv.Quote(r) + "; use no-browser, json, or the final address")
		}
		return a, open, "", usage(strconv.Quote(r) + " is not a sign-in address: paste the whole address that starts with http://127.0.0.1: (quote it, it holds &)")
	}
	return a, open, address, nil
}

// terminalColumns is the width of the terminal w writes to, 0 when w is not
// a terminal or its size cannot be read.
func terminalColumns(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	cols, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return cols
}

// textWidth is the widest line of s in characters (the QR's block
// characters are one column each).
func textWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		w = max(w, utf8.RuneCountInString(line))
	}
	return w
}

// fitsWidth reports whether every line of s fits in cols columns.
func fitsWidth(s string, cols int) bool { return cols > 0 && textWidth(s) <= cols }

// Login is the login command. With an address it finishes the pending
// sign-in. Without one it starts a sign-in, prints the link and a QR code,
// opens the local browser unless no-browser is given, and waits up to ten
// minutes for the loopback listener or a pasted final address.
func Login(ctx context.Context, env Env, raw []string) int {
	a, open, address, err := loginArgs(raw)
	if err != nil {
		return env.print(service.Result{}, err, a.json)
	}
	svc := env.Service()
	defer svc.Close()
	if address != "" {
		res, _, err := svc.Login(ctx, service.LoginInput{RedirectURL: address})
		return env.print(res, err, a.json)
	}
	_, started, err := svc.Login(ctx, service.LoginInput{OpenBrowser: open})
	if err != nil {
		return env.print(service.Result{}, err, a.json)
	}
	out := env.Stdout
	fmt.Fprintln(out, "Sign in to Hyper3D: open this link on any device, sign in if asked, and click Authorize.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+started.AuthorizeURL)
	fmt.Fprintln(out)
	if tui.IsInteractive() {
		// A QR wider than the terminal wraps into noise no phone can read.
		if qr := service.QRText(started.AuthorizeURL); qr != "" {
			if fitsWidth(qr, terminalColumns(out)) {
				fmt.Fprintln(out, qr)
			} else {
				fmt.Fprintf(out, "(A terminal at least %d columns wide also shows a QR code of the link.)\n\n", textWidth(qr))
			}
		}
	}
	if started.Listening {
		fmt.Fprintln(out, "If that browser runs on this computer, the sign-in finishes by itself.")
		fmt.Fprintln(out, "Otherwise the browser ends on a page that cannot connect to 127.0.0.1: paste that page's whole address here and press Enter.")
	} else {
		fmt.Fprintln(out, "After Authorize the browser ends on a page that cannot connect to 127.0.0.1: paste that page's whole address here and press Enter.")
	}
	fmt.Fprintf(out, "Waiting up to %d minutes...\n", int(auth.LoginWindow.Minutes()))

	pasted := make(chan string)
	if env.Stdin != nil {
		go func() {
			sc := bufio.NewScanner(env.Stdin)
			sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
			for sc.Scan() {
				if line := strings.TrimSpace(sc.Text()); line != "" {
					pasted <- line
				}
			}
		}()
	}
	timeout := time.NewTimer(time.Until(started.ExpiresAt))
	defer timeout.Stop()
	done := started.Done
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(env.Stderr, "Sign-in cancelled.")
			return 1
		case <-timeout.C:
			fmt.Fprintln(env.Stderr, "The sign-in link expired; run login again.")
			return 1
		case o := <-done:
			done = nil
			if o.Err != nil {
				if errors.Is(o.Err, auth.ErrLoginReplaced) {
					continue // a pasted address finished it, or it was cancelled
				}
				return env.print(service.Result{}, o.Err, a.json)
			}
			svc.AfterLogin(ctx, o.Credentials)
			fmt.Fprintf(out, "Signed in as %s.\n", o.Credentials.Username)
			return 0
		case line := <-pasted:
			creds, err := svc.CompleteLogin(ctx, line)
			if err != nil {
				e := service.Classify(err)
				fmt.Fprintf(env.Stderr, "%s Paste the address again, or wait.\n", e.Message)
				if errors.Is(err, auth.ErrLoginExpired) || errors.Is(err, auth.ErrNoPendingLogin) {
					return 1
				}
				continue
			}
			fmt.Fprintf(out, "Signed in as %s.\n", creds.Username)
			return 0
		}
	}
}
