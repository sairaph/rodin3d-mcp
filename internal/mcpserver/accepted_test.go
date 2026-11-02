package mcpserver

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
)

// priceNow reads the price a paid call refused with expected_credits -1.
var priceNow = regexp.MustCompile(`The price of \S+ is ([0-9.]+) credits now`)

// paidCall calls tool with expected_credits -1 (select_candidate takes
// none), and once more with the price when the call refused -1 for a known
// price.
func (f *fixture) paidCall(tool string, args map[string]any) toolReply {
	f.t.Helper()
	if tool != "select_candidate" {
		args["expected_credits"] = -1
	}
	r := f.call(tool, args)
	if m := priceNow.FindStringSubmatch(r.text); r.code() == "conflict" && m != nil {
		args["expected_credits"], _ = strconv.ParseFloat(m[1], 64)
		r = f.call(tool, args)
	}
	return r
}

// M3 (notes/75): once Hyper3D accepted a paid or changing request, a read
// after it that fails (the first card read, the wait for its jobs) answers
// that the call was accepted, never to send it again, and get_task for its
// progress: never the retry advice of the read that failed, which would pay
// twice. Each case sends its request once; the reads after it answer 503.
func TestAcceptedCallsAreNeverSentAgain(t *testing.T) {
	img := writeFile(t, "front.png", fakehyper.PNG(64, 64))
	model := writeFile(t, "chair.obj", []byte("o chair\nv 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	card, scheduler := "task/card", "scheduler"
	newTask := "99999999-9999-4999-8999-000000000001" // the fake's first submit or fork
	cases := []struct {
		name, tool, sent string
		fails            []string // the reads that fail after sent
		args             map[string]any
		task             string // the task get_task is to be called with
		setup            func(f *fixture, args map[string]any)
		then             string // more the hint must say
	}{
		{"generate_model", "generate_model", "task/rodin_mesh", []string{card},
			map[string]any{"images": []any{map[string]any{"path": img}}, "tier": "medium"}, newTask, nil, ""},
		{"generate_agentic", "generate_agentic", "task/rodin_agentic", []string{card},
			map[string]any{"images": []string{img}, "budget": "basic"}, newTask, nil, ""},
		{"texture_model", "texture_model", "task/rodin_mesh", []string{card},
			map[string]any{"model_path": model, "images": []string{img}}, newTask, nil, ""},
		{"redo_geometry", "redo_geometry", "task/rodin_update", []string{card},
			map[string]any{"task_id": fakehyper.DraftID}, fakehyper.DraftID, nil, ""},
		{"confirm_geometry", "confirm_geometry", "task/rodin_confirm", []string{card},
			map[string]any{"task_id": fakehyper.DraftID}, fakehyper.DraftID, nil, ""},
		{"select_candidate", "select_candidate", "task/select_candidate", []string{card},
			map[string]any{"task_id": fakehyper.DraftID, "candidate": "v1"}, fakehyper.DraftID, nil, ""},
		{"generate_texture", "generate_texture", "task/rodin_texture", []string{card},
			map[string]any{"task_id": fakehyper.GeometryID}, fakehyper.GeometryID, nil, ""},
		{"redo_material", "redo_material", "task/rodin_update", []string{card},
			map[string]any{"task_id": fakehyper.MatDraftID}, fakehyper.MatDraftID, nil, ""},
		{"confirm_material", "confirm_material", "task/rodin_confirm", []string{card},
			map[string]any{"task_id": fakehyper.MatDraftID}, fakehyper.MatDraftID, nil, ""},
		{"export_model", "export_model", "task/rodin_unlock", []string{card},
			map[string]any{"task_id": fakehyper.PackedID}, fakehyper.PackedID, nil, ""},
		{"fork_task", "fork_task", "task/rodin_fork", []string{card},
			map[string]any{"task_id": fakehyper.GeometryID, "stage": "geometry"}, newTask, nil, ""},
		{"split_parts plan", "split_parts", "task/bang_plan", []string{scheduler},
			map[string]any{"task_id": fakehyper.DraftID, "step": "plan"}, fakehyper.DraftID, splitSetup, "step generate"},
		{"split_parts generate", "split_parts", "task/bang_generate", []string{scheduler},
			map[string]any{"task_id": fakehyper.DraftID, "step": "generate"}, fakehyper.DraftID,
			func(f *fixture, _ map[string]any) { splitSetup(f, nil); f.fake.Tasks(fakehyper.DraftID).Planned = true }, "Bang version b1"},
		{"split_parts both, split refused", "split_parts", "task/bang_plan", []string{"task/bang_generate"},
			map[string]any{"task_id": fakehyper.DraftID}, fakehyper.DraftID, splitSetup, "step generate"},
		{"rig_model", "rig_model", "task/rodin_rigging", []string{scheduler},
			map[string]any{"task_id": fakehyper.PackedID, "template": "human"}, fakehyper.PackedID, nil, "rig block"},
		{"rig_model with an FBX, reskin refused", "rig_model", "task/rodin_rigging", []string{"task/rodin_rigging"},
			map[string]any{"task_id": fakehyper.PackedID}, fakehyper.PackedID, func(f *fixture, args map[string]any) {
				args["fbx_skeleton_path"] = fbxRig(t, f, fakehyper.PackedID, func(s string) string { return s })
			}, "reskin true"},
		{"animate_model", "animate_model", "task/rodin_rigging_pack", []string{card},
			map[string]any{"task_id": fakehyper.PackedID, "presets": []string{"human/Walking"}}, fakehyper.PackedID,
			func(f *fixture, _ map[string]any) { f.fake.Tasks(fakehyper.PackedID).Rig = "human" }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true, true)
			f.allow("generate", "redo", "confirm_geometry", "generate_texture", "confirm_material", "export", "fork", "agentic",
				"split_parts", "rig", "animate", "texture_model")
			if c.setup != nil {
				c.setup(f, c.args)
			}
			f.fake.FailAfter(c.sent, c.fails...)
			r := f.paidCall(c.tool, c.args)
			if n := f.fake.Calls(c.sent); n != 1 && !strings.Contains(c.name, "reskin") {
				t.Fatalf("%s sent %d times:\n%s", c.sent, n, r.text)
			}
			hint, _ := get(r.front, "error", "hint").(string)
			msg, _ := get(r.front, "error", "message").(string)
			switch {
			case r.code() != "unavailable" && r.code() != "service_error":
				t.Fatalf("code %q:\n%s", r.code(), r.text)
			case !strings.Contains(hint, "get_task with "+c.task) || !strings.Contains(hint, "o not send"):
				t.Fatalf("hint %q does not say get_task with %s and not to send it again:\n%s", hint, c.task, r.text)
			case strings.Contains(hint, "Retry") || strings.Contains(hint, "retry"):
				t.Fatalf("hint %q advises a retry:\n%s", hint, r.text)
			case !strings.Contains(msg, "accepted") && !strings.Contains(msg, "was made"):
				t.Fatalf("message %q does not say the request was accepted:\n%s", msg, r.text)
			case c.then != "" && !strings.Contains(hint+msg, c.then):
				t.Fatalf("the reply does not say %q:\n%s", c.then, r.text)
			}
		})
	}
}

// splitSetup makes the draft the measured split case (as splitFixture).
func splitSetup(f *fixture, _ map[string]any) {
	f.fake.Tasks(fakehyper.DraftID).Settings["brilliant_mode"] = "gen-2.5-extreme-low"
	f.fake.SetFile(fakehyper.DraftID+"/plan.png", fakehyper.PNG(80, 60))
}
