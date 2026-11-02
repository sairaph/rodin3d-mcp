package mcpserver

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/store"
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

// setPrivacySettle shortens how long a privacy change waits after a toggle
// for one test.
func setPrivacySettle(t *testing.T, d time.Duration) {
	t.Helper()
	old := service.PrivacySettle
	service.PrivacySettle = d
	t.Cleanup(func() { service.PrivacySettle = old })
}

// checkToggleSent checks the reply of a privacy change whose toggle was
// accepted while no card read shows its result: get_task with task first,
// never set_private again now nor a retry.
func checkToggleSent(t *testing.T, r toolReply, hint, task string) {
	t.Helper()
	switch {
	case !strings.Contains(hint, "get_task with "+task+" first") || !strings.Contains(hint, "o not call set_private"):
		t.Fatalf("%q does not say get_task with %s first and not to call set_private now:\n%s", hint, task, r.text)
	case strings.Contains(hint, "Call set_private") || strings.Contains(hint, "Retry") || strings.Contains(hint, "retry"):
		t.Fatalf("%q advises set_private or a retry:\n%s", hint, r.text)
	}
}

// M4 (notes/75): task/set_private flips the task. Once a toggle may have
// reached Hyper3D and no card read after it shows the result (the read
// failed, or the card lags), the reply says accepted and get_task first,
// and a set_private sent after it decides only from a settled card, so a
// lagging card never makes it flip the task back. M5: the same holds for a
// toggle answered 5xx, which may have been applied.
func TestAcceptedToggleIsNeverSentAgain(t *testing.T) {
	// The cards lag less than the settle wait; get_task right after the
	// toggle still sees the old privacy.
	lag := 400 * time.Millisecond
	setPrivacySettle(t, 700*time.Millisecond)
	toggle := "task/set_private"
	t.Run("set_private, the read after it fails", func(t *testing.T) {
		f := newFixture(t, true, true)
		f.fake.FailAfter(toggle, "task/card")
		r := f.call("set_private", map[string]any{"task_id": fakehyper.GeometryID, "private": true})
		hint, _ := get(r.front, "error", "hint").(string)
		msg, _ := get(r.front, "error", "message").(string)
		if r.code() != "unavailable" || !strings.Contains(msg, "accepted") || get(r.front, "error", "fields", "task_id") != fakehyper.GeometryID {
			t.Fatalf("code %q, message %q:\n%s", r.code(), msg, r.text)
		}
		checkToggleSent(t, r, hint, fakehyper.GeometryID)
		if n := f.fake.Calls(toggle); n != 1 || !f.fake.Tasks(fakehyper.GeometryID).Private {
			t.Fatalf("%s sent %d times, private %v", toggle, n, f.fake.Tasks(fakehyper.GeometryID).Private)
		}
	})
	// M5: a toggle answered 5xx (a gateway failing after Hyper3D acted) may
	// have been applied: it is answered and recorded as an accepted one.
	// M6: so does one answered 201 with a body code naming a server failure.
	answers := []struct {
		name   string
		status int    // what task/set_private answers after it toggled; 0: 201
		code   string // or the body error code of its 201
	}{{"the card lags", 0, ""}, {"the toggle answered 503 after it was applied, the card lags", 503, ""},
		{"the toggle answered INTERNAL_SERVER_ERROR after it was applied, the card lags", 0, "INTERNAL_SERVER_ERROR"}}
	answer := func(f *fixture, status int, code string) {
		if code != "" {
			f.fake.ActThenBodyError(toggle, code)
		} else {
			f.fake.ActThenAnswer(toggle, status)
		}
	}
	for _, a := range answers {
		t.Run("set_private, "+a.name+", sent again after get_task", func(t *testing.T) {
			f := newFixture(t, true, true)
			f.fake.SetPrivacyLag(lag)
			answer(f, a.status, a.code)
			r := f.call("set_private", map[string]any{"task_id": fakehyper.GeometryID, "private": true})
			hint, _ := get(r.front, "error", "hint").(string)
			if r.code() != "unavailable" || get(r.front, "error", "fields", "toggle_sent") != true {
				t.Fatalf("code %q:\n%s", r.code(), r.text)
			}
			checkToggleSent(t, r, hint, fakehyper.GeometryID)
			setPrivateAgain(t, f, fakehyper.GeometryID)
		})
	}

	img := writeFile(t, "front.png", fakehyper.PNG(64, 64))
	newTask := "99999999-9999-4999-8999-000000000001" // the fake's first submit
	generate := func(t *testing.T) *fixture {
		f := newFixture(t, true, true)
		f.allow("generate")
		draft := f.fake.Tasks(fakehyper.DraftID)
		f.fake.AddTask(&fakehyper.Task{UUID: newTask, Prompt: draft.Prompt, Created: "2026-11-02T10:00:00.000Z",
			Step: "ModelGenerate", UserState: "DonePreview", Settings: draft.Settings, Inputs: draft.Inputs})
		return f
	}
	args := func() map[string]any {
		return map[string]any{"images": []any{map[string]any{"path": img}}, "tier": "medium"}
	}
	t.Run("new task, the reads after the toggle fail", func(t *testing.T) {
		f := generate(t)
		f.fake.FailAfter(toggle, "task/card")
		r := f.paidCall("generate_model", args())
		hint, _ := get(r.front, "error", "hint").(string)
		if r.code() != "unavailable" || !strings.Contains(hint, "get_task with "+newTask+" for its progress") {
			t.Fatalf("code %q, hint %q:\n%s", r.code(), hint, r.text)
		}
		checkToggleSent(t, r, hint, newTask)
		if n := f.fake.Calls(toggle); n != 1 {
			t.Fatalf("%s sent %d times", toggle, n)
		}
	})
	for _, a := range answers {
		t.Run("new task, "+a.name+", sent again after get_task", func(t *testing.T) {
			f := generate(t)
			f.fake.SetPrivacyLag(lag)
			answer(f, a.status, a.code)
			r := f.paidCall("generate_model", args())
			if r.code() != "" {
				t.Fatalf("code %q:\n%s", r.code(), r.text)
			}
			i := strings.Index(r.text, "Privacy:")
			if i < 0 {
				t.Fatalf("no privacy note:\n%s", r.text)
			}
			checkToggleSent(t, r, r.text[i:], newTask)
			setPrivateAgain(t, f, newTask)
		})
	}
}

