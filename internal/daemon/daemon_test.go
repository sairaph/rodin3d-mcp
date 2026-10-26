package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

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
