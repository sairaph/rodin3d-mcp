package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

type fixture struct {
	fake  *fakehyper.Server
	store *store.Store
	d     *daemon.Daemon
	log   *bytes.Buffer
	creds *auth.Store
}

func setup(t *testing.T) *fixture {
	t.Helper()
	fake := fakehyper.Standard()
	t.Cleanup(fake.Close)
	paths := domain.PathsUnder(t.TempDir())
	cs := auth.NewStore(paths)
	oauth := auth.NewOAuth(auth.EndpointsFor(fake.Base()), "test")
	mgr := auth.NewManager(cs, oauth, func(ctx context.Context, tok string) (auth.Account, error) {
		info, err := rodin.New(fake.Base(), rodin.StaticToken(tok), "test").GetInfo(ctx)
		if err != nil {
			return auth.Account{}, err
		}
		return auth.Account{UserUUID: info.Meta.UserUUID, Username: info.Meta.Username}, nil
	})
	started, err := mgr.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mgr.StopListener()
	redirect, _ := fake.Approve(started.AuthorizeURL)
	if _, err := mgr.Complete(context.Background(), redirect); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	session := auth.NewSession(cs, oauth)
	client := rodin.New(fake.Base(), session, "test")
	var buf bytes.Buffer
	d := &daemon.Daemon{Store: st, Client: client, Session: session, Cache: assets.NewCache(paths.Cache, st, client), Log: log.New(&buf, "", 0)}
	return &fixture{fake: fake, store: st, d: d, log: &buf, creds: cs}
}

func TestFirstCycleSyncsHistoryAndCachesImages(t *testing.T) {
	f := setup(t)
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks, _ := f.store.Tasks()
	if len(tasks) != 5 {
		t.Fatalf("%d tasks, want 5", len(tasks))
	}
	if tasks[0].UUID != fakehyper.RunningID {
		t.Errorf("not newest first: %s", tasks[0].UUID)
	}
	packed, _ := f.store.Task(fakehyper.PackedID)
	if packed.Mode != "gen-2.5" || packed.Tier != "high" || strings.Join(packed.Views, ",") != "F,B" || packed.UserState != "DoneBasic" {
		t.Errorf("packed row %+v", packed)
	}
	st, _ := f.store.State()
	if st.LastSync.IsZero() || st.UserUUID == "" {
		t.Errorf("state %+v", st)
	}
	card, _, err := f.store.Card(fakehyper.PackedID)
	if err != nil || bytes.Contains(card, []byte(fakehyper.SignatureMark)) {
		t.Fatalf("stored card keeps a signature or is missing: %v", err)
	}
	files, _ := f.store.Files(fakehyper.PackedID, "cache:")
	if len(files) != 3 { // poster, geometry, material
		t.Errorf("cached %d images: %+v", len(files), files)
	}
	for _, file := range files {
		if _, err := os.Stat(file.Path); err != nil {
			t.Error(err)
		}
	}
	if strings.Contains(f.log.String(), fakehyper.SignatureMark) {
		t.Fatal("log holds a signature")
	}
	// A second cycle inside the interval lists nothing again.
	before := f.fake.Calls("task/list_Rodin")
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.fake.Calls("task/list_Rodin") != before {
		t.Error("history listed again before the interval")
	}
}

func TestWatchFollowsARunningTask(t *testing.T) {
	f := setup(t)
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := f.d.Cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run, _ := f.store.Task(fakehyper.RunningID)
	if run.UserState != "DonePreview" || run.PendingJobs != 0 {
		t.Fatalf("running task not followed: %+v", run)
	}
	evs, _ := f.store.Events(fakehyper.RunningID)
	if len(evs) < 2 || evs[len(evs)-1].UserState != "DonePreview" {
		t.Errorf("events %+v", evs)
	}
	if ids, _ := f.store.InFlight(); len(ids) != 0 {
		t.Errorf("still in flight: %v", ids)
	}
	st, _ := f.store.State()
	if st.Phase != store.PhaseIdle {
		t.Errorf("phase %s", st.Phase)
	}
}

func TestRefusedSessionIsNeedsLogin(t *testing.T) {
	f := setup(t)
	f.fake.RevokeAll()
	f.fake.ExpireAccess()
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := f.store.State()
	if st.Phase != store.PhaseNeedsLogin {
		t.Fatalf("phase %s (%s)", st.Phase, st.LastError)
	}
}

func TestBodyLevelRefusalRefreshesOnce(t *testing.T) {
	f := setup(t)
	// One refusal: the forced refresh cures it and the sync goes on.
	f.fake.RefuseBody(1)
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.fake.Refreshes() != 1 {
		t.Errorf("%d refreshes, want 1", f.fake.Refreshes())
	}
	if tasks, _ := f.store.Tasks(); len(tasks) != 5 {
		t.Errorf("%d tasks after a cured refusal", len(tasks))
	}
}

