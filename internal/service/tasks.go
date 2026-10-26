package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/budget"
	"github.com/sairaph/mcp-wizard/render"
	"gopkg.in/yaml.v3"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// resolveTask turns a full id or a stored id prefix into a task uuid.
func (s *Service) resolveTask(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fail(render.CodeInvalidInput, "task_id is empty.", hintListTasks)
	}
	if uuidRE.MatchString(id) {
		return strings.ToLower(id), nil
	}
	st, err := s.storeOrFail()
	if err != nil {
		return "", err
	}
	return st.ResolveID(id)
}

// TaskFront is the task: front matter of every result that touches a task.
type TaskFront struct {
	Prompt    string    `yaml:"prompt"`
	ID        string    `yaml:"id"`
	Stage     string    `yaml:"stage"`
	State     string    `yaml:"state"`
	Step      string    `yaml:"step"`
	UserState string    `yaml:"user_state"`
	Mode      string    `yaml:"mode,omitempty"`
	Tier      string    `yaml:"tier,omitempty"`
	Views     []string  `yaml:"views,omitempty,flow"`
	Private   bool      `yaml:"private"`
	Created   string    `yaml:"created,omitempty"`
	Updated   string    `yaml:"updated,omitempty"`
	Actions   []string  `yaml:"actions,flow"`
	RedoLeft  *RedoLeft `yaml:"redo_left,omitempty"`
	FilesDir  string    `yaml:"files_dir"`
}

// RedoLeft counts the free redos left at a draft stage.
type RedoLeft struct {
	Geometry *int `yaml:"geometry,omitempty"`
	Material *int `yaml:"material,omitempty"`
}

