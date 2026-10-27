package mcpserver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/fbx"
	"github.com/sairaph/rodin3d-mcp/internal/fbx/fbxtest"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// paidProcedures are the procedures that spend credits or change a task.
var paidProcedures = []string{
	"task/rodin_mesh", "task/rodin_update", "task/rodin_confirm", "task/rodin_polygen", "task/rodin_texture", "task/rodin_unlock",
	"task/rodin_fork", "task/select_candidate", "task/rodin_agentic", "task/bang_plan", "task/bang_generate", "task/rodin_rigging",
	"task/rodin_rigging_pack",
}

// allow writes config.toml with keys allowed (a key not in the file is
// allowed too).
func (f *fixture) allow(keys ...string) { f.permissions("allow", keys...) }

// deny writes config.toml with keys denied (every other key allowed).
func (f *fixture) deny(keys ...string) { f.permissions("deny", keys...) }

func (f *fixture) permissions(value string, keys ...string) {
	f.t.Helper()
	var b strings.Builder
	b.WriteString("[permission]\n")
	for _, k := range keys {
		b.WriteString(k + " = \"" + value + "\"\n")
	}
	if err := os.MkdirAll(filepath.Dir(f.paths.Config), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.paths.Config, []byte(b.String()), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// sent returns the paid procedures the fake received.
func (f *fixture) sent() []string {
	var out []string
	for _, p := range paidProcedures {
		if f.fake.Calls(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// L8: each paid P3 tool refuses a denied key, an expected_credits that is
// not the quote, -1 for a known price and a number for an unknown one,
// before any paid request and before the free reads that precede it.
func TestP3PaidToolsRefuseBeforeAnyRequest(t *testing.T) {
	img := writeFile(t, "front.png", fakehyper.PNG(64, 64))
	img2 := writeFile(t, "back.png", fakehyper.PNG(64, 64))
	model := writeFile(t, "chair.obj", []byte("o chair\nv 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	type tc struct {
		name, key, tool string
		args            map[string]any
		code            string
	} // key: the [permission] key allowed, or denied when it starts with -
	agentic := func(exp float64) map[string]any {
		return map[string]any{"images": []string{img}, "budget": "basic", "expected_credits": exp}
	}
	split := func(step string, exp float64) map[string]any {
		return map[string]any{"task_id": fakehyper.DraftID, "step": step, "expected_credits": exp}
	}
	rig := func(tmpl string, exp float64) map[string]any {
		return map[string]any{"task_id": fakehyper.PackedID, "template": tmpl, "expected_credits": exp}
	}
	animate := func(presets []string, exp float64) map[string]any {
		return map[string]any{"task_id": fakehyper.PackedID, "presets": presets, "expected_credits": exp}
	}
	texture := func(images []string, exp float64) map[string]any {
		return map[string]any{"model_path": model, "images": images, "expected_credits": exp}
	}
	walk := []string{"human/Walking"}
	cases := []tc{
		// generate_agentic: basic is 0.5 (measured); every budget has a price.
		{"agentic denied", "-agentic", "generate_agentic", agentic(0.5), "forbidden"},
		{"agentic mismatch", "agentic", "generate_agentic", agentic(1), "conflict"},
		{"agentic -1 for known", "agentic", "generate_agentic", agentic(-1), "conflict"},
		// split_parts on a gen-2.5 high draft: generate 0.5 (label), both not known.
		{"split denied", "-split_parts", "split_parts", split("generate", 0.5), "forbidden"},
		{"split mismatch", "split_parts", "split_parts", split("generate", 0), "conflict"},
		{"split -1 for known", "split_parts", "split_parts", split("generate", -1), "conflict"},
		{"split unknown without -1", "split_parts", "split_parts", split("both", 0.5), "conflict"},
		// rig_model on a textured gen-2.5 high task: human 0 (measured), dragon not known.
		{"rig denied", "-rig", "rig_model", rig("human", 0), "forbidden"},
		{"rig mismatch", "rig", "rig_model", rig("human", 0.5), "conflict"},
		{"rig -1 for known", "rig", "rig_model", rig("human", -1), "conflict"},
		{"rig unknown without -1", "rig", "rig_model", rig("dragon", 0), "conflict"},
		// animate_model: one preset 0 (measured), two not known.
		{"animate denied", "-animate", "animate_model", animate(walk, 0), "forbidden"},
		{"animate mismatch", "animate", "animate_model", animate(walk, 0.5), "conflict"},
		{"animate -1 for known", "animate", "animate_model", animate(walk, -1), "conflict"},
		{"animate unknown without -1", "animate", "animate_model", animate([]string{"human/Walking", "human/Run"}, 0), "conflict"},
		// texture_model: one image 0 (measured), two not known.
		{"texture denied", "-texture_model", "texture_model", texture([]string{img}, 0), "forbidden"},
		{"texture mismatch", "texture_model", "texture_model", texture([]string{img}, 0.5), "conflict"},
		{"texture -1 for known", "texture_model", "texture_model", texture([]string{img}, -1), "conflict"},
		{"texture unknown without -1", "texture_model", "texture_model", texture([]string{img, img2}, 0), "conflict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true, true)
			f.fake.Tasks(fakehyper.PackedID).Rig = "human"
			if k, denied := strings.CutPrefix(c.key, "-"); denied {
				f.deny(k)
			} else {
				f.allow(c.key)
			}
			r := f.call(c.tool, c.args)
			if r.code() != c.code {
				t.Fatalf("code %q, want %q:\n%s", r.code(), c.code, r.text)
			}
			if s := f.sent(); len(s) > 0 {
				t.Fatalf("sent %v", s)
			}
			if n := f.fake.Calls("task/rodin_mesh_image_process"); n > 0 {
				t.Fatalf("preprocess called %d times", n)
			}
			if c.code == "conflict" && !strings.Contains(r.text, "quote_cost") {
				t.Fatalf("no quote hint:\n%s", r.text)
			}
		})
	}
}

// splitFixture is a fixture whose draft is a gen-2.5 extreme-low one (the
// measured split case) with split_parts allowed.
func splitFixture(t *testing.T) *fixture {
	f := newFixture(t, true, true)
	f.fake.Tasks(fakehyper.DraftID).Settings["brilliant_mode"] = "gen-2.5-extreme-low"
	f.fake.SetFile(fakehyper.DraftID+"/plan.png", fakehyper.PNG(80, 60))
	f.allow("split_parts")
	return f
}

// L8, L1, M2: the branches of split_parts and the bodies they send.
func TestSplitPartsBranches(t *testing.T) {
	whole := map[string]any{"uuid": fakehyper.DraftID, "model_version": "v2"}
	planBody := func(n float64) map[string]any {
		return map[string]any{"uuid": fakehyper.DraftID, "model_version": "v2", "settings": map[string]any{"bang_component_num": n}}
	}

	t.Run("generate without a plan", func(t *testing.T) {
		f := splitFixture(t)
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "generate", "expected_credits": 0.5})
		if r.code() != "conflict" || !strings.Contains(r.text, "no Bang plan") || len(f.sent()) > 0 {
			t.Fatalf("%s %v:\n%s", r.code(), f.sent(), r.text)
		}
	})
	t.Run("plan", func(t *testing.T) {
		f := splitFixture(t)
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "strength": 5, "expected_credits": 0})
		if r.code() != "" || len(r.images()) != 1 || get(r.front, "detail", "plan") != "succeeded" || f.fake.Calls("task/bang_generate") != 0 {
			t.Fatalf("%s images %d:\n%s", r.code(), len(r.images()), r.text)
		}
		if b := f.fake.Bodies("task/bang_plan"); !reflect.DeepEqual(b, []map[string]any{planBody(5)}) {
			t.Fatalf("plan bodies %v", b)
		}
		if b := f.fake.Bodies("task/bang_plan_result"); len(b) != 1 || !reflect.DeepEqual(b[0], whole) {
			t.Fatalf("plan result bodies %v", b)
		}
		if !strings.Contains(r.text, "step generate") {
			t.Fatalf("next step:\n%s", r.text)
		}
	})
	t.Run("plan, wait false", func(t *testing.T) {
		f := splitFixture(t)
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "expected_credits": 0, "wait": false})
		if r.code() != "" || get(r.front, "detail", "plan") != "running" || f.fake.Calls("scheduler") != 0 || f.fake.Calls("task/bang_plan") != 1 {
			t.Fatalf("%s scheduler %d:\n%s", r.code(), f.fake.Calls("scheduler"), r.text)
		}
		if !strings.Contains(r.text, "was sent and is running") || !strings.Contains(r.text, "No split was sent") {
			t.Fatalf("text:\n%s", r.text)
		}
	})
	t.Run("generate after the plan", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.Tasks(fakehyper.DraftID).Planned = true
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "generate", "expected_credits": 0.5})
		if r.code() != "" || get(r.front, "detail", "split") != "succeeded" || f.fake.Calls("task/bang_plan") != 0 {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		if b := f.fake.Bodies("task/bang_generate"); !reflect.DeepEqual(b, []map[string]any{whole}) {
			t.Fatalf("generate bodies %v", b)
		}
	})
	t.Run("generate, wait false", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.Tasks(fakehyper.DraftID).Planned = true
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "generate", "expected_credits": 0.5, "wait": false})
		if r.code() != "" || get(r.front, "detail", "split") != "running" || f.fake.Calls("scheduler") != 0 {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		if !strings.Contains(r.text, "Do not call split_parts again") {
			t.Fatalf("text:\n%s", r.text)
		}
	})
	t.Run("both waits as with wait true", func(t *testing.T) {
		for _, wait := range []bool{true, false} {
			f := splitFixture(t)
			r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "expected_credits": 0.5, "wait": wait})
			if r.code() != "" || f.fake.Calls("task/bang_plan") != 1 || f.fake.Calls("task/bang_generate") != 1 {
				t.Fatalf("wait %v: %s:\n%s", wait, r.code(), r.text)
			}
			if get(r.front, "detail", "split") != "succeeded" {
				t.Fatalf("wait %v: split %v", wait, get(r.front, "detail", "split"))
			}
		}
	})
	t.Run("plan running at the bound", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.JobState = "Running"
		setFlowWait(t)
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "expected_credits": 0.5})
		if r.code() != "" || get(r.front, "detail", "plan") != "running" || f.fake.Calls("task/bang_generate") != 0 {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		if !strings.Contains(r.text, "still running after") || !strings.Contains(r.text, "step generate") || strings.Contains(r.text, "Do not call split_parts again") {
			t.Fatalf("text:\n%s", r.text)
		}
	})
	t.Run("one part again", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.Tasks(fakehyper.DraftID).Bangs = map[string][]string{"v2": {"b1"}}
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "bang_version": "b1", "part": "root.2", "expected_credits": -1})
		if r.code() != "" {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		part := map[string]any{"uuid": fakehyper.DraftID, "model_version": "v2", "bang_version": "b1", "bang_component_id": "root.2",
			"settings": map[string]any{"bang_component_num": 5.0}}
		if b := f.fake.Bodies("task/bang_plan"); !reflect.DeepEqual(b, []map[string]any{part}) {
			t.Fatalf("plan bodies %v", b)
		}
		if b := f.fake.Bodies("task/bang_generate"); !reflect.DeepEqual(b, []map[string]any{whole}) {
			t.Fatalf("generate bodies %v", b)
		}
	})
	t.Run("bang_version without part", func(t *testing.T) {
		f := splitFixture(t)
		r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "bang_version": "b1", "expected_credits": 0.5})
		if r.code() != "invalid_input" || len(f.sent()) > 0 {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
	})
}