// setPrivateAgain follows a toggleSent reply on task id the way an agent
// may: get_task still shows public (the card lags), so set_private private
// true. That call sends nothing and the task stays private: the toggle was
// sent once in all.
func setPrivateAgain(t *testing.T, f *fixture, id string) {
	t.Helper()
	if g := f.call("get_task", map[string]any{"task_id": id}); !strings.Contains(g.text, "private: false") {
		t.Fatalf("the lagging card does not show public:\n%s", g.text)
	}
	f.fake.ActThenAnswer("task/set_private", 0)
	r := f.call("set_private", map[string]any{"task_id": id, "private": true})
	if r.code() != "" || get(r.front, "changed") != false || !strings.Contains(r.text, "already private") {
		t.Fatalf("the second set_private:\n%s", r.text)
	}
	if n := f.fake.Calls("task/set_private"); n != 1 || !f.fake.Tasks(id).Private {
		t.Fatalf("task/set_private sent %d times, private %v", n, f.fake.Tasks(id).Private)
	}
}

// L7 (notes/75): split_parts step both whose split claim is refused after
// its plan was accepted (another session's split of the version started in
// between) says the plan was made, never to send step plan or both again,
// and step generate; the split is not sent.
func TestSplitBothClaimRefusedAfterPlan(t *testing.T) {
	f := newFixture(t, true, true)
	f.allow("split_parts")
	splitSetup(f, nil)
	f.fake.OnCall("task/bang_plan", func() {
		st, err := f.svc.Store()
		if err != nil {
			t.Error(err)
			return
		}
		for _, v := range f.fake.Tasks(fakehyper.DraftID).GeoVersions {
			if _, _, err := st.ClaimJob(store.Job{UUID: fakehyper.DraftID, Kind: "bang_generate", Key: v}, nil, 0); err != nil {
				t.Error(err)
			}
		}
	})
	r := f.paidCall("split_parts", map[string]any{"task_id": fakehyper.DraftID})
	hint, _ := get(r.front, "error", "hint").(string)
	msg, _ := get(r.front, "error", "message").(string)
	switch {
	case f.fake.Calls("task/bang_plan") != 1 || f.fake.Calls("task/bang_generate") != 0:
		t.Fatalf("bang_plan sent %d times, bang_generate %d:\n%s", f.fake.Calls("task/bang_plan"), f.fake.Calls("task/bang_generate"), r.text)
	case r.code() != "conflict" || !strings.Contains(msg, "was made, but the split was not sent"):
		t.Fatalf("code %q, message %q:\n%s", r.code(), msg, r.text)
	case !strings.Contains(hint, "Do not send step plan or both again") || !strings.Contains(hint, "get_task with "+fakehyper.DraftID) ||
		!strings.Contains(hint, "step generate"):
		t.Fatalf("hint %q:\n%s", hint, r.text)
	}
}

