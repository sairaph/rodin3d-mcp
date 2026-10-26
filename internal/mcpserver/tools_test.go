package mcpserver

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/service"
)

type fixture struct {
	t     *testing.T
	fake  *fakehyper.Server
	svc   *service.Service
	paths domain.Paths
	cs    *mcp.ClientSession
}

func newFixture(t *testing.T, signIn, synced bool) *fixture {
	t.Helper()
	service.WaitPoll = 50 * time.Millisecond
	fake := fakehyper.Standard()
	t.Cleanup(fake.Close)
	paths := domain.PathsUnder(t.TempDir())
	svc := service.New(service.Options{Paths: paths, BaseURL: fake.Base(), Version: "test"})
	t.Cleanup(svc.Close)
	f := &fixture{t: t, fake: fake, svc: svc, paths: paths}
	if signIn {
		f.signIn()
	}
	if synced {
		f.sync()
	}
	srv := New(Config{Version: "test", Service: svc})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.MCPServer().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	f.cs = cs
	return f
}

func (f *fixture) signIn() {
	ctx := context.Background()
	_, started, err := f.svc.Login(ctx, service.LoginInput{})
	if err != nil {
		f.t.Fatal(err)
	}
	redirect, err := f.fake.Approve(started.AuthorizeURL)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.CompleteLogin(ctx, redirect); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sync() {
	st, err := f.svc.Store()
	if err != nil {
		f.t.Fatal(err)
	}
	d := &daemon.Daemon{Store: st, Client: f.svc.Client(), Session: f.svc.Session(), Cache: assets.NewCache(f.paths.Cache, st, f.svc.Client())}
	if err := d.Cycle(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

type toolReply struct {
	res   *mcp.CallToolResult
	text  string
	front map[string]any
}

func (f *fixture) call(name string, args map[string]any) toolReply {
	f.t.Helper()
	res, err := f.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		f.t.Fatalf("%s: protocol error %v", name, err)
	}
	r := toolReply{res: res}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			r.text += tc.Text
		}
	}
	if strings.Contains(r.text, fakehyper.SignatureMark) {
		f.t.Fatalf("%s: reply carries a signed address:\n%s", name, r.text)
	}
	parts := strings.SplitN(r.text, "---\n", 3)
	if len(parts) == 3 {
		_ = yaml.Unmarshal([]byte(parts[1]), &r.front)
	}
	return r
}

func (r toolReply) code() string {
	if e, ok := r.front["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func (r toolReply) images() [][]byte {
	var out [][]byte
	for _, c := range r.res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			out = append(out, ic.Data)
		}
	}
	return out
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}

func TestToolList(t *testing.T) {
	f := newFixture(t, false, false)
	res, err := f.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if len(tool.Description) < 120 {
			t.Errorf("%s: description shorter than 120 characters", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" {
			t.Errorf("%s: no annotations", tool.Name)
		}
	}
	slices.Sort(names)
	want := slices.Clone(service.Tools)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
}

func TestSignedOutAnswersAuthentication(t *testing.T) {
	f := newFixture(t, false, false)
	for _, name := range []string{"get_account", "list_tasks", "quote_cost"} {
		args := map[string]any{}
		if name == "quote_cost" {
			args["operation"] = "confirm_geometry"
		}
		r := f.call(name, args)
		if !r.res.IsError || r.code() != "authentication" || !strings.Contains(r.text, "login") {
			t.Errorf("%s: %s", name, r.text)
		}
	}
	// The catalogue works without a session.
	if r := f.call("get_generation_options", nil); r.res.IsError {
		t.Errorf("options: %s", r.text)
	}
}

