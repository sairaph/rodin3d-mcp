package auth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
)

type env struct {
	fake    *fakehyper.Server
	paths   domain.Paths
	store   *auth.Store
	oauth   *auth.OAuth
	session *auth.Session
	mgr     *auth.Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fake := fakehyper.New()
	t.Cleanup(fake.Close)
	paths := domain.PathsUnder(t.TempDir())
	st := auth.NewStore(paths)
	oauth := auth.NewOAuth(auth.EndpointsFor(fake.Base()), "test")
	verify := func(ctx context.Context, token string) (auth.Account, error) {
		info, err := rodin.New(fake.Base(), rodin.StaticToken(token), "test").GetInfo(ctx)
		if err != nil {
			return auth.Account{}, err
		}
		return auth.Account{UserUUID: info.Meta.UserUUID, Username: info.Meta.Username, Email: info.Meta.Email}, nil
	}
	return &env{fake: fake, paths: paths, store: st, oauth: oauth, session: auth.NewSession(st, oauth), mgr: auth.NewManager(st, oauth, verify)}
}

func (e *env) signIn(t *testing.T) *auth.Credentials {
	t.Helper()
	started, err := e.mgr.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := e.fake.Approve(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := e.mgr.Complete(context.Background(), redirect)
	if err != nil {
		t.Fatal(err)
	}
	e.mgr.StopListener()
	return creds
}

func TestLoginThroughTheLoopbackListener(t *testing.T) {
	e := newEnv(t)
	started, err := e.mgr.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !started.Listening || !strings.HasPrefix(started.RedirectURI, "http://127.0.0.1:5368") {
		t.Fatalf("started %+v", started)
	}
	for _, want := range []string{"code_challenge_method=S256", "resource=", "scope=openid+offline_access+frontend%3Aaccess", e.fake.Issuer() + "/oauth2/authorize?"} {
		if !strings.Contains(started.AuthorizeURL, want) {
			t.Errorf("authorize URL lacks %q", want)
		}
	}
	redirect, err := e.fake.Approve(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(redirect) // the browser follows the redirect to the loopback
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Signed in") {
		t.Fatalf("callback page %d %s", resp.StatusCode, page)
	}
	select {
	case o := <-started.Done:
		if o.Err != nil || o.Credentials.Username != "owner" {
			t.Fatalf("outcome %+v", o)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not finish")
	}
	c, err := e.store.Load(context.Background())
	if err != nil || c.State() != auth.StateSignedIn || c.UserUUID == "" || c.RefreshToken == "" || c.ClientID == "" {
		t.Fatalf("stored %+v %v", c, err)
	}
	if fi, err := os.Stat(e.paths.Credentials); err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("credentials file: %v", err)
	}
	// The finished sign-in stays recorded (to recognise the same answer
	// again) without its PKCE verifier, and is no longer pending.
	data, err := os.ReadFile(e.paths.PendingLogin)
	if err != nil || !strings.Contains(string(data), `"completed": true`) || strings.Contains(string(data), `"verifier": "`+"x") {
		t.Fatalf("completed record: %v %s", err, data)
	}
	if strings.Contains(string(data), `"verifier": "`) && !strings.Contains(string(data), `"verifier": ""`) {
		t.Fatal("the verifier outlived the sign-in")
	}
	if _, _, ok := e.mgr.Pending(); ok {
		t.Fatal("a finished sign-in still reports as pending")
	}
}

func TestPastedAddressAndItsChecks(t *testing.T) {
	e := newEnv(t)
	started, err := e.mgr.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer e.mgr.StopListener()
	redirect, _ := e.fake.Approve(started.AuthorizeURL)

	if _, err := e.mgr.Complete(context.Background(), "hello"); !errors.Is(err, auth.ErrNotRedirect) {
		t.Errorf("garbage: %v", err)
	}
	wrongState := strings.Replace(redirect, "state=", "state=x", 1)
	if _, err := e.mgr.Complete(context.Background(), wrongState); !errors.Is(err, auth.ErrStateMismatch) {
		t.Errorf("state: %v", err)
	}
	// Only the query part, quoted the way a terminal paste may carry it.
	query := redirect[strings.Index(redirect, "?")+1:]
	creds, err := e.mgr.Complete(context.Background(), `"`+query+`"`)
	if err != nil || creds.Username != "owner" {
		t.Fatalf("paste: %v", err)
	}
	// The same address again (the listener racing the paste) returns the session.
	if again, err := e.mgr.Complete(context.Background(), redirect); err != nil || again.AccessToken != creds.AccessToken {
		t.Fatalf("second completion: %v", err)
	}
}

func TestDeniedConsent(t *testing.T) {
	e := newEnv(t)
	started, _ := e.mgr.Start(context.Background())
	defer e.mgr.StopListener()
	redirect, _ := e.fake.Approve(started.AuthorizeURL)
	u, _ := url.Parse(redirect)
	_, err := e.mgr.Complete(context.Background(), "http://127.0.0.1:53682/callback?error=access_denied&state="+u.Query().Get("state"))
	var rej *auth.LoginRejected
	if !errors.As(err, &rej) || rej.Code != "access_denied" {
		t.Fatalf("err %v", err)
	}
}

func TestClientIsRegisteredOnce(t *testing.T) {
	e := newEnv(t)
	e.signIn(t)
	first, _ := e.store.Load(context.Background())
	if _, err := e.mgr.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.signIn(t)
	second, _ := e.store.Load(context.Background())
	if first.ClientID != second.ClientID {
		t.Fatalf("client registered again: %s vs %s", first.ClientID, second.ClientID)
	}
}

func TestTokenRefreshesWhenDueAndRotates(t *testing.T) {
	e := newEnv(t)
	creds := e.signIn(t)
	tok, err := e.session.Token(context.Background())
	if err != nil || tok != creds.AccessToken || e.fake.Refreshes() != 0 {
		t.Fatalf("fresh token refreshed: %v", err)
	}
	creds.AccessExpiresAt = time.Now().Add(30 * time.Second)
	if err := e.store.Save(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	// Many callers at once refresh once.
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = e.session.Token(context.Background())
		}(i)
	}
	wg.Wait()
	if e.fake.Refreshes() != 1 {
		t.Fatalf("%d refreshes, want 1", e.fake.Refreshes())
	}
	for _, g := range got {
		if g == "" || g == creds.AccessToken || g != got[0] {
			t.Fatalf("tokens after refresh: %v", got)
		}
	}
	after, _ := e.store.Load(context.Background())
	if after.RefreshToken == creds.RefreshToken {
		t.Fatal("rotated refresh token not stored")
	}
}

