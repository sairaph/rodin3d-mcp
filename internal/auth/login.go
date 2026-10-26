package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoginWindow is how long a started sign-in stays open: the listener and the
// pending PKCE state end after one code or this long.
const LoginWindow = 10 * time.Minute

// Account is what the verification call learns about the signed-in user.
type Account struct {
	UserUUID string
	Username string
	Email    string
}

// VerifyFunc checks a fresh access token against the service (user/get_info)
// and returns the account it belongs to.
type VerifyFunc func(ctx context.Context, accessToken string) (Account, error)

// Errors of Complete that are the user's to fix.
var (
	ErrNoPendingLogin = errors.New("no sign-in is in progress; start one with login (no address) first")
	ErrLoginExpired   = errors.New("the sign-in link has expired; start a new sign-in with login (no address)")
	ErrStateMismatch  = errors.New("the address belongs to a different sign-in attempt (state does not match); use the address from the latest link, or start again")
	ErrNotRedirect    = errors.New("the pasted text is not the final sign-in address: paste the whole address that starts with http://127.0.0.1:")
	ErrLoginReplaced  = errors.New("the sign-in was replaced by a newer one or cancelled")
)

// LoginRejected is an authorization reply that carries an error (the user
// pressed Deny, or the server refused the request).
type LoginRejected struct {
	Code        string
	Description string
}

func (e *LoginRejected) Error() string {
	s := "the sign-in was not approved: " + e.Code
	if e.Description != "" {
		s += " (" + e.Description + ")"
	}
	return s
}

// Started is a sign-in waiting for the user.
type Started struct {
	AuthorizeURL string
	RedirectURI  string
	// Listening is false when every registered port was busy: the user then
	// pastes the final address.
	Listening bool
	ExpiresAt time.Time
	// Done receives the outcome of the loopback listener once (never when
	// Listening is false). A pasted address completes through Complete.
	Done <-chan Outcome
}

// Outcome is how a sign-in through the listener ended.
type Outcome struct {
	Credentials *Credentials
	Err         error
}

// Manager runs sign-ins and sign-outs.
type Manager struct {
	store  *Store
	oauth  *OAuth
	verify VerifyFunc
	now    func() time.Time

	mu     sync.Mutex
	active *http.Server
}

// NewManager returns a manager. verify is called with every new access token
// before it is stored.
func NewManager(store *Store, oauth *OAuth, verify VerifyFunc) *Manager {
	return &Manager{store: store, oauth: oauth, verify: verify, now: time.Now}
}

// Start begins a sign-in: it makes sure the OAuth client is registered,
// binds the first free loopback port, records the PKCE state, and serves the
// callback in the background until one code arrives or LoginWindow passes.
// A sign-in already running in this process is replaced.
func (m *Manager) Start(ctx context.Context) (*Started, error) {
	issuer := m.oauth.Endpoints().Issuer
	clientID := m.store.loadClient(issuer)
	if clientID == "" {
		id, err := m.oauth.Register(ctx)
		if err != nil {
			return nil, fmt.Errorf("register the OAuth client: %w", err)
		}
		if err := m.store.saveClient(issuer, id, m.now()); err != nil {
			return nil, err
		}
		clientID = id
	}

	m.stopActive()
	var ln net.Listener
	redirect := RedirectURI(Ports[0])
	for _, port := range Ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			ln, redirect = l, RedirectURI(port)
			break
		}
	}
	p, err := NewPKCE()
	if err != nil {
		if ln != nil {
			ln.Close()
		}
		return nil, err
	}
	now := m.now()
	if err := m.store.savePending(pendingLogin{
		ClientID: clientID, RedirectURI: redirect, State: p.State, Verifier: p.Verifier, Issuer: issuer, CreatedAt: now.UTC(),
	}); err != nil {
		if ln != nil {
			ln.Close()
		}
		return nil, err
	}
	started := &Started{
		AuthorizeURL: m.oauth.AuthorizeURL(clientID, redirect, p),
		RedirectURI:  redirect,
		Listening:    ln != nil,
		ExpiresAt:    now.Add(LoginWindow),
	}
	if ln != nil {
		started.Done = m.serve(ln)
	}
	return started, nil
}