// Fix round 5 (notes/74): a paid send answered 5xx (a gateway failing after
// Hyper3D acted) may have started, as one that got no answer: the reply says
// so, never to send it again, and get_task (list_tasks for a create) shows
// whether it started; never a retry, which could pay twice.
func TestPaidSend5xxIsNeverSentAgain(t *testing.T) {
	img := writeFile(t, "front.png", fakehyper.PNG(64, 64))
	model := writeFile(t, "chair.obj", []byte("o chair\nv 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	list := "list_tasks"
	onTask := func(id string) string { return "get_task with " + id }
	planned := func(f *fixture, _ map[string]any) { splitSetup(f, nil); f.fake.Tasks(fakehyper.DraftID).Planned = true }
	cases := []struct {
		name, tool, sent string
		args             map[string]any
		where            string // what the hint must point to
		setup            func(f *fixture, args map[string]any)
		reskin           bool // the 503 is the second rodin_rigging (the reskin)
	}{
		{"generate_model", "generate_model", "task/rodin_mesh",
			map[string]any{"images": []any{map[string]any{"path": img}}, "tier": "medium"}, list, nil, false},
		{"generate_agentic", "generate_agentic", "task/rodin_agentic", map[string]any{"images": []string{img}, "budget": "basic"}, list, nil, false},
		{"texture_model", "texture_model", "task/rodin_mesh", map[string]any{"model_path": model, "images": []string{img}}, list, nil, false},
		{"fork_task", "fork_task", "task/rodin_fork", map[string]any{"task_id": fakehyper.GeometryID, "stage": "geometry"}, list, nil, false},
		{"redo_geometry", "redo_geometry", "task/rodin_update", map[string]any{"task_id": fakehyper.DraftID}, onTask(fakehyper.DraftID), nil, false},
		{"confirm_geometry", "confirm_geometry", "task/rodin_confirm", map[string]any{"task_id": fakehyper.DraftID}, onTask(fakehyper.DraftID), nil, false},
		{"select_candidate", "select_candidate", "task/select_candidate", map[string]any{"task_id": fakehyper.DraftID, "candidate": "v1"},
			onTask(fakehyper.DraftID), nil, false},
		{"generate_texture", "generate_texture", "task/rodin_texture", map[string]any{"task_id": fakehyper.GeometryID}, onTask(fakehyper.GeometryID), nil, false},
		{"redo_material", "redo_material", "task/rodin_update", map[string]any{"task_id": fakehyper.MatDraftID}, onTask(fakehyper.MatDraftID), nil, false},
		{"confirm_material", "confirm_material", "task/rodin_confirm", map[string]any{"task_id": fakehyper.MatDraftID}, onTask(fakehyper.MatDraftID), nil, false},
		{"export_model", "export_model", "task/rodin_unlock", map[string]any{"task_id": fakehyper.PackedID}, onTask(fakehyper.PackedID), nil, false},
		{"split_parts plan", "split_parts", "task/bang_plan", map[string]any{"task_id": fakehyper.DraftID, "step": "plan"},
			onTask(fakehyper.DraftID), splitSetup, false},
		{"split_parts generate", "split_parts", "task/bang_generate", map[string]any{"task_id": fakehyper.DraftID, "step": "generate"},
			onTask(fakehyper.DraftID), planned, false},
		{"split_parts both, the split answered 503", "split_parts", "task/bang_generate", map[string]any{"task_id": fakehyper.DraftID},
			onTask(fakehyper.DraftID) + " shows whether the split started", splitSetup, false},
		{"rig_model", "rig_model", "task/rodin_rigging", map[string]any{"task_id": fakehyper.PackedID, "template": "human"},
			onTask(fakehyper.PackedID), nil, false},
		{"rig_model with an FBX, the reskin answered 503", "rig_model", "task/rodin_rigging", map[string]any{"task_id": fakehyper.PackedID},
			"do not send it again", func(f *fixture, args map[string]any) {
				args["fbx_skeleton_path"] = fbxRig(t, f, fakehyper.PackedID, func(s string) string { return s })
			}, true},
		{"animate_model", "animate_model", "task/rodin_rigging_pack", map[string]any{"task_id": fakehyper.PackedID, "presets": []string{"human/Walking"}},
			onTask(fakehyper.PackedID), func(f *fixture, _ map[string]any) { f.fake.Tasks(fakehyper.PackedID).Rig = "human" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true, true)
			f.allow("generate", "redo", "confirm_geometry", "generate_texture", "confirm_material", "export", "fork", "agentic",
				"split_parts", "rig", "animate", "texture_model")
			if c.setup != nil {
				c.setup(f, c.args)
			}
			sends := 1
			if c.reskin {
				// The rig call is answered; the reskin call after its wait is not.
				sends = 2
				f.fake.OnCall("scheduler", func() { f.fake.ActThenAnswer(c.sent, 503) })
			} else {
				f.fake.ActThenAnswer(c.sent, 503)
			}
			r := f.paidCall(c.tool, c.args)
			hint, _ := get(r.front, "error", "hint").(string)
			msg, _ := get(r.front, "error", "message").(string)
			switch {
			case f.fake.Calls(c.sent) != sends:
				t.Fatalf("%s sent %d times:\n%s", c.sent, f.fake.Calls(c.sent), r.text)
			case r.code() != "unavailable" || !strings.Contains(msg, "may have started"):
				t.Fatalf("code %q, message %q:\n%s", r.code(), msg, r.text)
			case !strings.Contains(hint, c.where) || !strings.Contains(hint, "o not send it again"):
				t.Fatalf("hint %q does not say %q and not to send it again:\n%s", hint, c.where, r.text)
			case strings.Contains(hint, "Retry") || strings.Contains(hint, "retry"):
				t.Fatalf("hint %q advises a retry:\n%s", hint, r.text)
			}
		})
	}
}

// A recorded call whose send answered 5xx keeps its claim (it may run), so
// a repeat in this or another session is refused until store.ClaimBound
// instead of being sent and paid twice.
func TestPaidSend5xxKeepsItsClaim(t *testing.T) {
	cases := []struct {
		tool, sent string
		args       func() map[string]any
		setup      func(f *fixture)
	}{
		{"split_parts", "task/bang_generate", func() map[string]any { return map[string]any{"task_id": fakehyper.DraftID, "step": "generate"} },
			func(f *fixture) { splitSetup(f, nil); f.fake.Tasks(fakehyper.DraftID).Planned = true }},
		{"rig_model", "task/rodin_rigging", func() map[string]any { return map[string]any{"task_id": fakehyper.PackedID, "template": "human"} }, nil},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			f := newFixture(t, true, true)
			f.allow("split_parts", "rig")
			if c.setup != nil {
				c.setup(f)
			}
			f.fake.ActThenAnswer(c.sent, 503)
			if r := f.paidCall(c.tool, c.args()); r.code() != "unavailable" {
				t.Fatalf("first call:\n%s", r.text)
			}
			f.fake.ActThenAnswer(c.sent, 0)
			r := f.paidCall(c.tool, c.args())
			if r.code() != "conflict" || !strings.Contains(r.text, "still running") || f.fake.Calls(c.sent) != 1 {
				t.Fatalf("the repeat: %s sent %d times:\n%s", c.sent, f.fake.Calls(c.sent), r.text)
			}
		})
	}
}