func TestLoginToolStartsAndFinishes(t *testing.T) {
	f := newFixture(t, false, false)
	r := f.call("login", map[string]any{"open_browser": false})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	authURL, _ := r.front["authorize_url"].(string)
	if authURL == "" || len(r.images()) != 1 || r.front["browser_opened"] != false {
		t.Fatalf("start: %s", r.text)
	}
	redirect, err := f.fake.Approve(authURL)
	if err != nil {
		t.Fatal(err)
	}
	r = f.call("login", map[string]any{"redirect_url": "http://127.0.0.1:53682/callback?code=x&state=wrong"})
	if r.code() != "invalid_input" {
		t.Errorf("wrong state: %s", r.text)
	}
	r = f.call("login", map[string]any{"redirect_url": redirect})
	if r.res.IsError || get(r.front, "user", "username") != "owner" {
		t.Fatalf("finish: %s", r.text)
	}
	if strings.Contains(r.text, "at.") {
		t.Fatal("the access token is in the reply")
	}
	r = f.call("logout", nil)
	if r.res.IsError || r.front["revoked"] != true || !f.fake.GrantRevoked() {
		t.Fatalf("logout: %s", r.text)
	}
	if r := f.call("get_account", nil); r.code() != "authentication" {
		t.Errorf("after logout: %s", r.text)
	}
}

func TestAccount(t *testing.T) {
	f := newFixture(t, true, false)
	if err := os.WriteFile(f.paths.Config, []byte("[permission]\nexport = \"allow\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.call("get_account", nil)
	if r.res.IsError {
		t.Fatal(r.text)
	}
	if num(get(r.front, "wallet", "available")) != 53.5 || num(get(r.front, "wallet", "regular")) != 25 || get(r.front, "plan") != "Creator" ||
		get(r.front, "permissions", "export") != "allow" || get(r.front, "permissions", "generate") != "deny" || get(r.front, "login", "state") != "signed_in" {
		t.Fatalf("account: %s", r.text)
	}
}

func TestListTasks(t *testing.T) {
	f := newFixture(t, true, true)
	r := f.call("list_tasks", nil)
	tasks, _ := r.front["tasks"].([]any)
	if r.res.IsError || len(tasks) != 5 || r.front["total"] != 5 {
		t.Fatalf("list: %s", r.text)
	}
	first := tasks[0].(map[string]any)
	if first["id"] != fakehyper.RunningID || first["state"] != "running" {
		t.Errorf("first row %v", first)
	}
	r = f.call("list_tasks", map[string]any{"state": "packed"})
	if tasks, _ := r.front["tasks"].([]any); len(tasks) != 2 {
		t.Errorf("packed filter: %s", r.text)
	}
	r = f.call("list_tasks", map[string]any{"query": "owl"})
	if tasks, _ := r.front["tasks"].([]any); len(tasks) != 1 {
		t.Errorf("query: %s", r.text)
	}
	if r := f.call("list_tasks", map[string]any{"state": "sleeping"}); r.code() != "invalid_input" {
		t.Errorf("bad enum: %s", r.text)
	}
	if r := f.call("list_tasks", map[string]any{"colour": "red"}); r.code() != "invalid_input" {
		t.Errorf("unknown argument: %s", r.text)
	}
}

func TestGetTask(t *testing.T) {
	f := newFixture(t, true, true)
	r := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID[:8]})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	if get(r.front, "task", "stage") != "material" || get(r.front, "task", "state") != "packed" ||
		!slices.Contains(get(r.front, "task", "actions").([]any), any("download_model")) {
		t.Errorf("task: %s", r.text)
	}
	inputs, _ := r.front["inputs"].([]any)
	if len(inputs) != 2 || inputs[0].(map[string]any)["view"] != "F" || inputs[1].(map[string]any)["view"] != "B" || inputs[1].(map[string]any)["weight"] != 0.5 {
		t.Errorf("inputs %v", inputs)
	}
	if get(r.front, "settings", "confirm", "quality_override") != 500000 || get(r.front, "settings", "material", "escore") != 7 ||
		get(r.front, "settings", "pack", "geometry_file_format") != "obj" || get(r.front, "settings", "geometry", "voxel_condition") != "(500 characters)" {
		t.Errorf("settings: %s", r.text)
	}
	if pack, _ := get(r.front, "files", "pack").([]any); len(pack) != 3 {
		t.Errorf("pack %v", pack)
	}
	if len(r.images()) != 1 {
		t.Error("no poster attached")
	}
	// The draft shows the redo counter: Creator 20, versions v1 v2 -> 19.
	r = f.call("get_task", map[string]any{"task_id": fakehyper.DraftID})
	if get(r.front, "task", "redo_left", "geometry") != 19 {
		t.Errorf("redo left: %s", r.text)
	}
	if r := f.call("get_task", map[string]any{"task_id": "99999999"}); r.code() != "not_found" {
		t.Errorf("unknown id: %s", r.text)
	}
}