// M3: a RodinAgentic task offers only reading, and every tool that acts on
// a task refuses it before any request.
func TestRodinAgenticTasksGetNoStagedTools(t *testing.T) {
	f := newFixture(t, true, true)
	for _, id := range []string{fakehyper.DraftID, fakehyper.GeometryID, fakehyper.MatDraftID, fakehyper.PackedID, fakehyper.RunningID} {
		f.fake.Tasks(id).Type = rodin.TypeAgentic
	}
	f.fake.Tasks(fakehyper.RunningID).Next = nil // stays running
	f.fake.Tasks(fakehyper.PackedID).Rig = "human"
	f.allow("generate", "redo", "confirm_geometry", "generate_texture", "confirm_material", "export", "fork", "split_parts", "rig", "animate")
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"redo_geometry", map[string]any{"task_id": fakehyper.DraftID}},
		{"confirm_geometry", map[string]any{"task_id": fakehyper.DraftID, "expected_credits": -1}},
		{"select_candidate", map[string]any{"task_id": fakehyper.DraftID, "candidate": "v1"}},
		{"split_parts", map[string]any{"task_id": fakehyper.DraftID, "expected_credits": -1}},
		{"generate_texture", map[string]any{"task_id": fakehyper.GeometryID, "expected_credits": -1}},
		{"export_model", map[string]any{"task_id": fakehyper.GeometryID, "expected_credits": -1}},
		{"fork_task", map[string]any{"task_id": fakehyper.GeometryID, "stage": "geometry", "expected_credits": -1}},
		{"rig_model", map[string]any{"task_id": fakehyper.GeometryID, "expected_credits": -1}},
		{"redo_material", map[string]any{"task_id": fakehyper.MatDraftID}},
		{"confirm_material", map[string]any{"task_id": fakehyper.MatDraftID, "expected_credits": -1}},
		{"animate_model", map[string]any{"task_id": fakehyper.PackedID, "presets": []string{"human/Walking"}, "expected_credits": -1}},
		{"confirm_geometry", map[string]any{"task_id": fakehyper.RunningID, "expected_credits": -1}},
	}
	for _, c := range calls {
		r := f.call(c.tool, c.args)
		if r.code() != "conflict" || !strings.Contains(r.text, "RodinAgentic") || !strings.Contains(r.text, "get_task, wait_task and get_preview") {
			t.Errorf("%s: %s:\n%s", c.tool, r.code(), r.text)
		}
	}
	if s := f.sent(); len(s) > 0 {
		t.Fatalf("sent %v", s)
	}
	for id, want := range map[string][]any{fakehyper.DraftID: {"get_preview"}, fakehyper.PackedID: {"get_preview"}, fakehyper.RunningID: {"wait_task"}} {
		r := f.call("get_task", map[string]any{"task_id": id})
		if got := get(r.front, "task", "actions"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s actions %v, want %v", id, got, want)
		}
		if get(r.front, "bang") != nil || get(r.front, "rig") != nil {
			t.Errorf("%s: bang or rig block on a RodinAgentic task", id)
		}
	}
	if n := f.fake.Calls("task/rodin_rigging_result") + f.fake.Calls("task/bang_history"); n > 0 {
		t.Errorf("get_task read the rig or Bang results of a RodinAgentic task %d times", n)
	}
}