// M6 (notes/75): a paid send answered 201 with a body code naming a failure
// on Hyper3D's side (INTERNAL_SERVER_ERROR) may have started, as a 5xx: the
// reply says so and never to send it again, the ledger records unknown, and
// a recorded call keeps its claim, so a repeat is refused.
func TestPaidSendBodyServerErrorIsNeverSentAgain(t *testing.T) {
	img := writeFile(t, "front.png", fakehyper.PNG(64, 64))
	cases := []struct {
		name, tool, sent string
		args             func() map[string]any
		where            string // what the hint must point to
		ledger           string // the task whose ledger row says unknown ("" none known)
		setup            func(f *fixture)
		claim            bool // a repeat is refused while the claim stays
	}{
		{"generate_model", "generate_model", "task/rodin_mesh",
			func() map[string]any {
				return map[string]any{"images": []any{map[string]any{"path": img}}, "tier": "medium"}
			}, "list_tasks", "", nil, false},
		{"redo_geometry", "redo_geometry", "task/rodin_update", func() map[string]any { return map[string]any{"task_id": fakehyper.DraftID} },
			"get_task with " + fakehyper.DraftID, fakehyper.DraftID, nil, false},
		{"split_parts generate", "split_parts", "task/bang_generate",
			func() map[string]any { return map[string]any{"task_id": fakehyper.DraftID, "step": "generate"} }, "get_task with " + fakehyper.DraftID,
			fakehyper.DraftID, func(f *fixture) { splitSetup(f, nil); f.fake.Tasks(fakehyper.DraftID).Planned = true }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true, true)
			f.allow("generate", "redo", "split_parts")
			if c.setup != nil {
				c.setup(f)
			}
			f.fake.ActThenBodyError(c.sent, "INTERNAL_SERVER_ERROR")
			r := f.paidCall(c.tool, c.args())
			hint, _ := get(r.front, "error", "hint").(string)
			msg, _ := get(r.front, "error", "message").(string)
			switch {
			case f.fake.Calls(c.sent) != 1:
				t.Fatalf("%s sent %d times:\n%s", c.sent, f.fake.Calls(c.sent), r.text)
			case r.code() != "unavailable" || !strings.Contains(msg, "may have started"):
				t.Fatalf("code %q, message %q:\n%s", r.code(), msg, r.text)
			case !strings.Contains(hint, c.where) || !strings.Contains(hint, "o not send it again") || strings.Contains(hint, "etry"):
				t.Fatalf("hint %q does not say %q and not to send it again, or advises a retry:\n%s", hint, c.where, r.text)
			}
			if c.ledger != "" {
				st, err := f.svc.Store()
				if err != nil {
					t.Fatal(err)
				}
				rows, err := st.LedgerFor(c.ledger)
				if err != nil || len(rows) == 0 || !strings.HasPrefix(rows[len(rows)-1].Result, "unknown:") {
					t.Fatalf("ledger of %s: %+v, %v", c.ledger, rows, err)
				}
			}
			if c.claim {
				f.fake.ActThenBodyError(c.sent, "")
				r := f.paidCall(c.tool, c.args())
				if r.code() != "conflict" || !strings.Contains(r.text, "still running") || f.fake.Calls(c.sent) != 1 {
					t.Fatalf("the repeat: %s sent %d times:\n%s", c.sent, f.fake.Calls(c.sent), r.text)
				}
			}
		})
	}
}