func TestWaitTask(t *testing.T) {
	f := newFixture(t, true, false)
	r := f.call("wait_task", map[string]any{"task_id": fakehyper.RunningID, "timeout_seconds": 5})
	if r.res.IsError || r.front["changed"] != true || r.front["timed_out"] != false || get(r.front, "task", "state") != "ready" {
		t.Fatalf("wait: %s", r.text)
	}
	if len(r.images()) != 1 {
		t.Error("no poster after the change")
	}
	r = f.call("wait_task", map[string]any{"task_id": fakehyper.PackedID, "timeout_seconds": 5})
	if r.front["changed"] != false || r.front["timed_out"] != false {
		t.Errorf("not running: %s", r.text)
	}
	if r := f.call("wait_task", map[string]any{"task_id": fakehyper.PackedID, "timeout_seconds": 90}); r.code() != "invalid_input" {
		t.Errorf("timeout range: %s", r.text)
	}
}

func TestPreview(t *testing.T) {
	f := newFixture(t, true, false)
	cases := []struct {
		id, stage, mode string
	}{
		{fakehyper.DraftID, "current", "poster"},
		{fakehyper.PackedID, "geometry_draft", "poster"},
		{fakehyper.PackedID, "geometry", "geometry"},
		{fakehyper.PackedID, "material", "material"},
		{fakehyper.PackedID, "material", "diffuse"},
		{fakehyper.MatDraftID, "material", "normal"},
		{fakehyper.GeometryID, "current", "geometry"},
	}
	for _, c := range cases {
		r := f.call("get_preview", map[string]any{"task_id": c.id, "stage": c.stage, "mode": c.mode, "size": 256})
		if r.res.IsError {
			t.Errorf("%s %s/%s: %s", c.id[:8], c.stage, c.mode, r.text)
			continue
		}
		imgs := r.images()
		if len(imgs) != 1 {
			t.Errorf("%s %s/%s: %d images", c.id[:8], c.stage, c.mode, len(imgs))
			continue
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(imgs[0]))
		if err != nil || max(cfg.Width, cfg.Height) > 256 {
			t.Errorf("%s: image %v %+v", c.mode, err, cfg)
		}
		file, _ := get(r.front, "preview", "file").(string)
		if _, err := os.Stat(file); err != nil {
			t.Errorf("preview file: %v", err)
		}
	}
	r := f.call("get_preview", map[string]any{"task_id": fakehyper.DraftID, "stage": "material", "mode": "poster"})
	if r.code() != "conflict" || !strings.Contains(r.text, "geometry_draft: poster") {
		t.Errorf("unavailable stage: %s", r.text)
	}
	out := filepath.Join(t.TempDir(), "p.png")
	if r := f.call("get_preview", map[string]any{"task_id": fakehyper.PackedID, "save_to": out}); r.res.IsError {
		t.Errorf("save_to: %s", r.text)
	} else if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
	if r := f.call("get_preview", map[string]any{"task_id": fakehyper.PackedID, "mode": "depth"}); r.code() != "invalid_input" {
		t.Errorf("render mode: %s", r.text)
	}
	if f.fake.BearerOnFile {
		t.Fatal("a file request carried the bearer token")
	}
}

