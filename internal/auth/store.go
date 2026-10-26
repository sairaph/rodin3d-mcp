package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/sairaph/mcp-wizard/secret"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// Credentials is the signed-in session as stored in credentials.json.
type Credentials struct {
	ClientID        string
	RedirectURI     string
	AccessToken     string
	AccessExpiresAt time.Time
	RefreshToken    string
	Scope           string
	UserUUID        string
	Username        string
	Email           string
	ObtainedAt      time.Time
	// NeedsLogin is set when the refresh token was refused (invalid_grant):
	// the tokens are cleared and only a new login helps.
	NeedsLogin bool
}

// Login states reported to every surface.
const (
	StateSignedIn   = "signed_in"
	StateNeedsLogin = "needs_login"
	StateSignedOut  = "signed_out"
)

// State is the login state of c (nil means signed out).
func (c *Credentials) State() string {
	switch {
	case c == nil:
		return StateSignedOut
	case c.NeedsLogin || c.RefreshToken == "" && c.AccessToken == "":
		return StateNeedsLogin
	}
	return StateSignedIn
}

const timeFormat = time.RFC3339

func (c *Credentials) toSession() *secret.Session {
	s := secret.NewSession()
	s.Set("client_id", c.ClientID)
	s.Set("redirect_uri", c.RedirectURI)
	s.Set("access_token", c.AccessToken)
	if !c.AccessExpiresAt.IsZero() {
		s.Set("access_expires_at", c.AccessExpiresAt.UTC().Format(timeFormat))
	}
	s.Set("refresh_token", c.RefreshToken)
	s.Set("scope", c.Scope)
	s.Set("user_uuid", c.UserUUID)
	s.Set("username", c.Username)
	s.Set("email", c.Email)
	if !c.ObtainedAt.IsZero() {
		s.Set("obtained_at", c.ObtainedAt.UTC().Format(timeFormat))
	}
	if c.NeedsLogin {
		s.Set("needs_login", true)
	}
	return s
}

func fromSession(s *secret.Session) *Credentials {
	parse := func(k string) time.Time {
		t, _ := time.Parse(timeFormat, s.GetString(k))
		return t
	}
	needs, _ := s.Get("needs_login").(bool)
	return &Credentials{
		ClientID:        s.GetString("client_id"),
		RedirectURI:     s.GetString("redirect_uri"),
		AccessToken:     s.GetString("access_token"),
		AccessExpiresAt: parse("access_expires_at"),
		RefreshToken:    s.GetString("refresh_token"),
		Scope:           s.GetString("scope"),
		UserUUID:        s.GetString("user_uuid"),
		Username:        s.GetString("username"),
		Email:           s.GetString("email"),
		ObtainedAt:      parse("obtained_at"),
		NeedsLogin:      needs,
	}
}

// Store reads and writes credentials.json and holds the refresh lock.
type Store struct {
	file     *secret.FileStore
	lockPath string
	client   string // client.json
	pending  string // pending-login.json
	revoke   string // revoke-pending.json
}

// NewStore returns the store for paths.
func NewStore(paths domain.Paths) *Store {
	return &Store{
		file:     secret.NewFileStore(paths.Credentials),
		lockPath: paths.CredentialsLock,
		client:   paths.Client,
		pending:  paths.PendingLogin,
		revoke:   paths.RevokePending,
	}
}

// Path is the credentials file.
func (s *Store) Path() string { return s.file.Path() }

// Load returns the stored credentials, or nil when there are none.
func (s *Store) Load(ctx context.Context) (*Credentials, error) {
	sess, exists, err := s.file.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return fromSession(sess), nil
}

// Save writes c atomically with mode 0600, in the format secret.FileStore
// reads, through domain.WriteFileAtomic: its Windows replace retries when
// another process holds the file for a moment, which a lost write after a
// refresh-token rotation cannot afford.
func (s *Store) Save(_ context.Context, c *Credentials) error {
	data, err := json.MarshalIndent(c.toSession().Values, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	return domain.WriteFileAtomic(s.file.Path(), data, 0o600)
}

// revokeRecord is one entry of revoke-pending.json: a grant whose revoke
// failed at logout.
type revokeRecord struct {
	ClientID     string    `json:"client_id"`
	RefreshToken string    `json:"refresh_token"`
	Since        time.Time `json:"since"`
}

// saveRevokes writes the grants still to revoke; none removes the file.
func (s *Store) saveRevokes(rs []revokeRecord) error {
	if len(rs) == 0 {
		_ = os.Remove(s.revoke)
		return nil
	}
	data, _ := json.MarshalIndent(rs, "", "  ")
	return domain.WriteFileAtomic(s.revoke, data, 0o600)
}

func (s *Store) loadRevokes() []revokeRecord {
	data, err := os.ReadFile(s.revoke)
	if err != nil {
		return nil
	}
	var rs []revokeRecord
	if json.Unmarshal(data, &rs) != nil {
		return nil
	}
	return rs
}

// RevokePending reports whether a failed revoke waits for a retry.
func (s *Store) RevokePending() bool { return len(s.loadRevokes()) > 0 }

// Delete removes the credentials file.
func (s *Store) Delete(ctx context.Context) error { return s.file.Delete(ctx) }

// lockTimeout bounds the wait for another process's refresh.
const lockTimeout = 30 * time.Second

// WithLock runs fn while holding credentials.lock, so a refresh and the
// write of its rotated token are one step for every process.
func (s *Store) WithLock(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.lockPath), 0o700); err != nil {
		return err
	}
	lock := flock.New(s.lockPath)
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("take credentials lock: %w", err)
	}
	if !ok {
		return errors.New("take credentials lock: timed out")
	}
	defer lock.Unlock()
	return fn()
}

// clientRecord is client.json: the registered client, kept across logouts
// so a new login reuses it (registered clients cannot be deleted).
type clientRecord struct {
	ClientID     string    `json:"client_id"`
	Issuer       string    `json:"issuer"`
	RegisteredAt time.Time `json:"registered_at"`
}

func (s *Store) loadClient(issuer string) string {
	data, err := os.ReadFile(s.client)
	if err != nil {
		return ""
	}
	var rec clientRecord
	if json.Unmarshal(data, &rec) != nil || rec.Issuer != issuer {
		return ""
	}
	return rec.ClientID
}

func (s *Store) saveClient(issuer, clientID string, now time.Time) error {
	data, _ := json.MarshalIndent(clientRecord{ClientID: clientID, Issuer: issuer, RegisteredAt: now.UTC()}, "", "  ")
	return domain.WriteFileAtomic(s.client, data, 0o600)
}

func (s *Store) forgetClient() { _ = os.Remove(s.client) }

// pendingLogin is pending-login.json: one sign-in in progress.
type pendingLogin struct {
	ClientID    string    `json:"client_id"`
	RedirectURI string    `json:"redirect_uri"`
	State       string    `json:"state"`
	Verifier    string    `json:"verifier"`
	Issuer      string    `json:"issuer"`
	CreatedAt   time.Time `json:"created_at"`
	// Completed marks a sign-in that finished; State then identifies the
	// answer that finished it.
	Completed bool `json:"completed,omitempty"`
}

func (s *Store) savePending(p pendingLogin) error {
	data, _ := json.MarshalIndent(p, "", "  ")
	return domain.WriteFileAtomic(s.pending, data, 0o600)
}

func (s *Store) loadPending() (*pendingLogin, error) {
	data, err := os.ReadFile(s.pending)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pendingLogin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil
	}
	return &p, nil
}

func (s *Store) deletePending() { _ = os.Remove(s.pending) }
