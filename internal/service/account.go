package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/config"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Result is what a call returns to its surface: typed front matter, a
// Markdown body, and PNG images in order.
type Result struct {
	Front  any
	Body   string
	Images []Image
}

// Image is one PNG of a result; What names it for logs and the CLI.
type Image struct {
	PNG  []byte
	What string
}

// Wallet in credits (the service counts tenths).
type Wallet struct {
	Available    float64 `yaml:"available"`
	Regular      float64 `yaml:"regular"`
	Subscription float64 `yaml:"subscription"`
	Frozen       float64 `yaml:"frozen"`
}

func credits(tenths float64) float64 { return math.Round(tenths) / 10 }

// WalletOf reads the wallet of info.
func WalletOf(info *rodin.UserInfo) Wallet {
	sub := 0.0
	for _, a := range info.Meta.Subscriptions.Active {
		sub += a.WalletBalance
	}
	avail := info.Meta.PrepaidAvailable
	if avail == 0 {
		avail = info.Meta.Balance + sub
	}
	return Wallet{Available: credits(avail), Regular: credits(info.Meta.Balance), Subscription: credits(sub), Frozen: credits(info.Meta.Frozen)}
}

type userFront struct {
	Username string `yaml:"username"`
	UUID     string `yaml:"uuid"`
}

type loginFront struct {
	State     string `yaml:"state"`
	ExpiresAt string `yaml:"expires_at,omitempty"`
}

// daemonFront is get_account.daemon: state is the syncer's phase (idle,
// syncing, watching, needs_login, error) while it runs, else "stopped".
type daemonFront struct {
	State     string `yaml:"state"`
	LastSync  string `yaml:"last_sync,omitempty"`
	LastError string `yaml:"last_error,omitempty"`
	Tasks     int    `yaml:"tasks"`
}

type accountFront struct {
	User        userFront          `yaml:"user"`
	Plan        string             `yaml:"plan"`
	Renews      string             `yaml:"renews,omitempty"`
	Wallet      Wallet             `yaml:"wallet"`
	Permissions config.Permissions `yaml:"permissions"`
	ConfigError string             `yaml:"config_error,omitempty"`
	Login       loginFront         `yaml:"login"`
	Daemon      daemonFront        `yaml:"daemon"`
}

// Account is get_account: who is signed in, the plan, the wallet, the write
// permissions, and the state of the session and the syncer.
func (s *Service) Account(ctx context.Context) (Result, error) {
	c, err := s.signedIn(ctx)
	if err != nil {
		return Result{}, err
	}
	info, err := s.client.GetInfo(ctx)
	if err != nil {
		return Result{}, err
	}
	s.EnsureDaemon(ctx)
	cfg, cfgErr := s.Config()
	front := accountFront{
		User:        userFront{Username: info.Meta.Username, UUID: info.Meta.UserUUID},
		Plan:        info.Meta.Subscriptions.HighestLevel,
		Wallet:      WalletOf(info),
		Permissions: cfg.Permission,
		Login:       loginFront{State: auth.StateSignedIn},
		Daemon:      s.daemonState(),
	}
	if c2, err := s.creds.Load(ctx); err == nil && c2 != nil && !c2.AccessExpiresAt.IsZero() {
		front.Login.ExpiresAt = c2.AccessExpiresAt.UTC().Format(time.RFC3339)
	} else if !c.AccessExpiresAt.IsZero() {
		front.Login.ExpiresAt = c.AccessExpiresAt.UTC().Format(time.RFC3339)
	}
	for _, a := range info.Meta.Subscriptions.Active {
		if a.EndTime != "" {
			front.Renews = a.EndTime
			break
		}
	}
	if cfgErr != nil {
		front.ConfigError = cfgErr.Error()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Signed in to Hyper3D as %s (%s plan). Wallet: %s credits available, %s frozen.\n",
		info.Meta.Username, orUnknown(front.Plan), num(front.Wallet.Available), num(front.Wallet.Frozen))
	allowed := []string{}
	for _, k := range config.Keys {
		if cfg.Permission.Allowed(k.Name) {
			allowed = append(allowed, k.Name)
		}
	}
	if len(allowed) == 0 {
		fmt.Fprintf(&b, "\nEvery paid or changing operation is denied in %s; reads need no permission.\n", s.paths.Config)
	} else {
		fmt.Fprintf(&b, "\nAllowed in %s: %s.\n", s.paths.Config, strings.Join(allowed, ", "))
	}
	if cfgErr != nil {
		fmt.Fprintf(&b, "\nThe config file has a problem, so every write is denied until it is fixed: %s\n", cfgErr)
	}
	b.WriteString("\nNext: list_tasks to see the generations, get_generation_options for the settings and the owner's recipes, quote_cost before any paid operation.")
	return Result{Front: front, Body: b.String()}, nil
}