func TestDownload(t *testing.T) {
	f := newFixture(t, true, false)
	r := f.call("download_model", map[string]any{"task_id": fakehyper.PackedID})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	files, _ := r.front["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("files %s", r.text)
	}
	dir, _ := r.front["dir"].(string)
	if !strings.HasPrefix(filepath.Base(dir), fakehyper.PackedID[:8]+"-a-human-figure") {
		t.Errorf("dir %s", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "base_basic_pbr.obj"))
	if err != nil || !strings.HasPrefix(string(data), "o pbr") {
		t.Errorf("file: %v %q", err, data)
	}
	out := t.TempDir()
	r = f.call("download_model", map[string]any{"task_id": fakehyper.PackedID, "output_dir": out, "files": []string{"material_video"}})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	if _, err := os.Stat(filepath.Join(out, "material_video.mp4")); err != nil {
		t.Error(err)
	}
	if r := f.call("download_model", map[string]any{"task_id": fakehyper.DraftID}); r.code() != "conflict" {
		t.Errorf("draft: %s", r.text)
	}
	if r := f.call("download_model", map[string]any{"task_id": fakehyper.PackedID, "files": []string{"nope.obj"}}); r.code() != "invalid_input" {
		t.Errorf("unknown file: %s", r.text)
	}
	// get_task lists the downloaded files.
	r = f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
	if dl, _ := get(r.front, "files", "downloaded").([]any); len(dl) != 4 {
		t.Errorf("downloaded: %v", dl)
	}
	if f.fake.BearerOnFile {
		t.Fatal("a file request carried the bearer token")
	}
}

func TestQuote(t *testing.T) {
	f := newFixture(t, true, false)
	r := f.call("quote_cost", map[string]any{"operation": "confirm_geometry", "task_id": fakehyper.DraftID})
	if r.res.IsError || get(r.front, "quote", "credits") != 0.5 || r.front["affordable"] != true || get(r.front, "permission", "value") != "deny" {
		t.Fatalf("confirm: %s", r.text)
	}
	r = f.call("quote_cost", map[string]any{"operation": "generate_model", "settings": map[string]any{"tier": "medium"}})
	if get(r.front, "quote", "credits") != 0 || get(r.front, "quote", "free_reason") == nil {
		t.Errorf("free preview: %s", r.text)
	}
	r = f.call("quote_cost", map[string]any{"operation": "rig_model"})
	if r.res.IsError || get(r.front, "quote", "known") != false {
		t.Errorf("unknown: %s", r.text)
	}
	if r := f.call("quote_cost", map[string]any{"operation": "export_model", "settings": map[string]any{"resolution": "4k"}}); r.code() != "forbidden" {
		t.Errorf("gate: %s", r.text)
	}
	if r := f.call("quote_cost", map[string]any{"operation": "make_tea"}); r.code() != "invalid_input" {
		t.Errorf("operation enum: %s", r.text)
	}
}

func TestOptionsRecipes(t *testing.T) {
	f := newFixture(t, true, true)
	r := f.call("get_generation_options", map[string]any{"model": "gen-2.5"})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	recipes, _ := r.front["recipes"].([]any)
	if len(recipes) == 0 {
		t.Fatalf("no recipes: %s", r.text)
	}
	found := false
	for _, rc := range recipes {
		m := rc.(map[string]any)
		if m["tier"] == "high" && m["quality_override"] == "500000" && m["format"] == "obj" {
			found = true
		}
	}
	if !found {
		t.Errorf("owner recipe missing: %v", recipes)
	}
	privs, _ := r.front["privileges"].([]any)
	if len(privs) == 0 || privs[0].(map[string]any)["has"] == nil {
		t.Errorf("privileges without has: %v", privs)
	}
	if models, _ := r.front["models"].([]any); len(models) != 1 {
		t.Errorf("model filter: %v", models)
	}
}

