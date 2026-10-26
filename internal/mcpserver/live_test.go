package mcpserver

// Live, read-only checks against Hyper3D with the session stored in the real
// data root (~/.rodin3d-mcp). They run only with RODIN_LIVE=1, after
// `rodin3d-mcp login`, never submit, confirm, fork, unlock, pack or spend,
// and write files only under the test's temporary folder (previews and
// downloads) and the data root's own cache and index.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

func liveFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("RODIN_LIVE") != "1" {
		t.Skip("set RODIN_LIVE=1 to run the live checks")
	}
	paths := domain.DefaultPaths()
	svc := service.New(service.Options{Paths: paths, Version: "live-test"})
	t.Cleanup(svc.Close)
	c, err := svc.Credentials(context.Background())
	if err != nil || c.State() != auth.StateSignedIn {
		t.Fatalf("not signed in (%v); run rodin3d-mcp login first", err)
	}
	srv := New(Config{Version: "live-test", Service: svc})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.MCPServer().Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "live", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &fixture{t: t, svc: svc, paths: paths, cs: cs}
}

// liveCall is call without the fake's signature check, plus a check that no
// signed address of the real service leaks.
func (f *fixture) liveCall(name string, args map[string]any) toolReply {
	f.t.Helper()
	res, err := f.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	r := toolReply{res: res}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			r.text += tc.Text
		}
	}
	for _, leak := range []string{"file.hyper3d.com", "X-Amz-", "Signature=", "Expires=", "access_token", "eyJ"} {
		if strings.Contains(r.text, leak) {
			f.t.Fatalf("%s: reply contains %q", name, leak)
		}
	}
	parts := strings.SplitN(r.text, "---\n", 3)
	if len(parts) == 3 {
		_ = yaml.Unmarshal([]byte(parts[1]), &r.front)
	}
	if r.res.IsError {
		f.t.Logf("%s error: %s", name, firstLine(r.text, 400))
	}
	return r
}

func firstLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > n {
		return s[:n]
	}
	return s
}

func TestLiveReadOnly(t *testing.T) {
	f := liveFixture(t)
	ctx := context.Background()

	r := f.liveCall("get_account", nil)
	if r.res.IsError {
		t.Fatal("get_account failed")
	}
	t.Logf("account: plan %v, wallet %v, login %v", r.front["plan"], r.front["wallet"], get(r.front, "login", "state"))

	// History sync of the owner's tasks, as the daemon runs it.
	st, err := f.svc.Store()
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := f.svc.Credentials(ctx)
	d := &daemon.Daemon{Store: st, Client: f.svc.Client(), Session: f.svc.Session(), Cache: assets.NewCache(f.paths.Cache, st, f.svc.Client())}
	if _, err := st.ClaimAccount(creds.UserUUID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := d.SyncHistory(ctx, creds.UserUUID); err != nil {
		t.Fatal(err)
	}
	tasks, _ := st.Tasks()
	t.Logf("history sync: %d tasks in %s", len(tasks), time.Since(start).Round(time.Millisecond))
	if len(tasks) == 0 {
		t.Fatal("no tasks synced")
	}

	r = f.liveCall("list_tasks", nil)
	if r.res.IsError {
		t.Fatal("list_tasks failed")
	}
	t.Logf("list_tasks: total %v, pages %v", r.front["total"], r.front["pages"])

	// One task per stage and state seen.
	seen := map[string]string{}
	var packedID string
	for _, task := range tasks {
		s := stage.Of(task.Step, task.UserState, task.PendingJobs)
		key := s.Stage + "/" + s.State
		if _, ok := seen[key]; !ok {
			seen[key] = task.UUID
		}
		if s.State == stage.Packed && packedID == "" {
			packedID = task.UUID
		}
	}
	tmp := t.TempDir()
	for key, id := range seen {
		r := f.liveCall("get_task", map[string]any{"task_id": id})
		if r.res.IsError {
			t.Errorf("get_task %s (%s) failed", id[:8], key)
			continue
		}
		previews, _ := r.front["previews"].([]any)
		t.Logf("get_task %s %s: actions %v, previews %v, images %d", id[:8], key, get(r.front, "task", "actions"), previews, len(r.images()))
		for _, p := range previews {
			pm := p.(map[string]any)
			for _, m := range pm["modes"].([]any) {
				out := filepath.Join(tmp, id[:8]+"-"+pm["stage"].(string)+"-"+m.(string)+".png")
				pr := f.liveCall("get_preview", map[string]any{"task_id": id, "stage": pm["stage"], "mode": m, "size": 256, "save_to": out})
				if pr.res.IsError || len(pr.images()) != 1 {
					t.Errorf("get_preview %s %v/%v failed", id[:8], pm["stage"], m)
					continue
				}
				if _, err := os.Stat(out); err != nil {
					t.Error(err)
				}
			}
		}
	}
	t.Logf("stages seen: %v", keysOfMap(seen))

	if packedID != "" {
		dir := filepath.Join(tmp, "download")
		r := f.liveCall("download_model", map[string]any{"task_id": packedID, "output_dir": dir})
		if r.res.IsError {
			t.Errorf("download_model %s failed", packedID[:8])
		} else {
			t.Logf("download_model %s: %v", packedID[:8], r.front["files"])
		}
	}

	r = f.liveCall("quote_cost", map[string]any{"operation": "confirm_geometry"})
	t.Logf("quote confirm_geometry: %v", r.front["quote"])
	r = f.liveCall("get_generation_options", nil)
	recipes, _ := r.front["recipes"].([]any)
	t.Logf("recipes: %d; first %v", len(recipes), first(recipes))
}

// TestLiveRefresh forces one refresh of the stored session and checks the
// new token works.
func TestLiveRefresh(t *testing.T) {
	f := liveFixture(t)
	ctx := context.Background()
	before, _ := f.svc.Credentials(ctx)
	refreshed, err := f.svc.Session().RefreshIfDue(ctx, 2*time.Hour)
	if err != nil || !refreshed {
		t.Fatalf("refresh: %v %v", refreshed, err)
	}
	after, _ := f.svc.Credentials(ctx)
	if after.AccessToken == before.AccessToken || after.RefreshToken == before.RefreshToken || !after.AccessExpiresAt.After(before.AccessExpiresAt) {
		t.Fatalf("tokens not rotated (access changed %v, refresh changed %v)", after.AccessToken != before.AccessToken, after.RefreshToken != before.RefreshToken)
	}
	if _, err := f.svc.Client().GetInfo(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("refreshed; access valid until %s", after.AccessExpiresAt.Format(time.RFC3339))
}

func keysOfMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func first(xs []any) any {
	if len(xs) == 0 {
		return nil
	}
	return xs[0]
}