// serve answers the loopback callback until one sign-in completes, fails for
// good, or LoginWindow passes.
func (m *Manager) serve(ln net.Listener) <-chan Outcome {
	done := make(chan Outcome, 1)
	finished := make(chan struct{})
	var once sync.Once
	finish := func(o Outcome) {
		once.Do(func() {
			done <- o
			close(finished)
		})
	}

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		creds, err := m.Complete(ctx, r.URL.String())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if errors.Is(err, ErrStateMismatch) {
			// A tab from an older attempt: keep waiting for the current one.
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, page("This sign-in link is out of date", "Open the latest link that rodin3d-mcp printed, then approve again."))
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, page("Sign-in failed", err.Error()))
		} else {
			fmt.Fprint(w, page("Signed in to Hyper3D", "rodin3d-mcp is signed in as "+creds.Username+". You can close this tab."))
		}
		finish(Outcome{Credentials: creds, Err: err})
		go m.stop(srv)
	})
	srv.Handler = mux

	m.mu.Lock()
	m.active = srv
	m.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
		// Serve returns once srv is shut down: after a finished sign-in
		// (finish already ran), or when a newer Start or a logout replaced it.
		finish(Outcome{Err: ErrLoginReplaced})
	}()
	go func() {
		t := time.NewTimer(LoginWindow)
		defer t.Stop()
		select {
		case <-t.C:
			finish(Outcome{Err: ErrLoginExpired})
			m.stop(srv)
		case <-finished:
		}
	}()
	return done
}

