package auth

import (
	"context"
	"errors"
	"time"
)

// LoginRequired is returned when there is no usable session: never signed
// in, signed out, or the refresh token was refused.
type LoginRequired struct {
	State string // StateSignedOut or StateNeedsLogin
}

func (e *LoginRequired) Error() string {
	if e.State == StateNeedsLogin {
		return "the Hyper3D session has ended; sign in again"
	}
	return "not signed in to Hyper3D"
}

// IsLoginRequired reports whether err means the user must sign in.
func IsLoginRequired(err error) bool {
	var lr *LoginRequired
	return errors.As(err, &lr)
}

// refreshMargin: a token closer than this to its expiry is refreshed before
// use.
const refreshMargin = 120 * time.Second

// Session hands out access tokens, refreshing them when due. It is the
// rodin.TokenSource of every process.
type Session struct {
	store *Store
	oauth *OAuth
	now   func() time.Time
}

// NewSession returns a session over store and oauth.
func NewSession(store *Store, oauth *OAuth) *Session {
	return &Session{store: store, oauth: oauth, now: time.Now}
}

// Store returns the credentials store of the session.
func (s *Session) Store() *Store { return s.store }

// Token returns an access token valid for at least refreshMargin, refreshing
// first when needed.
func (s *Session) Token(ctx context.Context) (string, error) {
	c, err := s.store.Load(ctx)
	if err != nil {
		return "", err
	}
	if err := usable(c); err != nil {
		return "", err
	}
	if c.AccessExpiresAt.Sub(s.now()) > refreshMargin {
		return c.AccessToken, nil
	}
	return s.refresh(ctx, true, func(c *Credentials) bool { return c.AccessExpiresAt.Sub(s.now()) <= refreshMargin })
}

// ForceRefresh refreshes after the service refused failed with a 401. When
// another process already replaced failed, its token is returned instead.
func (s *Session) ForceRefresh(ctx context.Context, failed string) (string, error) {
	return s.refresh(ctx, false, func(c *Credentials) bool { return c.AccessToken == failed })
}

// RefreshIfDue refreshes when the access token expires within within (the
// daemon refreshes ahead of time so tools rarely have to). It reports
// whether a refresh happened.
func (s *Session) RefreshIfDue(ctx context.Context, within time.Duration) (bool, error) {
	c, err := s.store.Load(ctx)
	if err != nil {
		return false, err
	}
	if err := usable(c); err != nil {
		return false, err
	}
	if c.AccessExpiresAt.Sub(s.now()) > within {
		return false, nil
	}
	before := c.AccessToken
	tok, err := s.refresh(ctx, true, func(c *Credentials) bool { return c.AccessExpiresAt.Sub(s.now()) <= within })
	return err == nil && tok != before, err
}

func usable(c *Credentials) error {
	switch c.State() {
	case StateSignedOut:
		return &LoginRequired{State: StateSignedOut}
	case StateNeedsLogin:
		return &LoginRequired{State: StateNeedsLogin}
	}
	return nil
}

// refresh runs one refresh under the credentials lock. The stored
// credentials are read again inside the lock and refreshed only when due
// still holds, so concurrent callers refresh once and share the result.
// keepValid lets a refresh that fails for a transient reason fall back to
// the stored token while it has not expired: it was not refused, only near
// its expiry.
func (s *Session) refresh(ctx context.Context, keepValid bool, due func(*Credentials) bool) (string, error) {
	var token string
	err := s.store.WithLock(ctx, func() error {
		c, err := s.store.Load(ctx)
		if err != nil {
			return err
		}
		if err := usable(c); err != nil {
			return err
		}
		if !due(c) {
			token = c.AccessToken
			return nil
		}
		if c.RefreshToken == "" {
			return s.endSession(ctx, c)
		}
		ts, err := s.oauth.Refresh(ctx, c.ClientID, c.RefreshToken)
		if err != nil {
			var oe *OAuthError
			if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "invalid_client") {
				if oe.Code == "invalid_client" {
					s.store.forgetClient()
				}
				return s.endSession(ctx, c)
			}
			// A failed refresh with a token that is still valid is not
			// fatal: use it, the next call tries again.
			if keepValid && c.AccessToken != "" && c.AccessExpiresAt.After(s.now()) {
				token = c.AccessToken
				return nil
			}
			return err
		}
		c.AccessToken = ts.AccessToken
		if ts.RefreshToken != "" {
			c.RefreshToken = ts.RefreshToken
		}
		if ts.Scope != "" {
			c.Scope = ts.Scope
		}
		c.AccessExpiresAt = s.now().Add(time.Duration(ts.ExpiresIn) * time.Second)
		if err := s.store.Save(ctx, c); err != nil {
			return err
		}
		token = c.AccessToken
		return nil
	})
	return token, err
}

// endSession clears the tokens of c, marks it as needing a login, and
// returns the LoginRequired error.
func (s *Session) endSession(ctx context.Context, c *Credentials) error {
	c.AccessToken, c.RefreshToken, c.AccessExpiresAt, c.NeedsLogin = "", "", time.Time{}, true
	if err := s.store.Save(ctx, c); err != nil {
		return err
	}
	return &LoginRequired{State: StateNeedsLogin}
}