func TestForceRefreshAfter401(t *testing.T) {
	e := newEnv(t)
	e.signIn(t)
	client := rodin.New(e.fake.Base(), e.session, "test")
	e.fake.Reject401 = 1
	if _, err := client.GetInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.fake.Refreshes() != 1 {
		t.Fatalf("%d refreshes", e.fake.Refreshes())
	}
}

func TestRefusedRefreshNeedsLogin(t *testing.T) {
	e := newEnv(t)
	creds := e.signIn(t)
	e.fake.RevokeAll()
	creds.AccessExpiresAt = time.Now()
	_ = e.store.Save(context.Background(), creds)
	_, err := e.session.Token(context.Background())
	if !auth.IsLoginRequired(err) {
		t.Fatalf("err %v", err)
	}
	c, _ := e.store.Load(context.Background())
	if c.State() != auth.StateNeedsLogin || c.RefreshToken != "" || c.AccessToken != "" {
		t.Fatalf("after invalid_grant: %+v", c)
	}
}

func TestLogoutRevokes(t *testing.T) {
	e := newEnv(t)
	e.signIn(t)
	res, err := e.mgr.Logout(context.Background())
	if err != nil || !res.Revoked || !res.WasSignedIn || !e.fake.GrantRevoked() {
		t.Fatalf("logout %+v %v", res, err)
	}
	if c, _ := e.store.Load(context.Background()); c != nil {
		t.Fatal("credentials kept")
	}
	if _, err := e.session.Token(context.Background()); !auth.IsLoginRequired(err) {
		t.Fatalf("token after logout: %v", err)
	}
}

func TestExpiredPendingLogin(t *testing.T) {
	e := newEnv(t)
	started, _ := e.mgr.Start(context.Background())
	e.mgr.StopListener()
	redirect, _ := e.fake.Approve(started.AuthorizeURL)
	data, _ := os.ReadFile(e.paths.PendingLogin)
	old := strings.Replace(string(data), time.Now().UTC().Format("2006-01-02"), "2000-01-01", 1)
	_ = os.WriteFile(e.paths.PendingLogin, []byte(old), 0o600)
	if _, err := e.mgr.Complete(context.Background(), redirect); !errors.Is(err, auth.ErrLoginExpired) {
		t.Fatalf("err %v", err)
	}
}