func (s *Service) taskFront(card *rodin.Card) TaskFront {
	sum := daemon.Summarize(card)
	st := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
	var actions []string
	for _, a := range stage.Actions(st) {
		if registered(a) {
			actions = append(actions, a)
		}
	}
	if actions == nil {
		actions = []string{}
	}
	return TaskFront{
		Prompt: card.Prompt, ID: card.TaskUUID, Stage: st.Stage, State: st.State, Step: st.Step, UserState: st.UserState,
		Mode: sum.Mode, Tier: sum.Tier, Views: sum.Views, Private: card.IsPrivate,
		Created: card.Time, Updated: card.GenerateTime, Actions: actions,
		FilesDir: s.downloadDir(card.TaskUUID, card.Prompt),
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// downloadDir is downloads/<id8>-<slug> (plan D10).
func (s *Service) downloadDir(uuid, prompt string) string {
	slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(prompt), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	name := short(uuid)
	if slug != "" {
		name += "-" + slug
	}
	return filepath.Join(s.paths.Downloads, name)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// --- list_tasks ---

// ListInput filters list_tasks.
type ListInput struct {
	Stage string
	State string
	Query string
	Page  int
}

type listRow struct {
	Prompt  string `yaml:"prompt"`
	ID      string `yaml:"id"`
	Stage   string `yaml:"stage"`
	State   string `yaml:"state"`
	Tier    string `yaml:"tier,omitempty"`
	Views   string `yaml:"views,omitempty"`
	Created string `yaml:"created,omitempty"`
}

type syncFront struct {
	Running  bool   `yaml:"daemon_running"`
	Phase    string `yaml:"phase"`
	LastSync string `yaml:"last_sync,omitempty"`
	Error    string `yaml:"last_error,omitempty"`
}

type listFront struct {
	Tasks []listRow `yaml:"tasks"`
	Page  int       `yaml:"page"`
	Pages int       `yaml:"pages"`
	Total int       `yaml:"total"`
	Sync  syncFront `yaml:"sync"`
}

// listTokenBudget bounds one page of list_tasks.
const listTokenBudget = 6000

// firstSyncWait bounds how long list_tasks waits for the first history sync.
const firstSyncWait = 25 * time.Second // stays inside a 30 s client tool timeout

// ListTasks is list_tasks: the task index from the store, filtered, newest
// first, paginated by token budget.
func (s *Service) ListTasks(ctx context.Context, in ListInput) (Result, error) {
	st, err := s.storeOrFail()
	if err != nil {
		return Result{}, err
	}
	state, err := st.State()
	if err != nil {
		return Result{}, err
	}
	// The index is shown only to the account it belongs to: signed out, or
	// signed in as someone the daemon has not synced yet, it is not current.
	creds, err := s.signedIn(ctx)
	if err != nil {
		return Result{}, err
	}
	s.EnsureDaemon(ctx)
	if !syncedFor(state, creds.UserUUID) {
		state = s.waitFirstSync(ctx, st, state, creds.UserUUID)
	}
	var tasks []store.Task
	if syncedFor(state, creds.UserUUID) {
		if tasks, err = st.Tasks(); err != nil {
			return Result{}, err
		}
	} else {
		state.LastSync = time.Time{} // reported as "first sync not finished"
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	var rows []listRow
	for _, t := range tasks {
		ts := stage.Of(t.Step, t.UserState, t.PendingJobs)
		if in.Stage != "" && ts.Stage != in.Stage {
			continue
		}
		if in.State != "" && ts.State != in.State {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Prompt), q) && !strings.HasPrefix(t.UUID, q) {
			continue
		}
		rows = append(rows, listRow{
			Prompt: clip(t.Prompt, 120), ID: t.UUID, Stage: ts.Stage, State: ts.State,
			Tier: strings.TrimSpace(t.Mode + " " + t.Tier), Views: strings.Join(t.Views, ","), Created: t.Created,
		})
	}
	page := max(in.Page, 1)
	window, pages, err := budget.Paginate(rows, page, listTokenBudget, func(r []listRow) (string, error) {
		out, err := yaml.Marshal(r)
		return string(out), err
	})
	if err != nil {
		return Result{}, err
	}
	front := listFront{Tasks: window, Page: page, Pages: pages, Total: len(rows), Sync: syncFront{Running: s.DaemonRunning(), Phase: state.Phase, Error: state.LastError}}
	if front.Tasks == nil {
		front.Tasks = []listRow{}
	}
	if !state.LastSync.IsZero() {
		front.Sync.LastSync = state.LastSync.UTC().Format(time.RFC3339)
	}

	var b strings.Builder
	switch {
	case len(rows) == 0 && len(tasks) == 0 && state.LastSync.IsZero():
		b.WriteString("The first history sync has not finished yet (phase " + orUnknown(state.Phase) + "). Call list_tasks again in a few seconds.")
	case len(rows) == 0 && len(tasks) == 0:
		b.WriteString("The account has no Rodin tasks yet.")
	case len(rows) == 0:
		fmt.Fprintf(&b, "No task matches the filters (%d tasks in total).", len(tasks))
	case page > pages:
		fmt.Fprintf(&b, "Page %d is past the last page (%d).", page, pages)
	default:
		fmt.Fprintf(&b, "%d of %d tasks match; page %d of %d, newest first.", len(rows), len(tasks), page, pages)
		if page < pages {
			fmt.Fprintf(&b, " Next page: list_tasks with page %d.", page+1)
		}
	}
	b.WriteString("\n\nNext: get_task with an id for its settings, inputs and actions; get_preview for its images.")
	return Result{Front: front, Body: b.String()}, nil
}

// waitFirstSync waits (bounded) for the running daemon's first history
// sync, so the first list after a login is not empty.
func (s *Service) waitFirstSync(ctx context.Context, st *store.Store, state store.SyncState, user string) store.SyncState {
	if !s.DaemonRunning() {
		return state
	}
	deadline := time.Now().Add(firstSyncWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return state
		case <-time.After(500 * time.Millisecond):
		}
		next, err := st.State()
		if err != nil {
			return state
		}
		state = next
		if syncedFor(state, user) || state.Phase == store.PhaseError || state.Phase == store.PhaseNeedsLogin {
			return state
		}
	}
	return state
}

// syncedFor reports whether the index holds a completed sync of user.
func syncedFor(state store.SyncState, user string) bool {
	return !state.LastSync.IsZero() && state.UserUUID == user
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// --- get_task ---

type settingsFront struct {
	Geometry map[string]any `yaml:"geometry,omitempty"`
	Confirm  map[string]any `yaml:"confirm,omitempty"`
	Material map[string]any `yaml:"material,omitempty"`
	Pack     map[string]any `yaml:"pack,omitempty"`
	Other    map[string]any `yaml:"other,omitempty"`
}

type inputFront struct {
	File   string  `yaml:"file"`
	View   string  `yaml:"view"`
	Weight float64 `yaml:"weight,omitempty"`
}

type geoVersionFront struct {
	Version   string `yaml:"version"`
	Generated string `yaml:"generated,omitempty"`
	Bang      bool   `yaml:"bang,omitempty"`
}

type matVersionFront struct {
	Reference string `yaml:"reference"`
	Version   string `yaml:"version"`
}

type versionsFront struct {
	Geometry []geoVersionFront `yaml:"geometry,omitempty"`
	Material []matVersionFront `yaml:"material,omitempty"`
}

type confirmedFront struct {
	GeometryVersion string `yaml:"geometry_version,omitempty"`
	GeometryAt      string `yaml:"geometry_at,omitempty"`
	MaterialVersion string `yaml:"material_version,omitempty"`
	MaterialAt      string `yaml:"material_at,omitempty"`
}

type previewFront struct {
	Stage string   `yaml:"stage"`
	Modes []string `yaml:"modes,flow"`
}

type fileFront struct {
	Name  string `yaml:"name"`
	Path  string `yaml:"path,omitempty"`
	Bytes int64  `yaml:"bytes,omitempty"`
}

type filesFront struct {
	Pack       []string    `yaml:"pack,omitempty,flow"`
	Videos     []string    `yaml:"videos,omitempty,flow"`
	Downloaded []fileFront `yaml:"downloaded,omitempty"`
}

type taskDetailFront struct {
	Task      TaskFront       `yaml:"task"`
	Settings  settingsFront   `yaml:"settings"`
	Inputs    []inputFront    `yaml:"inputs,omitempty"`
	Versions  versionsFront   `yaml:"versions,omitempty"`
	Confirmed *confirmedFront `yaml:"confirmed,omitempty"`
	Previews  []previewFront  `yaml:"previews"`
	Files     filesFront      `yaml:"files,omitempty"`
	Spend     []spendRow      `yaml:"spend,omitempty"`
}

// spendRow is one ledger row of the task: a paid or changing call this
// tool made on it.
type spendRow struct {
	At     string   `yaml:"at"`
	Op     string   `yaml:"op"`
	Quoted *float64 `yaml:"quoted,omitempty"`
	Before string   `yaml:"wallet_before"`
	After  string   `yaml:"wallet_after,omitempty"`
	Result string   `yaml:"result"`
}

func spendRows(rows []store.LedgerEntry) []spendRow {
	var out []spendRow
	for _, e := range rows {
		r := spendRow{At: e.At.UTC().Format(time.RFC3339), Op: e.Op, Quoted: e.Quoted, Result: e.Result,
			Before: fmt.Sprintf("%s available, %s frozen", num(e.BalanceBefore), num(e.FrozenBefore))}
		if e.BalanceAfter != nil && e.FrozenAfter != nil {
			r.After = fmt.Sprintf("%s available, %s frozen", num(*e.BalanceAfter), num(*e.FrozenAfter))
		}
		out = append(out, r)
	}
	return out
}

var settingGroups = map[string]string{}

func init() {
	for group, keys := range map[string][]string{
		"confirm":  {"quality", "symmetric", "use_hyper", "mesh_export_type", "mesh_smooth", "mesh_simplify", "quality_override", "quad_normal", "model_multi_udim"},
		"material": {"fast_texture", "escore", "reference_scale", "face_enhance", "superres_texture", "sharp_texture", "texture_delight", "texture_mode", "reference_image", "seed", "rodin_smart_uv_version", "image_enhance"},
		"pack":     {"geometry", "geometry_file_format", "material", "resolution", "align_ground"},
		"geometry": {"view_weights", "condition_mode", "image_label", "height", "allow_face_enhance", "default_face_enhance", "generation_mode", "brilliant_mode",
			"image_mode", "version", "type", "voxel_condition", "voxel_condition_cfg", "voxel_condition_weight", "enable_advance_option", "symmetric_bias",
			"highpoly_bias", "lineart_enable", "lineart_weight", "model_partial_reference_version", "model_partial_reference_bbox", "model_reference_image",
			"micro_bias", "geometry_instruct_mode", "detail_level", "sharp_bias", "rest_bias", "guidance_scale", "inference_steps", "negative_prompt",
			"bbox_condition", "pcd_condition", "pcd_condition_uncertainty", "spatial_condition", "batch_gen", "model_reference_rotation"},
	} {
		for _, k := range keys {
			settingGroups[k] = group
		}
	}
}

// groupSettings sorts a card's settings into stage groups, shortening the
// values that are bulk data (voxel grids, point clouds).
func groupSettings(settings map[string]any) settingsFront {
	var out settingsFront
	put := func(m *map[string]any, k string, v any) {
		if *m == nil {
			*m = map[string]any{}
		}
		(*m)[k] = v
	}
	for k, v := range settings {
		v = compactValue(v)
		switch settingGroups[k] {
		case "geometry":
			put(&out.Geometry, k, v)
		case "confirm":
			put(&out.Confirm, k, v)
		case "material":
			put(&out.Material, k, v)
		case "pack":
			put(&out.Pack, k, v)
		default:
			put(&out.Other, k, v)
		}
	}
	return out
}

func compactValue(v any) any {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
			return "(file address)"
		}
		if len(x) > 200 {
			return fmt.Sprintf("(%d characters)", len(x))
		}
	case []any:
		if len(x) > 24 {
			return fmt.Sprintf("(%d items)", len(x))
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = compactValue(e) // nested addresses are hidden too
		}
		return out
	case map[string]any:
		if len(x) > 24 {
			return fmt.Sprintf("(%d entries)", len(x))
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = compactValue(e)
		}
		return out
	}
	return v
}

// taskDetail gathers what get_task shows. Each extra call is best effort:
// the card alone is enough for a reply.
type taskDetail struct {
	card    *rodin.Card
	status  stage.Status
	hist    assets.History
	list    []assets.Asset
	pack    []string
	redo    *RedoLeft
	histErr error
}

func (s *Service) loadDetail(ctx context.Context, uuid string, withHistory bool) (*taskDetail, error) {
	card, err := s.client.Card(ctx, uuid)
	if err != nil {
		return nil, err
	}
	d := &taskDetail{card: card, status: stage.Of(card.Step, card.UserState, int(card.PendingJobs))}
	if withHistory {
		if geo, err := s.client.GeometryHistory(ctx, uuid); err == nil {
			d.hist.Geometry = geo
		} else {
			d.histErr = err
		}
		if d.status.Stage == stage.MaterialDraft || d.status.Stage == stage.Material {
			if mat, err := s.client.MaterialHistory(ctx, uuid); err == nil {
				d.hist.Material = mat
			} else if d.histErr == nil {
				d.histErr = err
			}
		}
	}
	d.list = assets.Index(card, d.hist)
	return d, nil
}

// GetTask is get_task: the task's stage, every stored setting by stage, its
// input images with view tags and weights, versions, previews and files.
func (s *Service) GetTask(ctx context.Context, id string) (Result, error) {
	uuid, err := s.resolveTask(id)
	if err != nil {
		return Result{}, err
	}
	d, err := s.loadDetail(ctx, uuid, true)
	if err != nil {
		return Result{}, err
	}
	card := d.card
	front := taskDetailFront{Task: s.taskFront(card), Settings: groupSettings(card.Settings)}

	// Redo counters at the draft stages (plan limits from the privileges).
	if d.status.Stage == stage.GeometryDraft || d.status.Stage == stage.MaterialDraft {
		if info, err := s.client.GetInfo(ctx); err == nil {
			geoLimit, matLimit := stage.RedoLimits(info.Meta.Privileges)
			r := &RedoLeft{}
			if d.status.Stage == stage.GeometryDraft && d.hist.Geometry != nil {
				n := stage.RedosLeft(geoLimit, len(d.hist.Geometry))
				r.Geometry = &n
			}
			if d.status.Stage == stage.MaterialDraft && d.hist.Material != nil {
				n := stage.RedosLeft(matLimit, len(d.hist.Material[assets.MaterialRef(card, d.hist)]))
				r.Material = &n
			}
			if r.Geometry != nil || r.Material != nil {
				front.Task.RedoLeft = r
			}
		}
	}

	// Input images with their tags and weights, in upload order.
	names := make([]string, 0, len(card.Info.MeshInputImages))
	for n := range card.Info.MeshInputImages {
		names = append(names, n)
	}
	sortInputNames(names)
	labels, _ := card.Settings["image_label"].([]any)
	weights, _ := card.Settings["view_weights"].([]any)
	for i, n := range names {
		in := inputFront{File: n, View: "?"}
		if i < len(labels) {
			if l, ok := labels[i].(string); ok {
				in.View = l
			}
		}
		if i < len(weights) {
			if w, ok := weights[i].(float64); ok {
				in.Weight = w
			}
		}
		front.Inputs = append(front.Inputs, in)
	}

	for _, v := range assets.SortVersions(keysOf(d.hist.Geometry)) {
		gv := d.hist.Geometry[v]
		front.Versions.Geometry = append(front.Versions.Geometry, geoVersionFront{Version: v, Generated: gv.GenerationTime, Bang: gv.HasBang})
	}
	refs := keysOf(d.hist.Material)
	sort.Strings(refs)
	for _, ref := range refs {
		for _, v := range assets.SortVersions(keysOf(d.hist.Material[ref])) {
			front.Versions.Material = append(front.Versions.Material, matVersionFront{Reference: ref, Version: v})
		}
	}
	if c := (confirmedFront{
		GeometryVersion: card.Confirmed("model_version"), GeometryAt: card.Confirmed("model_confirm_at"),
		MaterialVersion: card.Confirmed("texture_version"), MaterialAt: card.Confirmed("texture_confirm_at"),
	}); c != (confirmedFront{}) {
		front.Confirmed = &c
	}

	for _, stg := range assets.Stages {
		if modes := assets.Available(d.list, stg); len(modes) > 0 {
			front.Previews = append(front.Previews, previewFront{Stage: stg, Modes: modes})
		}
	}
	if front.Previews == nil {
		front.Previews = []previewFront{}
	}

	if d.status.State == stage.Packed {
		items, err := s.client.GetDownload(ctx, uuid)
		for _, it := range items {
			front.Files.Pack = append(front.Files.Pack, it.Name)
		}
		// Only an answer that lists nothing removes the action; a failed call
		// says nothing about the pack.
		if err == nil && len(front.Files.Pack) == 0 {
			// Hyper3D lists no file for this pack (seen on a material
			// draft marked DoneBasic): there is nothing to download.
			front.Task.Actions = without(front.Task.Actions, "download_model")
		}
	}
	front.Files.Videos = videoNames(card)
	if st, err := s.Store(); err == nil {
		if rows, err := st.Files(uuid, "download"); err == nil {
			for _, f := range rows {
				if _, err := os.Stat(f.Path); err == nil {
					front.Files.Downloaded = append(front.Files.Downloaded, fileFront{Name: filepath.Base(f.Path), Path: f.Path, Bytes: f.Bytes})
				}
			}
		}
		if rows, err := st.LedgerFor(uuid); err == nil {
			front.Spend = spendRows(rows)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s, %s.", clip(card.Prompt, 80), describeStage(d.status), d.status.State)
	if len(front.Inputs) > 0 {
		views := make([]string, len(front.Inputs))
		for i, in := range front.Inputs {
			views[i] = in.View
		}
		fmt.Fprintf(&b, " %d input image(s), views %s.", len(front.Inputs), strings.Join(views, ", "))
	}
	if d.histErr != nil {
		fmt.Fprintf(&b, "\n\nThe version history could not be read (%s); versions and earlier-stage previews are missing.", Classify(d.histErr).Message)
	}
	if len(front.Files.Pack) > 0 {
		fmt.Fprintf(&b, "\n\nPacked files: %s. download_model saves them to %s.", strings.Join(front.Files.Pack, ", "), front.Task.FilesDir)
	}
	b.WriteString("\n\nNext: " + nextFor(front.Task))
	res := Result{Front: front, Body: b.String()}
	if img, ok := s.posterImage(ctx, card.TaskUUID, d.list, d.status); ok {
		res.Images = append(res.Images, img)
	}
	return res, nil
}

// sortInputNames orders input image names the way they were uploaded: the
// service names them by upload index, zero-padded (0000.webp, 0001.webp,
// seen live on single- and multi-image cards), so the number before the
// extension is the position image_label and view_weights refer to. Names
// without a number sort after, by name.
func sortInputNames(names []string) {
	index := func(n string) int {
		base := strings.TrimSuffix(n, filepath.Ext(n))
		if i, err := strconv.Atoi(base); err == nil {
			return i
		}
		return 1 << 30
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, b := index(names[i]), index(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func videoNames(card *rodin.Card) []string {
	var out []string
	for _, k := range []string{"geometry_video", "render_video", "material_video"} {
		if card.Poster[k] != "" {
			out = append(out, k)
		}
	}
	return out
}

func describeStage(st stage.Status) string {
	switch st.Stage {
	case stage.GeometryDraft:
		return "geometry draft"
	case stage.Geometry:
		return "geometry confirmed"
	case stage.MaterialDraft:
		return "material draft"
	case stage.Material:
		return "material confirmed"
	}
	return st.Stage
}

func nextFor(t TaskFront) string {
	switch {
	case t.State == stage.Running:
		return "wait_task with this id until the stage changes."
	case len(t.Actions) > 0:
		return strings.Join(t.Actions, ", ") + " (get_preview takes a stage and mode from previews)."
	}
	return "get_preview for the images of this task."
}

// posterImage returns the current stage's poster as a 512 px PNG.
func (s *Service) posterImage(ctx context.Context, uuid string, list []assets.Asset, st stage.Status) (Image, bool) {
	a, ok := assets.Find(list, assets.PreviewStage(st.Stage), "poster")
	if !ok {
		return Image{}, false
	}
	cache, err := s.assetCache()
	if err != nil {
		return Image{}, false
	}
	file, _, err := cache.Get(ctx, uuid, a)
	if err != nil {
		return Image{}, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Image{}, false
	}
	png, _, _, err := imaging.ToPNG(data, 512)
	if err != nil {
		return Image{}, false
	}
	return Image{PNG: png, What: a.Stage + " poster"}, true
}

// --- wait_task ---

type waitFront struct {
	Task     TaskFront `yaml:"task"`
	Changed  bool      `yaml:"changed"`
	TimedOut bool      `yaml:"timed_out"`
	Waited   int       `yaml:"waited_seconds"`
}

// WaitPoll is the card poll period of wait_task (plan 2.4: 5 s); tests
// shorten it.
var WaitPoll = 5 * time.Second

// WaitTask is wait_task: poll the card until the task's stage or state
// changes, up to timeout.
func (s *Service) WaitTask(ctx context.Context, id string, timeout time.Duration) (Result, error) {
	uuid, err := s.resolveTask(id)
	if err != nil {
		return Result{}, err
	}
	start := time.Now()
	card, err := s.client.Card(ctx, uuid)
	if err != nil {
		return Result{}, err
	}
	first := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
	front := waitFront{}
	if first.State == stage.Running {
		deadline := start.Add(timeout)
		for {
			left := time.Until(deadline)
			if left <= 0 {
				front.TimedOut = true
				break
			}
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(min(WaitPoll, left)):
			}
			next, err := s.client.Card(ctx, uuid)
			if err != nil {
				return Result{}, err
			}
			card = next
			if now := stage.Of(card.Step, card.UserState, int(card.PendingJobs)); now != first {
				front.Changed = true
				break
			}
		}
	}
	front.Task = s.taskFront(card)
	front.Waited = int(time.Since(start).Round(time.Second).Seconds())
	now := stage.Of(card.Step, card.UserState, int(card.PendingJobs))

	var b strings.Builder
	switch {
	case first.State != stage.Running:
		fmt.Fprintf(&b, "No job is running on this task (%s, %s).", describeStage(now), now.State)
	case front.Changed:
		fmt.Fprintf(&b, "The task moved from %s (%s) to %s (%s).", describeStage(first), first.State, describeStage(now), now.State)
	default:
		fmt.Fprintf(&b, "Still %s (%s) after %d seconds.", now.State, describeStage(now), front.Waited)
	}
	if front.TimedOut {
		b.WriteString("\n\nNext: wait_task again with the same id.")
	} else {
		b.WriteString("\n\nNext: " + nextFor(front.Task))
	}
	res := Result{Front: front, Body: b.String()}
	if front.Changed && now.State != stage.Running {
		if img, ok := s.posterImage(ctx, uuid, assets.Index(card, assets.History{}), now); ok {
			res.Images = append(res.Images, img)
		}
	}
	return res, nil
}

// without returns list without name.
func without(list []string, name string) []string {
	out := []string{}
	for _, v := range list {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}
