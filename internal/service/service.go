// Package service is what every surface does: each tool of the MCP server
// and each one-shot command calls the same method here and renders the
// Result it returns. Errors are classified once, in Classify.
package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/sairaph/mcp-wizard/daemon/lock"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Tools are the MCP tools of this build. Stage actions name other tools of
// the plan too; they are shown only when listed here.
var Tools = []string{
	"get_account", "login", "logout", "get_generation_options", "quote_cost",
	"list_tasks", "get_task", "wait_task", "get_preview", "download_model",
	"generate_model", "redo_geometry", "confirm_geometry", "select_candidate", "generate_texture", "redo_material",
	"confirm_material", "export_model", "fork_task",
}

func registered(tool string) bool {
	for _, t := range Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// Options configure a Service.
type Options struct {
	Paths   domain.Paths
	BaseURL string // API base; rodin.BaseURL() when empty
	Version string
	// Executable is the binary started as the daemon; empty never starts one
	// (tests).
	Executable string
	// OpenBrowser opens a sign-in link locally; nil never opens one.
	OpenBrowser func(string) error
}

// Service holds what the surfaces share in one process.
type Service struct {
	opts    Options
	paths   domain.Paths
	creds   *auth.Store
	oauth   *auth.OAuth
	session *auth.Session
	login   *auth.Manager
	client  *rodin.Client

	mu    sync.Mutex
	st    *store.Store
	cache *assets.Cache
}

// New returns a service for opts.
func New(opts Options) *Service {
	if opts.BaseURL == "" {
		opts.BaseURL = rodin.BaseURL()
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := &Service{opts: opts, paths: opts.Paths}
	s.creds = auth.NewStore(opts.Paths)
	s.oauth = auth.NewOAuth(auth.EndpointsFor(opts.BaseURL), opts.Version)
	s.session = auth.NewSession(s.creds, s.oauth)
	s.client = rodin.New(opts.BaseURL, s.session, opts.Version)
	s.login = auth.NewManager(s.creds, s.oauth, func(ctx context.Context, token string) (auth.Account, error) {
		info, err := rodin.New(opts.BaseURL, rodin.StaticToken(token), opts.Version).GetInfo(ctx)
		if err != nil {
			return auth.Account{}, err
		}
		return auth.Account{UserUUID: info.Meta.UserUUID, Username: info.Meta.Username, Email: info.Meta.Email}, nil
	})
	return s
}

// Paths returns the data paths of the service.
func (s *Service) Paths() domain.Paths { return s.paths }

// Client returns the protocol client.
func (s *Service) Client() *rodin.Client { return s.client }

// Session returns the token source.
func (s *Service) Session() *auth.Session { return s.session }

// Store opens the database on first use and keeps it open.
func (s *Service) Store() (*store.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st != nil {
		return s.st, nil
	}
	st, err := store.Open(s.paths.Database)
	if err != nil {
		return nil, err
	}
	s.st = st
	s.cache = assets.NewCache(s.paths.Cache, st, s.client)
	return st, nil
}

func (s *Service) assetCache() (*assets.Cache, error) {
	if _, err := s.Store(); err != nil {
		return nil, err
	}
	return s.cache, nil
}

// Close releases the database.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st != nil {
		s.st.Close()
		s.st = nil
	}
}

// Credentials returns the stored credentials (nil when signed out).
func (s *Service) Credentials(ctx context.Context) (*auth.Credentials, error) {
	return s.creds.Load(ctx)
}

// RevokePending reports whether an earlier logout could not revoke its grant
// and keeps it for the next logout to retry.
func (s *Service) RevokePending() bool { return s.creds.RevokePending() }

// signedIn returns the credentials or the LoginRequired error.
func (s *Service) signedIn(ctx context.Context) (*auth.Credentials, error) {
	c, err := s.creds.Load(ctx)
	if err != nil {
		return nil, err
	}
	if st := c.State(); st != auth.StateSignedIn {
		return nil, &auth.LoginRequired{State: st}
	}
	return c, nil
}

// Config reads config.toml; a malformed file yields the defaults (every
// write denied) and the error.
func (s *Service) Config() (config.Config, error) { return config.Load(s.paths.Config) }

// EnsureDaemon starts the background syncer when signed in. It never fails a
// call: problems go to the daemon log.
func (s *Service) EnsureDaemon(ctx context.Context) {
	if s.opts.Executable == "" {
		return
	}
	c, err := s.creds.Load(ctx)
	if err != nil || c.State() != auth.StateSignedIn {
		return
	}
	if daemon.IsRunning(s.paths) {
		return
	}
	if !daemon.EnsureRunning(s.opts.Executable, s.paths) {
		s.daemonLog("could not start the daemon from %s", s.opts.Executable)
	}
}

func (s *Service) daemonLog(format string, args ...any) {
	if err := os.MkdirAll(s.paths.Root, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.paths.DaemonLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// RunDaemon is the daemon subcommand: it takes the singleton lock and syncs
// until ctx ends or the user signs out. Losing the lock to a running daemon
// is not an error.
func (s *Service) RunDaemon(ctx context.Context) error {
	inst, err := lock.Open(daemon.LockOptions(s.paths))
	if err == lock.ErrAlreadyRunning {
		return nil
	}
	if err != nil {
		return err
	}
	defer inst.Close()
	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	st, err := s.Store()
	if err != nil {
		return err
	}
	// Holding the lock, no other daemon writes; a temporary file older than
	// an hour is a leftover of a hard stop.
	dirs, _ := st.DownloadDirs()
	if n := daemon.Sweep(s.paths, time.Now(), dirs...); n > 0 {
		logger.Printf("removed %d temporary files left by an earlier stop", n)
	}
	cache, _ := s.assetCache()
	d := &daemon.Daemon{Store: st, Client: s.client, Session: s.session, Cache: cache, Log: logger}
	return d.Serve(ctx)
}

// StopDaemon stops a running daemon.
func (s *Service) StopDaemon() error { return daemon.Stop(s.paths) }

// DaemonRunning reports whether the daemon holds its lock.
func (s *Service) DaemonRunning() bool { return daemon.IsRunning(s.paths) }