// M2: a pasted address that is cut short keeps the pending sign-in, and the
// hint asks for the whole address instead of starting over.
func TestLoginWithATruncatedAddress(t *testing.T) {
	f := newFixture(t, false, false)
	r := f.call("login", map[string]any{"open_browser": false})
	authURL, _ := r.front["authorize_url"].(string)
	redirect, err := f.fake.Approve(authURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []string{redirect[:strings.Index(redirect, "?")], redirect[:strings.Index(redirect, "&")]} {
		r = f.call("login", map[string]any{"redirect_url": cut})
		hint, _ := get(r.front, "error", "hint").(string)
		if r.code() != "invalid_input" || !strings.Contains(hint, "still pending") || !strings.Contains(hint, "redirect_url") {
			t.Fatalf("truncated %q: %s", cut, r.text)
		}
	}
	r = f.call("login", map[string]any{"redirect_url": redirect})
	if r.res.IsError || get(r.front, "user", "username") != "owner" {
		t.Fatalf("the whole address after a bad paste: %s", r.text)
	}
}

// M4: 8k is not a pack resolution (it is the sharp_texture chip).
func TestQuoteRefusesTheEightKPack(t *testing.T) {
	f := newFixture(t, true, false)
	if r := f.call("quote_cost", map[string]any{"operation": "export_model", "settings": map[string]any{"resolution": "8k"}}); r.code() != "invalid_input" {
		t.Fatalf("8k: %s", r.text)
	}
	if r := f.call("quote_cost", map[string]any{"operation": "export_model", "settings": map[string]any{"resolution": "2k", "tier": "low"}}); r.res.IsError || get(r.front, "quote", "credits") != 0 {
		t.Fatalf("2k low: %s", r.text)
	}
}

// L11: frozen credits count as spent when deciding affordable.
func TestQuoteAffordableCountsFrozenCredits(t *testing.T) {
	f := newFixture(t, true, false)
	f.fake.SetUser(func(u *fakehyper.User) { u.Frozen = 533 }) // 53.5 available, 53.3 frozen
	r := f.call("quote_cost", map[string]any{"operation": "confirm_geometry"})
	if r.res.IsError || r.front["affordable"] != false || num(get(r.front, "wallet", "frozen")) != 53.3 {
		t.Fatalf("frozen: %s", r.text)
	}
	f.fake.SetUser(func(u *fakehyper.User) { u.Frozen = 0 })
	if r := f.call("quote_cost", map[string]any{"operation": "confirm_geometry"}); r.front["affordable"] != true {
		t.Fatalf("unfrozen: %s", r.text)
	}
	// An unknown price has no affordable at all.
	if r := f.call("quote_cost", map[string]any{"operation": "rig_model"}); r.front["affordable"] != nil {
		t.Fatalf("unknown price: %s", r.text)
	}
}

// L3: the material redo counter and the material previews use the same
// reference, also when the card's reference_image setting is empty.
func TestMaterialReferenceFallback(t *testing.T) {
	f := newFixture(t, true, false)
	// Creator: 6 material redos; versions v1, v2 of reference 0000.webp -> 5.
	r := f.call("get_task", map[string]any{"task_id": fakehyper.MatDraftID})
	if get(r.front, "task", "redo_left", "material") != 5 {
		t.Fatalf("with the setting: %s", r.text)
	}
	delete(f.fake.Tasks(fakehyper.MatDraftID).Settings, "reference_image")
	r = f.call("get_task", map[string]any{"task_id": fakehyper.MatDraftID})
	if get(r.front, "task", "redo_left", "material") != 5 {
		t.Fatalf("without the setting: %s", r.text)
	}
	// The history's only reference also gives the material images.
	if r := f.call("get_preview", map[string]any{"task_id": fakehyper.MatDraftID, "stage": "material", "mode": "diffuse", "size": 256}); r.res.IsError {
		t.Fatalf("material preview without the setting: %s", r.text)
	}
}

// L4: an address nested in a setting (a list of maps) is hidden like a
// top-level one. call() fails on any signature in the reply.
func TestGetTaskHidesNestedAddresses(t *testing.T) {
	f := newFixture(t, true, false)
	f.fake.Tasks(fakehyper.PackedID).Settings["texture_partial_reference_images"] = []any{
		map[string]any{"image": f.fake.HTTP.URL + "/files/x.webp?sig=" + fakehyper.SignatureMark + "nested", "weight": 0.5},
	}
	r := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
	if r.res.IsError {
		t.Fatal(r.text)
	}
	list, _ := get(r.front, "settings", "other", "texture_partial_reference_images").([]any)
	if len(list) != 1 || list[0].(map[string]any)["image"] != "(file address)" || list[0].(map[string]any)["weight"] != 0.5 {
		t.Fatalf("nested setting: %v", list)
	}
}

// L9 and R4: why a download is refused, and file host failures.
func TestDownloadRefusalsAndFileHostFailures(t *testing.T) {
	f := newFixture(t, true, false)
	// Packed, but Hyper3D lists no file (seen on a material draft marked
	// DoneBasic): no download_model action, and a clear conflict.
	f.fake.Tasks(fakehyper.MatDraftID).UserState = "DoneBasic"
	f.fake.Tasks(fakehyper.MatDraftID).Pack = []string{}
	r := f.call("get_task", map[string]any{"task_id": fakehyper.MatDraftID})
	if get(r.front, "task", "state") != "packed" || slices.Contains(get(r.front, "task", "actions").([]any), any("download_model")) {
		t.Fatalf("packed without files: %s", r.text)
	}
	r = f.call("download_model", map[string]any{"task_id": fakehyper.MatDraftID})
	if r.code() != "conflict" || !strings.Contains(r.text, "lists no files") {
		t.Fatalf("download of an empty pack: %s", r.text)
	}
	r = f.call("download_model", map[string]any{"task_id": fakehyper.RunningID})
	if r.code() != "conflict" || !strings.Contains(r.text, "still working") || !strings.Contains(r.text, "wait_task") {
		t.Fatalf("download of a running task: %s", r.text)
	}
	r = f.call("download_model", map[string]any{"task_id": fakehyper.DraftID})
	if r.code() != "conflict" || !strings.Contains(r.text, "geometry draft") {
		t.Fatalf("download of a draft: %s", r.text)
	}

	// An expired signature (the file host answers 403) is unavailable with
	// a fresh-address hint, not a plan or id problem.
	f.fake.SetFileStatus(403)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"download_model", map[string]any{"task_id": fakehyper.PackedID, "output_dir": t.TempDir()}},
		{"get_preview", map[string]any{"task_id": fakehyper.PackedID, "stage": "material", "mode": "pbr"}},
	} {
		r := f.call(c.tool, c.args)
		if r.code() != "unavailable" || !strings.Contains(r.text, "fresh addresses") {
			t.Errorf("%s with a 403 file host: %s", c.tool, r.text)
		}
	}
}