// setFlowWait shortens the tool wait and the scheduler poll for one test.
func setFlowWait(t *testing.T) time.Duration {
	t.Helper()
	old, oldPoll := service.FlowWait, service.JobPoll
	service.FlowWait, service.JobPoll = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { service.FlowWait, service.JobPoll = old, oldPoll })
	return old
}

// fbxRig writes the human FBX fixture and serves, as the task's rig file,
// a rig whose bones are the FBX bones (named as listed) at their FBX
// frames.
func fbxRig(t *testing.T, f *fixture, id string, rename func(string) string) string {
	t.Helper()
	data := fbxtest.Human()
	path := writeFile(t, "skeleton.fbx", data)
	bones, err := fbx.Skeleton(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var parents []int
	var inv [][16]float64
	for _, b := range bones {
		names = append(names, rename(b.Name))
		parents = append(parents, b.Parent)
		inv = append(inv, b.Transform.Invert())
	}
	f.fake.SetFile(id+"/model.glb.rigging.rodinrig", fbxtest.RigFile(names, parents, inv))
	return path
}

// M6: rig_model with an FBX skeleton sends the web's Import FBX: the rig
// from the FBX bones (shell, custom), then the reskin call with the rig's
// bones turned to the FBX frames, under one quote; reskin true sends only
// the second; the refusals send nothing.
func TestRigModelFBXSkeleton(t *testing.T) {
	same := func(s string) string { return s }
	t.Run("both calls", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, same)
		r := f.call("rig_model", map[string]any{"task_id": fakehyper.PackedID, "fbx_skeleton_path": path, "expected_credits": -1})
		if r.code() != "" || get(r.front, "detail", "rig", "reskin") != "succeeded" {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		calls := f.fake.Bodies("task/rodin_rigging")
		if len(calls) != 2 {
			t.Fatalf("%d rigging calls", len(calls))
		}
		bones, _ := fbx.Skeleton(fbxtest.Human())
		first := calls[0]["settings"].(map[string]any)
		h := first["rodinrig_hierarchy"].([]any)
		if first["skinning_method"] != "shell" || first["rodinrig_template"] != "custom" || len(h) != len(bones) ||
			get(first, "source", "stage") != "texture_refine" {
			t.Fatalf("first call %v", first)
		}
		for i, b := range bones {
			e := h[i].(map[string]any)
			if e["name"] != b.Name || e["parent"] != float64(b.Parent) || len(e) != 2 {
				t.Fatalf("first call bone %d: %v", i, e)
			}
		}
		second := calls[1]["settings"].(map[string]any)
		h2 := second["rodinrig_hierarchy"].([]any)
		if second["skinning_method"] != "shell" || second["rodinrig_template"] != "custom" || len(h2) != len(bones) {
			t.Fatalf("second call %v", second)
		}
		for i, b := range bones {
			e := h2[i].(map[string]any)
			tr, _ := e["transform"].([]any)
			if e["name"] != strings.TrimPrefix(b.Name, "mixamorig:") || len(tr) != 16 {
				t.Fatalf("second call bone %d: %v", i, e)
			}
			for k := range tr {
				if d := tr[k].(float64) - b.Transform[k]; d > 1e-9 || d < -1e-9 {
					t.Fatalf("%s element %d: %v, want %v", e["name"], k, tr[k], b.Transform[k])
				}
			}
		}
		if !strings.Contains(r.text, "turned the 22 bones to the frames of the bones of skeleton.fbx") {
			t.Fatalf("text:\n%s", r.text)
		}
	})
	t.Run("reskin only", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		f.fake.Tasks(fakehyper.PackedID).Rig, f.fake.Tasks(fakehyper.PackedID).RigSkinning = "custom", "shell"
		path := fbxRig(t, f, fakehyper.PackedID, same)
		r := f.call("rig_model", map[string]any{"task_id": fakehyper.PackedID, "fbx_skeleton_path": path, "reskin": true, "expected_credits": -1})
		if r.code() != "" || len(f.fake.Bodies("task/rodin_rigging")) != 1 || get(r.front, "detail", "rig", "reskin") != "succeeded" {
			t.Fatalf("%s %d:\n%s", r.code(), len(f.fake.Bodies("task/rodin_rigging")), r.text)
		}
		if _, ok := f.fake.Bodies("task/rodin_rigging")[0]["settings"].(map[string]any)["rodinrig_hierarchy"].([]any)[0].(map[string]any)["transform"]; !ok {
			t.Fatal("the reskin call carries no transforms")
		}
	})
	t.Run("bones that do not match", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, func(s string) string { return strings.Replace(s, "Head", "Skull", 1) })
		r := f.call("rig_model", map[string]any{"task_id": fakehyper.PackedID, "fbx_skeleton_path": path, "expected_credits": -1})
		if r.code() != "" || len(f.fake.Bodies("task/rodin_rigging")) != 1 || get(r.front, "detail", "rig", "reskin") != "not sent" ||
			// L2: right after the rig only the web's own match decides.
			!strings.Contains(r.text, "the rig's bones and the FBX bones do not match: cannot match imported bone: mixamorig:Head") {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		// L3: a reskin that cannot work is not advised.
		if strings.Contains(r.text, "reskin true") || !strings.Contains(r.text, "The web stops here too") {
			t.Fatalf("next step:\n%s", r.text)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, same)
		notFBX := writeFile(t, "skeleton.obj", []byte("o x\n"))
		for name, args := range map[string]map[string]any{
			"reskin without rig":   {"fbx_skeleton_path": path, "reskin": true},
			"core":                 {"fbx_skeleton_path": path, "skinning": "core"},
			"template and fbx":     {"fbx_skeleton_path": path, "template": "dragon"},
			"not an fbx name":      {"fbx_skeleton_path": notFBX},
			"reskin without a fbx": {"reskin": true},
		} {
			args["task_id"], args["expected_credits"] = fakehyper.PackedID, -1
			r := f.call("rig_model", args)
			if r.code() != "invalid_input" && r.code() != "conflict" {
				t.Errorf("%s: %s:\n%s", name, r.code(), r.text)
			}
		}
		if n := len(f.fake.Bodies("task/rodin_rigging")); n != 0 {
			t.Fatalf("%d rigging calls", n)
		}
	})
}