func (s *Service) daemonState() daemonFront {
	running := s.DaemonRunning()
	d := daemonFront{State: "stopped"}
	st, err := s.Store()
	if err != nil {
		d.LastError = err.Error()
		return d
	}
	if state, err := st.State(); err == nil {
		if running {
			d.State = orUnknown(state.Phase)
		}
		d.LastError = state.LastError
		if !state.LastSync.IsZero() {
			d.LastSync = state.LastSync.UTC().Format(time.RFC3339)
		}
	}
	if tasks, err := st.Tasks(); err == nil {
		d.Tasks = len(tasks)
	}
	return d
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func num(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}

type loginStartFront struct {
	AuthorizeURL  string `yaml:"authorize_url"`
	ListeningOn   string `yaml:"listening_on,omitempty"`
	ExpiresIn     int    `yaml:"expires_in"`
	BrowserOpened bool   `yaml:"browser_opened"`
	ReplacesUser  string `yaml:"replaces_user,omitempty"`
}

// LoginInput is the input of login.
type LoginInput struct {
	RedirectURL string
	OpenBrowser bool
}

// Login is the login tool. Without RedirectURL it starts a sign-in and
// returns the link (and a QR code of it); the loopback listener finishes it
// when the browser runs on this machine, and started.Done reports that.
// With RedirectURL it finishes the pending sign-in from the pasted final
// address and returns the account.
func (s *Service) Login(ctx context.Context, in LoginInput) (Result, *auth.Started, error) {
	if strings.TrimSpace(in.RedirectURL) != "" {
		creds, err := s.CompleteLogin(ctx, in.RedirectURL)
		if err != nil {
			return Result{}, nil, err
		}
		res, err := s.Account(ctx)
		if err != nil {
			return Result{}, nil, err
		}
		res.Body = "Signed in as " + creds.Username + ".\n\n" + res.Body
		return res, nil, nil
	}

	started, err := s.login.Start(ctx)
	if err != nil {
		return Result{}, nil, err
	}
	front := loginStartFront{AuthorizeURL: started.AuthorizeURL, ExpiresIn: int(time.Until(started.ExpiresAt).Round(time.Second).Seconds())}
	if started.Listening {
		front.ListeningOn = started.RedirectURI
	}
	if c, err := s.creds.Load(ctx); err == nil && c.State() == auth.StateSignedIn {
		front.ReplacesUser = c.Username
	}
	if in.OpenBrowser && s.opts.OpenBrowser != nil {
		front.BrowserOpened = s.opts.OpenBrowser(started.AuthorizeURL) == nil
	}

	var b strings.Builder
	b.WriteString("Sign in to Hyper3D:\n\n")
	b.WriteString("1. Open the authorize_url on any device (or scan the QR code): sign in to Hyper3D there if asked, then click Authorize on the page \"Connect rodin3d-mcp to Hyper3D task API\". The page says Hyper3D cannot verify the app's identity because rodin3d-mcp registered itself; it shows the redirect address " + auth.RedirectURI(auth.Ports[0]) + " (the first one rodin3d-mcp registered) whichever port this sign-in uses.\n")
	if started.Listening {
		b.WriteString("2. If that browser runs on this computer, the sign-in finishes by itself and the tab says so.\n")
		b.WriteString("3. Otherwise the browser shows a page that cannot connect to 127.0.0.1. Copy that page's whole address and call login with it as redirect_url.\n")
	} else {
		b.WriteString("2. Every sign-in port was busy, so nothing listens on this computer: after Authorize the browser shows a page that cannot connect to 127.0.0.1. Copy that page's whole address and call login with it as redirect_url.\n")
	}
	fmt.Fprintf(&b, "\nThe link works for %d minutes.", front.ExpiresIn/60)
	if front.ReplacesUser != "" {
		fmt.Fprintf(&b, " Finishing it replaces the current session of %s.", front.ReplacesUser)
	}
	b.WriteString("\n\nNext: give the user the link; when they say they approved, call get_account.")

	res := Result{Front: front, Body: b.String()}
	if png, err := qrcode.Encode(started.AuthorizeURL, qrcode.Medium, 512); err == nil {
		res.Images = append(res.Images, Image{PNG: png, What: "sign-in QR code"})
	}
	return res, started, nil
}

// QRText renders address as a QR code made of terminal characters.
func QRText(address string) string {
	q, err := qrcode.New(address, qrcode.Low)
	if err != nil {
		return ""
	}
	return q.ToSmallString(false)
}

// AfterLogin runs once a sign-in completed: the syncer starts.
func (s *Service) AfterLogin(ctx context.Context, creds *auth.Credentials) {
	if creds != nil {
		s.EnsureDaemon(ctx)
	}
}

// CompleteLogin finishes a sign-in from a pasted address (the CLI uses it
// while its listener also waits).
func (s *Service) CompleteLogin(ctx context.Context, address string) (*auth.Credentials, error) {
	creds, err := s.login.Complete(ctx, address)
	if err == nil {
		s.login.StopListener()
		s.AfterLogin(ctx, creds)
	}
	return creds, err
}

type logoutFront struct {
	WasSignedIn   bool   `yaml:"was_signed_in"`
	Revoked       bool   `yaml:"revoked"`
	RevokePending bool   `yaml:"revoke_pending"`
	RevokeError   string `yaml:"revoke_error,omitempty"`
	DaemonStopped bool   `yaml:"daemon_stopped"`
}

// Logout is the logout tool: revoke the grant, delete the tokens, stop the
// syncer. The task index stays.
func (s *Service) Logout(ctx context.Context) (Result, error) {
	res, err := s.login.Logout(ctx)
	if err != nil {
		return Result{}, err
	}
	front := logoutFront{WasSignedIn: res.WasSignedIn, Revoked: res.Revoked, RevokePending: res.RevokePending}
	if res.RevokeError != nil {
		front.RevokeError = res.RevokeError.Error()
	}
	if s.DaemonRunning() {
		if err := s.StopDaemon(); err == nil {
			front.DaemonStopped = true
		}
	}
	var b strings.Builder
	switch {
	case res.RevokePending:
		fmt.Fprintf(&b, "Signed out on this computer, but Hyper3D could not be asked to revoke the grant (%s). "+
			"Its refresh token is kept in %s for one purpose, the revoke, and the grant stays valid until then "+
			"(its lifetime is not measured). Call logout again when the network is back to finish the revoke.", front.RevokeError, s.paths.RevokePending)
	case res.RevokeError != nil && !res.Revoked:
		fmt.Fprintf(&b, "Signed out on this computer: the tokens are deleted. Hyper3D refused to revoke the grant (%s), "+
			"which a retry would not change; the grant ends when its refresh token lapses (its lifetime is not measured).", front.RevokeError)
	case res.RevokeError != nil:
		// Revoked too: one grant was revoked, another was refused for good.
		fmt.Fprintf(&b, "Signed out on this computer: the tokens are deleted. One Hyper3D grant is revoked; another was refused (%s), "+
			"which a retry would not change; that grant ends when its refresh token lapses (its lifetime is not measured).", front.RevokeError)
	case !res.WasSignedIn && !res.Revoked:
		b.WriteString("Not signed in; nothing to sign out of.")
	default:
		b.WriteString("Signed out: the Hyper3D grant is revoked and the tokens on this computer are deleted. The access token that was in use cannot be revoked by Hyper3D and lapses within the hour; it is deleted here.")
	}
	b.WriteString(" The task index stays in " + s.paths.Database + ".")
	if res.RevokePending {
		b.WriteString("\n\nNext: logout again to finish the revoke; login to sign in again.")
	} else {
		b.WriteString("\n\nNext: login to sign in again.")
	}
	return Result{Front: front, Body: b.String()}, nil
}

// storeOrFail opens the store, classifying the failure.
func (s *Service) storeOrFail() (*store.Store, error) {
	st, err := s.Store()
	if err != nil {
		return nil, wrapf(err, "open the task index %s", s.paths.Database)
	}
	return st, nil
}