// L12: after a login as another account, the index of the previous one is
// not shown until the daemon has synced the new one.
func TestListTasksOfAnotherAccountWaitsForItsSync(t *testing.T) {
	f := newFixture(t, true, true)
	if r := f.call("list_tasks", nil); r.front["total"] != 5 {
		t.Fatalf("first account: %s", r.text)
	}
	f.call("logout", nil)
	f.fake.SetUser(func(u *fakehyper.User) { u.UUID, u.Username = "u-1111-1111", "other" })
	f.signIn()
	r := f.call("list_tasks", nil)
	if r.res.IsError || r.front["total"] != 0 || !strings.Contains(r.text, "first history sync has not finished") {
		t.Fatalf("other account before its sync: %s", r.text)
	}
	f.sync()
	if r := f.call("list_tasks", nil); r.front["total"] != 5 {
		t.Fatalf("other account after its sync: %s", r.text)
	}
}

func TestExpiredTokenIsRefreshedForTheCall(t *testing.T) {
	f := newFixture(t, true, false)
	f.fake.ExpireAccess()
	if r := f.call("get_account", nil); r.res.IsError {
		t.Fatalf("after server-side expiry: %s", r.text)
	}
	if f.fake.Refreshes() != 1 {
		t.Errorf("%d refreshes", f.fake.Refreshes())
	}
}

func num(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	return -1
}