// The rig_model schema fills no template, so a skeleton text or a
// description reaches the service alone (a filled template refused them).
func TestRigModelTextSkeletonReachesTheService(t *testing.T) {
	f := newFixture(t, true, true)
	f.allow("rig")
	for _, args := range []map[string]any{{"skeleton": "Root\n-Tail"}, {"description": "a snake"}} {
		args["task_id"], args["expected_credits"] = fakehyper.PackedID, -1
		if r := f.call("rig_model", args); r.code() != "" {
			t.Fatalf("%v: %s:\n%s", args, r.code(), r.text)
		}
	}
	calls := f.fake.Bodies("task/rodin_rigging")
	if len(calls) != 2 || calls[0]["settings"].(map[string]any)["rodinrig_template"] != "custom" || calls[1]["settings"].(map[string]any)["rigging_mode"] != "generate" {
		t.Fatalf("calls %v", calls)
	}
}

// M-1, L4, L5: a rig or reskin that outlasts the wait is followed through
// get_task (the card shows no rig job) and never sent again while it runs;
// reskin true reskins only the rig made from the same FBX bones, refusing
// any other before a request or a ledger row.
func TestRigModelFollowsItsJobs(t *testing.T) {
	same := func(s string) string { return s }
	rigging := func(f *fixture) int { return len(f.fake.Bodies("task/rodin_rigging")) }
	args := func(path string, reskin bool) map[string]any {
		a := map[string]any{"task_id": fakehyper.PackedID, "fbx_skeleton_path": path, "expected_credits": -1}
		if reskin {
			a["reskin"] = true
		}
		return a
	}
	t.Run("reskin outlasts the wait", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		f.fake.Tasks(fakehyper.PackedID).Rig, f.fake.Tasks(fakehyper.PackedID).RigSkinning = "custom", "shell"
		path := fbxRig(t, f, fakehyper.PackedID, same)
		f.fake.JobState = "Running"
		setFlowWait(t)
		r := f.call("rig_model", args(path, true))
		if r.code() != "" || rigging(f) != 1 || get(r.front, "detail", "rig", "state") != "done" || get(r.front, "detail", "rig", "reskin") != "running" {
			t.Fatalf("%s %d:\n%s", r.code(), rigging(f), r.text)
		}
		if strings.Contains(r.text, "reskin true sends") || !strings.Contains(r.text, "its rig block's reskin says when the reskin call is done") {
			t.Fatalf("next step:\n%s", r.text)
		}
		g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
		if get(g.front, "rig", "state") != "done" || get(g.front, "rig", "reskin") != "running" || get(g.front, "rig", "fbx") != path {
			t.Fatalf("get_task rig %v:\n%s", get(g.front, "rig"), g.text)
		}
		if again := f.call("rig_model", args(path, true)); again.code() != "conflict" || rigging(f) != 1 {
			t.Fatalf("a second reskin while one runs: %s %d:\n%s", again.code(), rigging(f), again.text)
		}
		if other := f.call("rig_model", map[string]any{"task_id": fakehyper.PackedID, "template": "human", "expected_credits": 0}); other.code() != "conflict" || rigging(f) != 1 {
			t.Fatalf("a new rig while the reskin runs: %s %d:\n%s", other.code(), rigging(f), other.text)
		}
		f.fake.JobState = "Succeeded"
		if g = f.call("get_task", map[string]any{"task_id": fakehyper.PackedID}); get(g.front, "rig", "reskin") != "succeeded" {
			t.Fatalf("get_task rig after the job %v", get(g.front, "rig"))
		}
	})
	t.Run("the first call outlasts the wait", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, same)
		f.fake.JobState = "Running"
		setFlowWait(t)
		r := f.call("rig_model", args(path, false))
		if r.code() != "" || rigging(f) != 1 || get(r.front, "detail", "rig", "state") != "running" || get(r.front, "detail", "rig", "reskin") != "not sent" ||
			!strings.Contains(r.text, "once its rig block says state done, rig_model with the same fbx_skeleton_path and reskin true") {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
		if get(g.front, "rig", "state") != "running" || contains(get(g.front, "task", "actions"), "animate_model") {
			t.Fatalf("get_task while the rig runs %v %v", get(g.front, "rig"), get(g.front, "task", "actions"))
		}
		if again := f.call("rig_model", args(path, true)); again.code() != "conflict" || rigging(f) != 1 {
			t.Fatalf("reskin while the rig runs: %s:\n%s", again.code(), again.text)
		}
		f.fake.JobState = "Succeeded"
		g = f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
		if get(g.front, "rig", "state") != "done" || get(g.front, "rig", "reskin") != "not sent" || !strings.Contains(g.text, "reskin true sends it") {
			t.Fatalf("get_task after the rig %v:\n%s", get(g.front, "rig"), g.text)
		}
		if r = f.call("rig_model", args(path, true)); r.code() != "" || rigging(f) != 2 || get(r.front, "detail", "rig", "reskin") != "succeeded" {
			t.Fatalf("reskin: %s %d:\n%s", r.code(), rigging(f), r.text)
		}
	})
	// L5, L7 (review 58): the calls describe the rig they made, known by its
	// generation_time, not by its template: a rig made later in the web with
	// the same template is not theirs. reskin true of the rig a recorded FBX
	// call made (the first call outlasted the wait) checks only what Reskin
	// checks, as right after the rig: a rig with a bone more is reskinned.
	t.Run("the rig a recorded FBX call made", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, same)
		bones, _ := fbx.Skeleton(fbxtest.Human())
		var names []string
		var parents []int
		var inv [][16]float64
		for _, b := range bones {
			names, parents, inv = append(names, b.Name), append(parents, b.Parent), append(inv, b.Transform.Invert())
		}
		names, parents, inv = append(names, "Tail"), append(parents, 0), append(inv, bones[0].Transform.Invert())
		f.fake.SetFile(fakehyper.PackedID+"/model.glb.rigging.rodinrig", fbxtest.RigFile(names, parents, inv))
		f.fake.JobState = "Running"
		setFlowWait(t)
		if r := f.call("rig_model", args(path, false)); r.code() != "" || get(r.front, "detail", "rig", "state") != "running" {
			t.Fatalf("first call: %s:\n%s", r.code(), r.text)
		}
		f.fake.JobState = "Succeeded"
		if g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID}); get(g.front, "rig", "fbx") != path || get(g.front, "rig", "reskin") != "not sent" {
			t.Fatalf("get_task after the rig %v", get(g.front, "rig"))
		}
		task := f.fake.Tasks(fakehyper.PackedID)
		ours := task.RigTime
		task.RigTime = "2026-10-27T11:00:00.000Z" // a custom rig made in the web
		if g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID}); get(g.front, "rig", "state") != "done" || get(g.front, "rig", "fbx") != nil || get(g.front, "rig", "reskin") != nil {
			t.Fatalf("get_task after a web rig %v", get(g.front, "rig"))
		}
		if r := f.call("rig_model", args(path, true)); r.code() != "conflict" || !strings.Contains(r.text, "the rig has 23 bones and the FBX 22") || rigging(f) != 1 {
			t.Fatalf("reskin of the web rig: %s:\n%s", r.code(), r.text)
		}
		task.RigTime = ours
		if r := f.call("rig_model", args(path, true)); r.code() != "" || rigging(f) != 2 || get(r.front, "detail", "rig", "reskin") != "succeeded" {
			t.Fatalf("reskin of the recorded call's rig: %s:\n%s", r.code(), r.text)
		}
		if g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID}); get(g.front, "rig", "fbx") != path || get(g.front, "rig", "reskin") != "succeeded" {
			t.Fatalf("get_task after the reskin %v", get(g.front, "rig"))
		}
	})
	// L3 (review 58): a rig call that failed leaves the task's earlier rig,
	// so the reply offers animate_model as get_task does.
	t.Run("a failed rig keeps the earlier one", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		task := f.fake.Tasks(fakehyper.PackedID)
		task.Rig, task.RigTime = "human", "2026-10-27T09:00:00.000Z"
		f.fake.JobState = "Failed"
		r := f.call("rig_model", map[string]any{"task_id": fakehyper.PackedID, "template": "human", "expected_credits": 0})
		if r.code() != "" || get(r.front, "detail", "rig", "state") != "failed" || !contains(get(r.front, "task", "actions"), "animate_model") {
			t.Fatalf("%s %v:\n%s", r.code(), get(r.front, "task", "actions"), r.text)
		}
		g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID})
		if get(g.front, "rig", "last_call") != "failed" || !contains(get(g.front, "task", "actions"), "animate_model") {
			t.Fatalf("get_task %v %v", get(g.front, "rig"), get(g.front, "task", "actions"))
		}
	})
	t.Run("a rig of another skeleton", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.allow("rig")
		path := fbxRig(t, f, fakehyper.PackedID, same)
		// The human template's Mixamo names cover the FBX's bones, so the
		// reskin could be computed from it: the template refuses it.
		f.fake.Tasks(fakehyper.PackedID).Rig = "human"
		r := f.call("rig_model", args(path, true))
		if r.code() != "conflict" || !strings.Contains(r.text, "human template rig") || rigging(f) != 0 {
			t.Fatalf("human rig: %s:\n%s", r.code(), r.text)
		}
		// A custom rig with a bone more than the FBX.
		f.fake.Tasks(fakehyper.PackedID).Rig, f.fake.Tasks(fakehyper.PackedID).RigSkinning = "custom", "shell"
		bones, _ := fbx.Skeleton(fbxtest.Human())
		var names []string
		var parents []int
		var inv [][16]float64
		for _, b := range bones {
			names, parents, inv = append(names, b.Name), append(parents, b.Parent), append(inv, b.Transform.Invert())
		}
		names, parents, inv = append(names, "Tail"), append(parents, 0), append(inv, bones[0].Transform.Invert())
		f.fake.SetFile(fakehyper.PackedID+"/model.glb.rigging.rodinrig", fbxtest.RigFile(names, parents, inv))
		r = f.call("rig_model", args(path, true))
		if r.code() != "conflict" || !strings.Contains(r.text, "the rig has 23 bones and the FBX 22") || rigging(f) != 0 {
			t.Fatalf("larger rig: %s:\n%s", r.code(), r.text)
		}
		if g := f.call("get_task", map[string]any{"task_id": fakehyper.PackedID}); get(g.front, "spend") != nil {
			t.Fatalf("a refused reskin left ledger rows: %v", get(g.front, "spend"))
		}
	})
}