// stop shuts srv down and forgets it when it is the active listener.
func (m *Manager) stop(srv *http.Server) {
	m.mu.Lock()
	if m.active == srv {
		m.active = nil
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// StopListener ends the loopback listener of this process, if any (after a
// pasted address finished the sign-in). Never call it from the callback
// handler itself.
func (m *Manager) StopListener() { m.stopActive() }

// stopActive ends the listener of an earlier Start in this process.
func (m *Manager) stopActive() {
	m.mu.Lock()
	srv := m.active
	m.active = nil
	m.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func page(title, text string) string {
	return "<!doctype html><meta charset=utf-8><title>" + html.EscapeString(title) +
		"</title><body style=\"font-family:sans-serif;margin:3em\"><h2>" + html.EscapeString(title) +
		"</h2><p>" + html.EscapeString(text) + "</p></body>"
}

// parseRedirect accepts the final address in full (http://127.0.0.1:53682/
// callback?code=...), as a path with a query (/callback?code=...), or as the
// query alone (code=...&state=...), and returns its query values.
func parseRedirect(address string) (url.Values, error) {
	a := strings.TrimSpace(address)
	a = strings.Trim(a, `"'<>`)
	if a == "" {
		return nil, ErrNotRedirect
	}
	query := a
	if i := strings.IndexByte(a, '?'); i >= 0 {
		query = a[i+1:]
	} else if strings.Contains(a, "://") {
		return nil, ErrNotRedirect
	}
	if i := strings.IndexByte(query, '#'); i >= 0 {
		query = query[:i]
	}
	q, err := url.ParseQuery(query)
	if err != nil || (q.Get("code") == "" && q.Get("error") == "") || q.Get("state") == "" {
		return nil, ErrNotRedirect
	}
	return q, nil
}

// Complete finishes the pending sign-in with its final address: it checks
// state and issuer, exchanges the code, verifies the token and stores the
// credentials. When another process (the listener) finished the same
// sign-in moments ago, its credentials are returned.
func (m *Manager) Complete(ctx context.Context, address string) (*Credentials, error) {
	q, err := parseRedirect(address)
	if err != nil {
		return nil, err
	}
	var creds *Credentials
	err = m.store.WithLock(ctx, func() error {
		p, err := m.store.loadPending()
		if err != nil {
			return err
		}
		if p == nil {
			if c, _ := m.store.Load(ctx); c.State() == StateSignedIn && m.now().Sub(c.ObtainedAt) < LoginWindow {
				creds = c
				return nil
			}
			return ErrNoPendingLogin
		}
		if m.now().Sub(p.CreatedAt) > LoginWindow {
			m.store.deletePending()
			return ErrLoginExpired
		}
		if q.Get("state") != p.State {
			return ErrStateMismatch
		}
		if iss := q.Get("iss"); iss != "" && iss != p.Issuer {
			return fmt.Errorf("the sign-in answer came from %q, not from %q; start again", iss, p.Issuer)
		}
		if code := q.Get("error"); code != "" {
			m.store.deletePending()
			return &LoginRejected{Code: code, Description: q.Get("error_description")}
		}
		ts, err := m.oauth.Exchange(ctx, p.ClientID, p.RedirectURI, q.Get("code"), p.Verifier)
		if err != nil {
			var oe *OAuthError
			if errors.As(err, &oe) && (oe.Code == "invalid_client" || oe.Code == "invalid_grant") {
				m.store.deletePending()
				if oe.Code == "invalid_client" {
					m.store.forgetClient()
				}
			}
			return fmt.Errorf("exchange the sign-in code: %w", err)
		}
		acct, err := m.verify(ctx, ts.AccessToken)
		if err != nil {
			return fmt.Errorf("check the new session: %w", err)
		}
		if acct.UserUUID == "" {
			return errors.New("check the new session: the service returned no user")
		}
		now := m.now()
		c := &Credentials{
			ClientID: p.ClientID, RedirectURI: p.RedirectURI,
			AccessToken: ts.AccessToken, AccessExpiresAt: now.Add(time.Duration(ts.ExpiresIn) * time.Second),
			RefreshToken: ts.RefreshToken, Scope: ts.Scope,
			UserUUID: acct.UserUUID, Username: acct.Username, Email: acct.Email,
			ObtainedAt: now,
		}
		if err := m.store.Save(ctx, c); err != nil {
			return err
		}
		m.store.deletePending()
		creds = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// Pending reports the sign-in in progress, if any: its redirect and expiry.
func (m *Manager) Pending() (redirect string, expires time.Time, ok bool) {
	p, err := m.store.loadPending()
	if err != nil || p == nil || m.now().Sub(p.CreatedAt) > LoginWindow {
		return "", time.Time{}, false
	}
	return p.RedirectURI, p.CreatedAt.Add(LoginWindow), true
}

// LogoutResult says what Logout did.
type LogoutResult struct {
	WasSignedIn bool
	Revoked     bool
	RevokeError error // why the revoke failed; local tokens are deleted anyway
}

// Logout revokes the refresh token (ending the grant session on the
// server), then deletes the credentials and any pending sign-in. The access
// token cannot be revoked server-side and lapses within the hour; it is
// deleted with the rest. The registered client (client.json) is kept.
func (m *Manager) Logout(ctx context.Context) (LogoutResult, error) {
	m.stopActive()
	var res LogoutResult
	err := m.store.WithLock(ctx, func() error {
		c, err := m.store.Load(ctx)
		if err != nil {
			return err
		}
		m.store.deletePending()
		if c == nil {
			return nil
		}
		res.WasSignedIn = c.State() == StateSignedIn
		if c.RefreshToken != "" && c.ClientID != "" {
			if err := m.oauth.Revoke(ctx, c.ClientID, c.RefreshToken); err != nil {
				res.RevokeError = err
			} else {
				res.Revoked = true
			}
		}
		return m.store.Delete(ctx)
	})
	return res, err
}