func TestBodyLevelRefusalTwiceIsNeedsLogin(t *testing.T) {
	f := setup(t)
	f.fake.RefuseBody(2) // the call and its one retry after the refresh
	err := f.d.Cycle(context.Background())
	if !errors.Is(err, rodin.ErrUnauthorized) {
		t.Fatalf("err %v, want an authorization error (so Serve backs off)", err)
	}
	st, _ := f.store.State()
	if st.Phase != store.PhaseNeedsLogin {
		t.Fatalf("phase %s (%s)", st.Phase, st.LastError)
	}
	if f.fake.Refreshes() != 1 {
		t.Errorf("%d refreshes, want 1", f.fake.Refreshes())
	}
	// The session heals: the next cycle syncs and the phase clears.
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.store.State(); st.Phase != store.PhaseIdle && st.Phase != store.PhaseWatching {
		t.Errorf("phase after recovery %s", st.Phase)
	}
}

func TestFailedListIsAnErrorPhase(t *testing.T) {
	f := setup(t)
	f.fake.FailLists(1)
	err := f.d.Cycle(context.Background())
	var he *rodin.HTTPError
	if !errors.As(err, &he) || he.Status != 503 {
		t.Fatalf("err %v, want the HTTP 503", err)
	}
	st, _ := f.store.State()
	if st.Phase != store.PhaseError || !strings.Contains(st.LastError, "503") || !st.LastSync.IsZero() {
		t.Fatalf("state %+v", st)
	}
	// Not synced, so the next cycle lists again (after Serve's backoff).
	if err := f.d.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.store.State(); st.LastSync.IsZero() || st.Phase == store.PhaseError {
		t.Errorf("after recovery %+v", st)
	}
}

func TestBackoff(t *testing.T) {
	// 5 s doubling: 5, 10, 20, 40 s ... 10m40s at 8 failures, then the cap.
	want := []time.Duration{daemon.Tick, daemon.Tick, 2 * daemon.Tick, 4 * daemon.Tick, 8 * daemon.Tick}
	for failures, w := range want {
		if got := daemon.Backoff(failures); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", failures, got, w)
		}
	}
	if got := daemon.Backoff(8); got != 128*daemon.Tick {
		t.Errorf("Backoff(8) = %s, want %s", got, 128*daemon.Tick)
	}
	for _, failures := range []int{9, 10, 50, 1000} {
		if got := daemon.Backoff(failures); got != daemon.SyncInterval {
			t.Errorf("Backoff(%d) = %s, want the cap %s", failures, got, daemon.SyncInterval)
		}
	}
	for failures := 1; failures < 20; failures++ {
		if daemon.Backoff(failures+1) < daemon.Backoff(failures) {
			t.Fatalf("Backoff shrinks at %d", failures)
		}
	}
}

func TestSyncFollowsEveryPage(t *testing.T) {
	f := setup(t)
	f.d.PageSize = 2 // 5 fixture tasks: pages of 2, 2, 1
	st, _ := f.store.State()
	if err := f.d.SyncHistory(context.Background(), st.UserUUID); err != nil {
		t.Fatal(err)
	}
	if n := f.fake.Calls("task/list_Rodin"); n != 3 {
		t.Errorf("%d list calls, want 3", n)
	}
	if tasks, _ := f.store.Tasks(); len(tasks) != 5 {
		t.Fatalf("%d tasks, want 5", len(tasks))
	}
}

func TestShortListDoesNotPrune(t *testing.T) {
	f := setup(t)
	f.d.PageSize = 2
	ctx := context.Background()
	if err := f.d.SyncHistory(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// The account drops a task, but the list reports one more than it
	// returned (a task hidden or added while paging): the list is not
	// complete, so nothing is removed.
	f.fake.RemoveTask(fakehyper.GeometryID)
	f.fake.SetExtraTotal(1)
	if err := f.d.SyncHistory(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := f.store.Tasks(); len(tasks) != 5 {
		t.Fatalf("an incomplete list pruned: %d tasks", len(tasks))
	}
	// A complete list removes it.
	f.fake.SetExtraTotal(0)
	if err := f.d.SyncHistory(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := f.store.Tasks(); len(tasks) != 4 {
		t.Fatalf("%d tasks after the complete list, want 4", len(tasks))
	}
	if _, err := f.store.Task(fakehyper.GeometryID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removed task still indexed: %v", err)
	}
}

func TestSignedOutEndsTheDaemon(t *testing.T) {
	f := setup(t)
	if err := f.creds.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Cycle(context.Background()); !errors.Is(err, daemon.ErrSignedOut) {
		t.Fatalf("err %v", err)
	}
	if err := f.d.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
}