func contains(list any, v string) bool {
	l, _ := list.([]any)
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// L6, L7: the plan split_parts sent is followed through get_task with its
// picture, and step generate is sent only after a finished plan of the same
// part, never while a split of the version still runs.
func TestSplitPartsFollowsItsPlan(t *testing.T) {
	gen := func(extra map[string]any) map[string]any {
		a := map[string]any{"task_id": fakehyper.DraftID, "step": "generate", "expected_credits": 0.5}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	t.Run("plan, wait false", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.JobState = "Running"
		if r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "expected_credits": 0, "wait": false}); r.code() != "" ||
			!strings.Contains(r.text, "its bang_plan block says when the plan is made") {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		if r := f.call("split_parts", gen(nil)); r.code() != "conflict" || !strings.Contains(r.text, "still running") || f.fake.Calls("task/bang_generate") != 0 {
			t.Fatalf("generate while the plan runs: %s:\n%s", r.code(), r.text)
		}
		g := f.call("get_task", map[string]any{"task_id": fakehyper.DraftID})
		if get(g.front, "bang_plan", "state") != "running" || get(g.front, "bang_plan", "geometry_version") != "v2" || len(g.images()) != 1 {
			t.Fatalf("get_task while the plan runs: %v, %d images", get(g.front, "bang_plan"), len(g.images()))
		}
		f.fake.JobState = "Succeeded"
		g = f.call("get_task", map[string]any{"task_id": fakehyper.DraftID})
		if get(g.front, "bang_plan", "state") != "succeeded" || len(g.images()) != 2 || !strings.Contains(g.text, "split_parts with step generate and version v2") {
			t.Fatalf("get_task after the plan: %v, %d images:\n%s", get(g.front, "bang_plan"), len(g.images()), g.text)
		}
		if r := f.call("split_parts", gen(nil)); r.code() != "" || get(r.front, "detail", "split") != "succeeded" {
			t.Fatalf("generate: %s:\n%s", r.code(), r.text)
		}
		if g = f.call("get_task", map[string]any{"task_id": fakehyper.DraftID}); get(g.front, "bang_plan") != nil || len(g.images()) != 1 {
			t.Fatalf("get_task after the split still shows the plan: %v", get(g.front, "bang_plan"))
		}
	})
	t.Run("a plan of another part", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.Tasks(fakehyper.DraftID).Bangs = map[string][]string{"v2": {"b1"}}
		part := map[string]any{"bang_version": "b1", "part": "root.2"} // the split's label 0.5
		if r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "expected_credits": 0}); r.code() != "" {
			t.Fatalf("whole plan: %s:\n%s", r.code(), r.text)
		}
		if r := f.call("split_parts", gen(part)); r.code() != "conflict" || !strings.Contains(r.text, "is of the whole model, not of part root.2 of Bang result b1") {
			t.Fatalf("part split after a whole plan: %s:\n%s", r.code(), r.text)
		}
		planPart := map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "bang_version": "b1", "part": "root.2", "expected_credits": -1}
		if r := f.call("split_parts", planPart); r.code() != "" {
			t.Fatalf("part plan: %s:\n%s", r.code(), r.text)
		}
		if r := f.call("split_parts", gen(nil)); r.code() != "conflict" || !strings.Contains(r.text, "is of part root.2 of Bang result b1, not of the whole model") {
			t.Fatalf("whole split after a part plan: %s:\n%s", r.code(), r.text)
		}
		if f.fake.Calls("task/bang_generate") != 0 {
			t.Fatal("a split was sent")
		}
		if r := f.call("split_parts", gen(part)); r.code() != "" || f.fake.Calls("task/bang_generate") != 1 {
			t.Fatalf("part split after its plan: %s:\n%s", r.code(), r.text)
		}
	})
	// M1 (review 58): a plan sent before the newest one is read from the
	// scheduler before the split's claim, by step generate and by step both,
	// so it does not block every split once it ended.
	t.Run("an older plan no read follows", func(t *testing.T) {
		for _, step := range []string{"generate", "both"} {
			f := splitFixture(t)
			f.fake.JobState = "Running"
			for i := 0; i < 2; i++ {
				if r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": "plan", "expected_credits": 0, "wait": false}); r.code() != "" {
					t.Fatalf("plan %d: %s:\n%s", i, r.code(), r.text)
				}
			}
			f.fake.JobState = "Succeeded"
			r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": step, "expected_credits": 0.5})
			if r.code() != "" || f.fake.Calls("task/bang_generate") != 1 || get(r.front, "detail", "split") != "succeeded" {
				t.Fatalf("step %s after two plans: %s:\n%s", step, r.code(), r.text)
			}
		}
	})
	t.Run("a split still running", func(t *testing.T) {
		f := splitFixture(t)
		f.fake.Tasks(fakehyper.DraftID).Planned = true // a plan made elsewhere
		f.fake.JobState = "Running"
		if r := f.call("split_parts", gen(map[string]any{"wait": false})); r.code() != "" || f.fake.Calls("task/bang_generate") != 1 {
			t.Fatalf("%s:\n%s", r.code(), r.text)
		}
		for _, step := range []string{"generate", "both"} {
			r := f.call("split_parts", map[string]any{"task_id": fakehyper.DraftID, "step": step, "expected_credits": 0.5})
			if r.code() != "conflict" || !strings.Contains(r.text, "is still running") || f.fake.Calls("task/bang_generate") != 1 || f.fake.Calls("task/bang_plan") != 0 {
				t.Fatalf("step %s while the split runs: %s:\n%s", step, r.code(), r.text)
			}
		}
	})
}

// L2: the measured Bang confirm price holds for a draft fork_task made (its
// ledger row names the task); any other extreme-low draft may hold a submit
// freeze, so its price is not known.
func TestBangConfirmQuoteNeedsAForkMadeHere(t *testing.T) {
	f := splitFixture(t)
	quote := func() toolReply {
		return f.call("quote_cost", map[string]any{"operation": "confirm_geometry", "task_id": fakehyper.DraftID,
			"settings": map[string]any{"mesh": "raw", "bang_version": "b1"}})
	}
	if q := quote(); get(q.front, "quote", "known") != false {
		t.Fatalf("not forked here: %v", get(q.front, "quote"))
	}
	st, err := f.svc.Store()
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddLedger(store.LedgerEntry{Op: "fork_task", UUID: fakehyper.GeometryID})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishLedger(id, fakehyper.DraftID, nil, nil, "ok"); err != nil {
		t.Fatal(err)
	}
	if q := quote(); get(q.front, "quote", "credits") != 0.5 || get(q.front, "quote", "measured") != true {
		t.Fatalf("forked here: %v", get(q.front, "quote"))
	}
}
