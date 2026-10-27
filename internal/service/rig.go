package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/fbx"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rigdata"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Rigging and animation (notes 14 section 6; web call sites
// main.fmt.js:55804-56090 rigging, 39843-39863 animation pack, builders
// 149330-149393). A task with confirmed geometry gets a skeleton and skin
// from task/rodin_rigging (job on the scheduler, result read with
// rodin_rigging_result); task/rodin_rigging_pack then exports the rigged
// model with up to 10 animations, and task/get_download lists the files.

// rigSource is the rig source of a task at status st (main.fmt.js:149431):
// the confirmed geometry, also while the material is a draft, or the
// confirmed material.
func rigSource(st stage.Status) (string, bool) {
	switch st.Stage {
	case stage.Geometry, stage.MaterialDraft:
		return rodin.RigSourceModel, true
	case stage.Material:
		return rodin.RigSourceTexture, true
	}
	return "", false
}

// RigInput is the input of rig_model.
type RigInput struct {
	TaskID      string
	Template    string // human quadruped bird dragon kaiju
	Skeleton    string // own bone hierarchy, one bone per line, depth by leading "-"
	Description string // a skeleton described in words: the server plans the bones
	Skinning    string // shell | core
	// FBXSkeleton is the path of an FBX file whose skeleton the rig takes:
	// its bones (the first call), then their frames (the reskin call).
	FBXSkeleton string
	// Reskin applies the frames of FBXSkeleton to the task's current rig
	// only (the second call), for a rig whose first call outlasted the wait.
	Reskin   bool
	Expected *float64
	Wait     bool
}

type rigFront struct {
	Source   string `yaml:"source"`
	Template string `yaml:"template,omitempty"`
	Skinning string `yaml:"skinning,omitempty"`
	Bones    int    `yaml:"bones,omitempty"`
	// State: succeeded (rig_model's new rig), done (the task's rig: get_task,
	// and rig_model with reskin true), running (a rig call rig_model sent is
	// still running), failed, canceled, or none (get_task: no rig).
	State string `yaml:"state"`
	// LastCall (get_task) is how the newest rig call this tool sent on the
	// task ended when that was not a success: failed or canceled (the rig
	// shown, if any, is an earlier one; left out once a rig was made after
	// that end was read). FBX and Reskin are shown only while the task's
	// rig is the one those calls left (rigState, noteRigEnd).
	LastCall string `yaml:"last_call,omitempty"`
	// FBX and Reskin: the imported skeleton file and the state of the
	// reskin call that turns the rig's bones to its frames (succeeded |
	// running | failed | canceled | not sent).
	FBX    string `yaml:"fbx,omitempty"`
	Reskin string `yaml:"reskin,omitempty"`
}

// fbxSkeleton reads the skeleton of the FBX file at path as the web's
// import does (an .fbx name, three.js FBXLoader, the rigging panel's bone
// walk).
func fbxSkeleton(path string) ([]fbx.Bone, error) {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".fbx") {
		return nil, bad(fmt.Sprintf("fbx_skeleton_path %q is not an absolute path of an .fbx file.", path), "Pass the absolute path of the FBX file whose skeleton the rig should take.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, bad(fmt.Sprintf("fbx_skeleton_path cannot be read: %v.", err), "Pass the path of an existing FBX file.")
	}
	bones, err := fbx.Skeleton(data)
	if err != nil {
		return nil, bad("The FBX skeleton cannot be used: "+err.Error()+".", "Pass an FBX file with a skeleton (bones), as the web's Import FBX takes.")
	}
	return bones, nil
}

// rigSettings builds the rodin_rigging settings of in for source; bones
// are the FBX skeleton when in has one.
func rigSettings(in RigInput, source string, bones []fbx.Bone) (map[string]any, rigFront, error) {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	tmpl := strings.ToLower(strings.TrimSpace(in.Template))
	skel, desc, fbxPath := strings.TrimSpace(in.Skeleton), strings.TrimSpace(in.Description), strings.TrimSpace(in.FBXSkeleton)
	given := 0
	for _, v := range []string{tmpl, skel, desc, fbxPath} {
		if v != "" {
			given++
		}
	}
	if given > 1 {
		return nil, rigFront{}, bad("Pass one of template, skeleton, description or fbx_skeleton_path.",
			"template picks a built-in skeleton; skeleton gives your own bone hierarchy; description lets Hyper3D plan the bones from words; fbx_skeleton_path takes the skeleton of an FBX file.")
	}
	if given == 0 {
		tmpl = "human" // the web panel's default template
	}
	skinning := strings.ToLower(strings.TrimSpace(in.Skinning))
	if skinning != "" && !oneOf(skinning, "shell", "core") {
		return nil, rigFront{}, bad(fmt.Sprintf("skinning %q is not shell or core.", in.Skinning), "Pass shell or core (core works with the human template only).")
	}
	st := map[string]any{"source": map[string]string{"stage": source}}
	front := rigFront{Source: source}
	switch {
	case tmpl != "":
		t, ok := catalog.RigTemplateNamed(tmpl)
		if !ok {
			return nil, rigFront{}, bad(fmt.Sprintf("template %q is not human, quadruped, bird, dragon or kaiju.", in.Template), "Pass one of the listed templates.")
		}
		if skinning == "" {
			skinning = t.Skinning
		}
		bones := catalog.ParseSkeleton(t.Skeleton)
		st["rodinrig_hierarchy"], st["rodinrig_template"] = bones, t.Name
		front.Template, front.Bones = t.Name, len(bones)
	case skel != "":
		bones := catalog.ParseSkeleton(skel)
		if len(bones) == 0 {
			return nil, rigFront{}, bad("skeleton names no bone.", "Pass one bone name per line; leading - characters give the depth (Hips, -Spine, --Chest).")
		}
		st["rodinrig_hierarchy"], st["rodinrig_template"] = bones, "custom"
		front.Template, front.Bones = "custom", len(bones)
	case fbxPath != "":
		// The web sends the FBX bones' names and parents and always skins
		// an own skeleton file with shell (main.fmt.js:55808-55812, 55925).
		if skinning == "core" {
			return nil, rigFront{}, bad("core skinning works with the human template only; an FBX skeleton is skinned with shell.", "Leave skinning out.")
		}
		h := make([]catalog.Bone, len(bones))
		for i, b := range bones {
			h[i] = catalog.Bone{Name: b.Name, Parent: b.Parent}
		}
		st["rodinrig_hierarchy"], st["rodinrig_template"] = h, "custom"
		front.Template, front.Bones, front.FBX = "custom", len(h), fbxPath
		skinning = "shell"
	default:
		st["rodinrig_description"], st["rigging_mode"], st["rodinrig_template"] = desc, "generate", "custom"
		front.Template = "custom"
	}
	if skinning == "" {
		skinning = "shell"
	}
	if skinning == "core" && front.Template != "human" {
		return nil, rigFront{}, bad("core skinning works with the human template only.", "Leave skinning out (shell) or use template human.")
	}
	st["skinning_method"] = skinning
	front.Skinning = skinning
	return st, front, nil
}

// RigModel is rig_model. With an FBX skeleton it sends what the web's
// Import FBX sends: the rig from the FBX bones, then, once that rig is
// done, the reskin call that turns its bones to the FBX bones' frames
// (both within one wait and one quote). With reskin true it sends only the
// reskin, of the rig made from the same FBX bones. Every rig and reskin job
// is recorded, so get_task follows it while the card shows nothing.
func (s *Service) RigModel(ctx context.Context, in RigInput) (Result, error) {
	if _, err := s.permit("rig_model"); err != nil {
		return Result{}, err
	}
	fbxPath := strings.TrimSpace(in.FBXSkeleton)
	if in.Reskin && fbxPath == "" {
		return Result{}, fail(render.CodeInvalidInput, "reskin needs fbx_skeleton_path: the FBX whose bone frames the rig takes.", "Pass fbx_skeleton_path with reskin true.")
	}
	var bones []fbx.Bone
	if fbxPath != "" {
		var err error
		if bones, err = fbxSkeleton(fbxPath); err != nil {
			return Result{}, err
		}
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "rig_model")
	if err != nil {
		return Result{}, err
	}
	source, _ := rigSource(st)
	settings, rig, err := rigSettings(in, source, bones)
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{Operation: "rig_model", RigTemplate: rig.Template, Skinning: rig.Skinning}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "rig_model", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	before, err := s.rigBusy(ctx, uuid, source)
	if err != nil {
		return Result{}, err
	}
	// reskin true: the reskin of the task's current rig is read, checked and
	// computed before the ledger row, so a refusal sends and records nothing.
	var call *reskinCall
	if in.Reskin {
		var block *reskinBlock
		if call, block = s.reskinFor(ctx, uuid, source, fbxPath, bones, false); block != nil {
			code := render.CodeConflict
			if block.retry {
				code = render.CodeUnavailable
			}
			return Result{}, fail(code, "The reskin call was not sent: "+block.why+".", block.hint)
		}
		rig.State, rig.Template, rig.Skinning = "done", call.template, call.skinning
	}
	deadline := time.Now().Add(FlowWait)
	// The FBX import needs the finished rig before its reskin call, so it
	// waits as with wait true.
	wait := in.Wait || fbxPath != ""
	detail := map[string]string{"template": rig.Template}
	if fbxPath != "" {
		detail["fbx"] = fbxPath
	}
	if call != nil {
		before = call.gen // the rig the reskin turns
	}
	if before != "" {
		detail[rigBefore] = before
	}
	// The call is recorded in the same store step that checks no rig or
	// reskin call runs, so a second session cannot pass rigBusy meanwhile.
	kind := jobRig
	if in.Reskin {
		kind = jobReskin
	}
	c, busy, err := s.claimJob(ctx, uuid, kind, source, detail, rigKinds, 0)
	if err != nil {
		return Result{}, err
	}
	if busy != nil {
		return Result{}, rigRunning(uuid, *busy)
	}
	sp.open()
	var rigID int64
	if !in.Reskin {
		reply, err := s.client.Rigging(ctx, uuid, settings)
		if err != nil {
			c.drop()
			sp.close(ctx, err)
			return Result{}, sendFailure("rig_model", uuid, err)
		}
		id := c.fill(reply.Job.UUIDs, nil)
		rigID = id
		jr := jobsResult{}
		if wait {
			if jr, err = s.waitJobs(ctx, reply.Job.UUIDs, deadline); err != nil {
				sp.close(ctx, nil)
				then := ""
				if fbxPath != "" {
					then = "; once its rig block says done, rig_model with the same fbx_skeleton_path and reskin true sends the reskin call"
				}
				return Result{}, wrapf(err, "the rigging was started; get_task with %s shows when it is done%s", uuid, then)
			}
			s.endJobs(id, jr)
		}
		rig.State = jobWord(jr)
		if jr.Done {
			s.endRigCall(ctx, id, uuid, source)
		}
	}
	var block *reskinBlock
	if fbxPath != "" {
		if !in.Reskin && rig.State == "succeeded" {
			call, block = s.reskinFor(ctx, uuid, source, fbxPath, bones, true)
			if call != nil {
				// The reskin is claimed like the rig: another session's
				// reskin true may have started since the rig ended.
				c, busy, err = s.claimJob(ctx, uuid, jobReskin, source, map[string]string{"fbx": fbxPath, "template": call.template, rigBefore: call.gen}, rigKinds, rigID)
				switch {
				case err != nil:
					call, block = nil, &reskinBlock{why: Classify(err).Message, hint: "Call rig_model with the same fbx_skeleton_path and reskin true in a moment.", retry: true}
				case busy != nil:
					call, block = nil, &reskinBlock{why: fmt.Sprintf("a %s call rig_model sent at %s on this task is still running", busy.Kind, sentAt(*busy)),
						hint: "In a minute or two, get_task with " + uuid + ": its rig block says when it is done. Do not call rig_model again until then."}
				}
			}
		}
		rig.Reskin = "not sent"
		if call != nil {
			state, err := s.sendReskin(ctx, uuid, source, c, call, deadline)
			if err != nil {
				sp.close(ctx, err)
				return Result{}, reskinFailure(uuid, in.Reskin, err)
			}
			rig.Reskin = state
			if !in.Reskin {
				rig.Template, rig.Skinning = call.template, call.skinning
			}
		}
	}
	sp.close(ctx, nil)
	if rig.State == "succeeded" && call == nil {
		// The result is the web's own rig data (a gzipped DRCRIG01 file of
		// bones and skin weights the browser applies), not a model file:
		// animate_model exports the rigged model.
		if res, err := s.client.RiggingResult(ctx, uuid, source); err == nil && res.Template != "" {
			rig.Template = res.Template
		}
	}
	card2, err := s.client.Card(ctx, uuid)
	if err != nil {
		card2 = card
	}
	hasRig := rig.State == "succeeded" || rig.State == "done"
	done := hasRig && (fbxPath == "" || rig.Reskin == "succeeded")
	// The task block reads the task's rig as get_task does (an earlier rig
	// kept after this call failed offers animate_model too); this call's own
	// rig is added, in case the result does not show it yet.
	front := flowFront{Task: s.liveTaskFront(ctx, card2), Charge: sp.front(), TimedOut: rig.State == "running" || rig.Reskin == "running", Extra: map[string]any{"rig": rig}}
	addRigAction(&front.Task, hasRig)
	var b strings.Builder
	switch rig.State {
	case "succeeded":
		fmt.Fprintf(&b, "Rigged with the %s skeleton (%s skinning).", rig.Template, rig.Skinning)
	case "done":
		fmt.Fprintf(&b, "The task's rig (%s skeleton, %s skinning) was kept; only the reskin call was sent.", rig.Template, rig.Skinning)
	case "running":
		b.WriteString("Rigging started and is still running.")
	default:
		fmt.Fprintf(&b, "Rigging ended %s.", rig.State)
	}
	if fbxPath != "" {
		const what = " The reskin call that turns the rig's bones to the frames of the FBX bones"
		switch {
		case rig.Reskin == "succeeded":
			fmt.Fprintf(&b, " The reskin call then turned the %d bones to the frames of the bones of %s, as the web's Import FBX does.", rig.Bones, filepath.Base(fbxPath))
		case rig.Reskin == "running":
			b.WriteString(what + " was sent and is still running.")
		case rig.Reskin != "not sent":
			fmt.Fprintf(&b, "%s ended %s.", what, rig.Reskin)
		case block != nil:
			b.WriteString(what + " was not sent: " + block.why + ".")
		case rig.State == "running":
			b.WriteString(what + " was not sent: it needs the finished rig.")
		default:
			fmt.Fprintf(&b, "%s was not sent: the rig ended %s.", what, rig.State)
		}
	}
	b.WriteString("\n\n" + sp.walletText())
	switch {
	case done:
		t, _ := catalog.RigTemplateNamed(rig.Template)
		next := "\n\nNext: animate_model exports the rigged model, with animations or without (list_animation_presets lists them"
		if t.Animations != "" {
			next += "; category " + t.Animations + " fits this skeleton"
		}
		b.WriteString(next + "); download_model then saves the files.")
	case rig.State == "running" && fbxPath != "":
		b.WriteString("\n\nNext: in a minute or two, get_task with " + uuid + ": once its rig block says state done, rig_model with the same fbx_skeleton_path and reskin true sends the reskin call. Do not call rig_model again without reskin for it.")
	case rig.State == "running":
		b.WriteString("\n\nNext: in a minute or two, get_task with " + uuid + ": its rig block says when the rig is done. Do not call rig_model again for it.")
	case rig.Reskin == "running":
		b.WriteString("\n\nNext: in a minute or two, get_task with " + uuid + ": its rig block's reskin says when the reskin call is done. Do not call rig_model again for it.")
	case block != nil:
		b.WriteString("\n\nNext: " + block.hint)
	case hasRig && fbxPath != "":
		// The reskin ended failed or canceled.
		b.WriteString("\n\nNext: rig_model with the same fbx_skeleton_path and reskin true sends the reskin call again; animate_model works with the rig as it is.")
	case !hasRig:
		b.WriteString("\n\nNext: get_task with " + uuid + " shows the task's rig; rig_model again sends a new rig.")
	}
	return Result{Front: front, Body: b.String()}, nil
}

// rigBusy refuses a rig while a rig or reskin call rig_model sent on the
// same source still runs: the web's panel takes no new rig until its job
// ends, and a second call would repeat a paid request. It returns the
// task's rig as it is before the new call (rigGen; "" when it could not be
// read), and records it for the newest calls whose end it read
// (noteRigEnd), before the new call changes it.
func (s *Service) rigBusy(ctx context.Context, uuid, source string) (string, error) {
	gen, read := rigGen(s.client.RiggingResult(ctx, uuid, source))
	for _, kind := range rigKinds {
		j, ok := s.latestJob(uuid, kind, source)
		if !ok {
			continue
		}
		state, err := s.jobState(ctx, j)
		if err != nil {
			return "", wrapf(err, "read the state of the %s call sent at %s", kind, sentAt(j))
		}
		if state == "running" {
			return "", rigRunning(uuid, j)
		}
		if read {
			s.noteRigEnd(j, state, gen)
		}
	}
	if !read {
		gen = ""
	}
	return gen, nil
}

// The task's rig as the recorded calls name it: generation_time of
// rodin_rigging_result. The server sets it when it takes the rig's request,
// not when the rig is made (read 2026-10-27 on aebfc197: rig_model opened
// its ledger row at 23:11:38, generation_time 23:11:39.074, rig file written
// 23:11:54), and the result names no job, so it identifies a rig but cannot
// be compared with local times.
const (
	rigNone   = "none"       // the task has no rig (RIGGING_NOT_FOUND)
	rigBefore = "rig_before" // detail: the task's rig right before the call was sent
	rigAfter  = "rig_after"  // detail: the task's rig when the call's end was first read
)

// rigGen is the task's rig as a rodin_rigging_result read answered: its
// generation_time, or rigNone; read is false when the read failed.
func rigGen(res *rodin.RigResult, err error) (gen string, read bool) {
	switch {
	case isCode(err, "RIGGING_NOT_FOUND"):
		return rigNone, true
	case err != nil:
		return "", false
	}
	return scalarOf(res.Raw["generation_time"]), true
}

// noteRigEnd records, for the rig or reskin call j whose end state was read
// (not running), the task's rig gen when that end is first read, and returns
// the recorded rig ("" when none is recorded yet). A call that succeeded
// made the rig shown, unless the result still shows the rig from before the
// call (it is not updated yet): then nothing is recorded, and a later read
// records it. A call that ended otherwise left the task's rig as it was.
// So a recorded call describes the task's rig exactly while the rig's
// generation_time is its rig_after; a rig made in the web since then has
// another. One case stays open: a call whose end is first read after a web
// rig followed it takes that rig as its own (no field ties a rig to a job).
func (s *Service) noteRigEnd(j store.Job, state, gen string) string {
	if v, ok := j.Detail[rigAfter]; ok {
		return v
	}
	if b, ok := j.Detail[rigBefore]; ok && state == "succeeded" && gen == b {
		return ""
	}
	if st, err := s.Store(); err == nil {
		_ = st.SetDetail(j.ID, map[string]string{rigAfter: gen})
	}
	return gen
}

// endRigCall records the task's rig right after the recorded rig or reskin
// call id ended within the tool's wait (noteRigEnd), when the store has the
// call.
func (s *Service) endRigCall(ctx context.Context, id int64, uuid, source string) {
	if id == 0 {
		return
	}
	st, err := s.Store()
	if err != nil {
		return
	}
	j, err := st.Job(id)
	if err != nil || j.State == store.JobRunning {
		return
	}
	if gen, read := rigGen(s.client.RiggingResult(ctx, uuid, source)); read {
		s.noteRigEnd(j, j.State, gen)
	}
}

// rigKinds are the calls one rig source takes one at a time (rigBusy).
var rigKinds = []string{jobRig, jobReskin}

// rigRunning is the refusal of a rig while the recorded call j runs.
func rigRunning(uuid string, j store.Job) error {
	return failFields(render.CodeConflict, fmt.Sprintf("The %s call rig_model sent on this task at %s is still running.", j.Kind, sentAt(j)),
		"In a minute or two, get_task with "+uuid+": its rig block says when it is done. Do not call rig_model again until then.",
		map[string]any{"call": j.Kind, "sent": j.At.UTC().Format(time.RFC3339)})
}

// reskinCall is the web's second Import FBX call computed for the task's
// current rig: the rig's bones turned to the FBX bones' frames, with the
// rig's template and skinning (main.fmt.js:55813, 55854, 55957).
type reskinCall struct {
	settings           map[string]any
	template, skinning string
	gen                string // the rig it turns (rigGen)
}

// reskinBlock is why a reskin cannot be sent, the next step, and whether a
// later call may succeed (the rig could not be read now).
type reskinBlock struct {
	why, hint string
	retry     bool
}

// reskinFor reads the task's current rig and computes its reskin to the FBX
// bones of fbxPath. It reskins only the rig made from these bones (template
// custom, the same bones by name), as the web reskins only the rig its own
// first call just made; justRigged is true right after that call succeeded.
func (s *Service) reskinFor(ctx context.Context, uuid, source, fbxPath string, bones []fbx.Bone, justRigged bool) (*reskinCall, *reskinBlock) {
	const again = "Call rig_model with the same fbx_skeleton_path and reskin true again in a moment; animate_model already works with the rig as it is."
	const asIs = "The web stops here too: animate_model works with the rig as it is, without the FBX bones' frames; rig_model with another skeleton makes a new rig."
	firstCall := "rig_model with this fbx_skeleton_path and without reskin makes the rig from the FBX bones, then sends the reskin call."
	if justRigged {
		firstCall = asIs
	}
	res, err := s.client.RiggingResult(ctx, uuid, source)
	switch {
	case isCode(err, "RIGGING_NOT_FOUND") && !justRigged:
		return nil, &reskinBlock{why: "the task has no rig", hint: firstCall}
	case err != nil:
		return nil, &reskinBlock{why: "the rig's result could not be read (" + Classify(err).Message + ")", hint: again, retry: true}
	case res.URL == "":
		return nil, &reskinBlock{why: "the rig's result names no rig file", hint: again, retry: true}
	case res.Template != "custom":
		b := &reskinBlock{why: fmt.Sprintf("the task's rig is a %s template rig, not the rig of an FBX skeleton (custom)", orUnknown(res.Template)), hint: firstCall}
		if justRigged {
			// Right after the rig from the FBX bones the result should be
			// that rig: it does not show it yet.
			b.hint, b.retry = again, true
		}
		return nil, b
	}
	var buf bytes.Buffer
	if _, _, err := s.client.Fetch(ctx, res.URL, &buf, 256<<20); err != nil {
		return nil, &reskinBlock{why: "the rig file could not be read (" + Classify(err).Message + ")", hint: again, retry: true}
	}
	h, err := rigdata.DecodeHeader(buf.Bytes())
	if err != nil {
		return nil, &reskinBlock{why: "the rig file cannot be read: " + err.Error(), hint: asIs}
	}
	joints, err := rigdata.Joints(h)
	if err != nil {
		return nil, &reskinBlock{why: "the rig's skeleton cannot be read: " + err.Error(), hint: asIs}
	}
	// reskin true reskins only the rig made from these bones. Right after
	// that rig the web checks nothing more than Reskin does (every FBX bone
	// among the rig's joints, no count rule; main.fmt.js:56092-56115); the
	// same holds later for the rig a recorded call from this FBX made. Any
	// other custom rig must have exactly the FBX's bones.
	gen, _ := rigGen(res, nil)
	if !justRigged && !s.madeFromFBX(uuid, source, fbxPath, gen) {
		if err := rigdata.SameSkeleton(bones, joints); err != nil {
			return nil, &reskinBlock{why: "the task's rig is not the rig of these FBX bones: " + err.Error(), hint: firstCall}
		}
	}
	hierarchy, err := rigdata.Reskin(bones, joints)
	if err != nil {
		// The web stops here too and shows the reason.
		return nil, &reskinBlock{why: "the rig's bones and the FBX bones do not match: " + err.Error(), hint: asIs}
	}
	skinning := scalarOf(res.Raw["skinning_method"])
	return &reskinCall{template: res.Template, skinning: skinning, gen: gen, settings: map[string]any{"source": map[string]string{"stage": source},
		"skinning_method": skinning, "rodinrig_hierarchy": hierarchy, "rodinrig_template": res.Template}}, nil
}

// madeFromFBX reports whether the task's rig gen is the one a recorded rig
// or reskin call from the FBX file fbxPath made: such a call ended succeeded
// and recorded gen as its rig (noteRigEnd; a generation_time names one rig).
func (s *Service) madeFromFBX(uuid, source, fbxPath, gen string) bool {
	st, err := s.Store()
	if err != nil || gen == "" || gen == rigNone {
		return false
	}
	calls, err := st.JobsOf(uuid, source, rigKinds)
	if err != nil {
		return false
	}
	for _, j := range calls {
		if j.Detail["fbx"] == fbxPath && j.State == "succeeded" && j.Detail[rigAfter] == gen {
			return true
		}
	}
	return false
}

// sendReskin sends a reskin call claimed as c and follows its job until
// deadline; the job is recorded, so get_task follows it after the wait.
func (s *Service) sendReskin(ctx context.Context, uuid, source string, c claim, call *reskinCall, deadline time.Time) (string, error) {
	reply, err := s.client.Rigging(ctx, uuid, call.settings)
	if err != nil {
		c.drop()
		return "", err
	}
	id := c.fill(reply.Job.UUIDs, nil)
	jr, err := s.waitJobs(ctx, reply.Job.UUIDs, deadline)
	if err != nil {
		return "running", nil // sent; get_task follows the recorded job
	}
	s.endJobs(id, jr)
	if jr.Done {
		s.endRigCall(ctx, id, uuid, source)
	}
	return jobWord(jr), nil
}

// reskinFailure is the error of a reskin call that failed to send; it says
// what the rig is, so the agent does not send the first call again.
func reskinFailure(uuid string, reskinOnly bool, err error) error {
	e := Classify(sendFailure("rig_model (reskin)", uuid, err))
	lead := "The rig from the FBX bones is done; the reskin call failed: "
	if reskinOnly {
		lead = "The task's rig is unchanged; the reskin call failed: "
	}
	hint := e.Hint + " The reskin was not started: rig_model with the same fbx_skeleton_path and reskin true sends it again; animate_model works with the rig as it is."
	if sentUnanswered(err) {
		hint = "It may have started on Hyper3D, and no read shows its progress: do not send it again unless the user decides to. animate_model works with the rig either way."
	}
	return failFields(e.Code, lead+e.Message, hint, e.Fields)
}

// rigState reads the rig of a task at status st for get_task: nil when the
// stage has no rig source. The result is the task's rig; the newest rig and
// reskin calls rig_model recorded say whether one still runs (the card
// shows no rig job).
func (s *Service) rigState(ctx context.Context, uuid string, st stage.Status) *rigFront {
	source, ok := rigSource(st)
	if !ok || st.State == stage.Running {
		return nil
	}
	res, err := s.client.RiggingResult(ctx, uuid, source)
	gen, read := rigGen(res, err)
	if !read {
		return nil
	}
	out := &rigFront{Source: source, State: "done"}
	if gen == rigNone {
		out.State = "none"
	} else {
		out.Template, out.Skinning = res.Template, scalarOf(res.Raw["skinning_method"])
	}
	// The recorded calls describe the task's rig only while it is the rig
	// they left (noteRigEnd): a rig made since, in the web, is not theirs.
	rigCall, hasRigCall := s.latestJob(uuid, jobRig, source)
	if hasRigCall {
		state, err := s.jobState(ctx, rigCall)
		switch {
		case err != nil:
		case state == "running":
			// The result may still be an earlier rig's.
			out.State, out.Template, out.Skinning = "running", "", ""
		case s.noteRigEnd(rigCall, state, gen) != gen:
		case state != "succeeded":
			out.LastCall = state // the rig shown, if any, is the one from before it
		case rigCall.Detail["fbx"] != "":
			out.FBX, out.Reskin = rigCall.Detail["fbx"], "not sent"
		}
	}
	if r, ok := s.latestJob(uuid, jobReskin, source); ok && (!hasRigCall || !r.At.Before(rigCall.At)) {
		// A running reskin is shown as such (the result still shows the rig
		// it reskins); an ended one while the task's rig is the one it left:
		// its own when it succeeded, else the rig it was to turn.
		state, err := s.jobState(ctx, r)
		switch {
		case err != nil:
		case state == "running" || s.noteRigEnd(r, state, gen) == gen:
			out.FBX, out.Reskin = r.Detail["fbx"], state
		}
	}
	return out
}

func scalarOf(v any) string {
	s, _ := v.(string)
	return s
}

// AnimateInput is the input of animate_model.
type AnimateInput struct {
	TaskID      string
	Presets     []string // <category>/<name>
	Files       []string // own FBX animations (absolute paths)
	Format      string   // fbx | glb | usdz
	Resolution  string
	Material    string
	Height      *float64
	AlignGround *bool
	Expected    *float64
	Wait        bool
}

// maxAnimationFile is the web's limit for an own FBX animation (25 MiB).
const maxAnimationFile = 26214400

// AnimateModel is animate_model.
func (s *Service) AnimateModel(ctx context.Context, in AnimateInput) (Result, error) {
	if _, err := s.permit("animate_model"); err != nil {
		return Result{}, err
	}
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "glb" // the web's skeleton export default
	}
	if !oneOf(format, "fbx", "glb", "usdz") {
		return Result{}, bad(fmt.Sprintf("format %q is not fbx, glb or usdz.", in.Format), "Pass fbx, glb or usdz.")
	}
	for _, p := range in.Presets {
		if !catalog.IsAnimationPreset(p) {
			return Result{}, bad(fmt.Sprintf("%q is not an animation preset.", p), "list_animation_presets lists them as <category>/<name>, e.g. human/Walking.")
		}
	}
	var files []rodin.UploadImage
	for i, p := range in.Files {
		if !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".fbx") {
			return Result{}, bad(fmt.Sprintf("animation_files[%d] %q is not an absolute path of an .fbx file.", i, p), "Pass absolute paths of FBX animations.")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return Result{}, bad(fmt.Sprintf("animation_files[%d] cannot be read: %v.", i, err), "Pass the path of an existing FBX file.")
		}
		if len(data) == 0 || len(data) > maxAnimationFile {
			return Result{}, bad(fmt.Sprintf("animation_files[%d] is %s; an FBX animation must be non-empty and at most 25 MiB.", i, sizeText(int64(len(data)))), "Pass a smaller FBX animation.")
		}
		files = append(files, rodin.UploadImage{Name: uploadBase(p), MIME: "application/octet-stream", Data: data})
	}
	// animate_model works where rig_model does, once a rig exists.
	uuid, card, st, err := s.actionCardAs(ctx, in.TaskID, "animate_model", "rig_model")
	if err != nil {
		return Result{}, err
	}
	source, _ := rigSource(st)
	if _, err := s.client.RiggingResult(ctx, uuid, source); err != nil {
		if isCode(err, "RIGGING_NOT_FOUND") {
			return Result{}, fail(render.CodeConflict, "The task has no rig yet.", "Call rig_model first, then animate_model.")
		}
		return Result{}, wrapf(err, "read the task's rig")
	}
	// The web exports the material only from a textured task; otherwise the
	// rigged mesh alone, material "None" (main.fmt.js:39851-39858).
	typ, matWire := "mesh", "None"
	if st.Stage == stage.Material {
		material := strings.ToLower(strings.TrimSpace(in.Material))
		if material == "" {
			material = "all"
		}
		w, ok := map[string]string{"all": "All", "pbr": "PBR", "shaded": "Shaded"}[material]
		if !ok {
			return Result{}, bad(fmt.Sprintf("material %q is not all, pbr or shaded.", in.Material), "Pass one of the listed values.")
		}
		typ, matWire = "material", w
	} else if in.Material != "" {
		return Result{}, bad("material applies to a textured task (stage material).", "Leave material out.")
	}
	resolution := strings.ToLower(strings.TrimSpace(in.Resolution))
	if resolution == "" {
		resolution = "2k"
	}
	resWire, ok := map[string]string{"1k": "Basic", "2k": "Basic", "6k": "Basic", "4k": "High", "12k": "High"}[resolution]
	if !ok {
		return Result{}, bad(fmt.Sprintf("resolution %q is not one of 1k, 2k, 4k, 6k, 12k.", in.Resolution), "Pass 2k (Basic) unless the plan allows High (4k).")
	}
	if in.Height != nil && *in.Height <= 0 {
		return Result{}, bad("height must be above 0.", "Pass the real-world height, or leave it out.")
	}
	pin := price.Input{Operation: "animate_model", Format: format, Resolution: resolution, Material: strings.ToLower(matWire),
		Animations: len(in.Presets), AnimationFiles: len(files)}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "animate_model", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	if resWire == "High" && !sp.info.HasPrivilege("RodinPackHighResolution") {
		return Result{}, &price.Gate{Reason: "4K and 12K exports need the RodinPackHighResolution privilege (Business plan); use 2k"}
	}
	limit := 5
	if sp.info.HasPrivilege("GatewayContentGenerationForFree") {
		limit = 10
	}
	if n := len(in.Presets) + len(files); n > limit {
		return Result{}, bad(fmt.Sprintf("%d animations; this plan exports at most %d at a time.", n, limit), "Export them in batches.")
	}
	req := rodin.RigPackRequest{TaskUUID: uuid, Format: format, Resolution: resWire, Material: matWire, Type: typ, Height: in.Height,
		AlignGround: optTrue(in.AlignGround), Presets: in.Presets, Files: files}

	sp.open()
	raw, err := s.client.RiggingPack(ctx, req)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("animate_model", uuid, err)
	}
	jobs, _ := rodinJobs(raw)
	set, err := s.settle(ctx, uuid, st, jobs, in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	detail := map[string]any{"format": format, "type": typ, "material": matWire, "resolution": resWire, "presets": in.Presets}
	if len(in.Files) > 0 {
		detail["animation_files"] = in.Files
	}
	var names []string
	if set.status.State == stage.Packed {
		if items, err := s.client.GetDownload(ctx, uuid); err == nil {
			for _, it := range items {
				names = append(names, it.Name)
			}
			// An export in a format the task already has replaces the files of
			// those names (live 2026-10-27: a second glb export left one
			// base_basic_pbr.glb, holding the new animation), so the names
			// are the new export's.
			names = exportedFiles(names, format, false)
			detail["files"] = names
		}
	}
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Animation export requested (%d presets, %d own files, %s).", len(in.Presets), len(files), format), detail)
	if len(names) > 0 {
		res.Body += "\n\nExported files: " + strings.Join(names, ", ") + ". download_model with these as files saves them " +
			"(an export replaces the task's earlier files of the same name; files of other formats stay)."
	}
	return res, nil
}

var lodName = regexp.MustCompile(`(?i)^(lod|highpoly)([_.-]|$)`)

// exportedFiles picks the files of a rigging export from the task's file
// list the way the web does (main.fmt.js:149394): the files of the format,
// without LOD and high-poly ones, plus the texture images when textures is
// true. The web's export formats (fbx, glb, usdz) carry their textures, so
// the caller passes false.
func exportedFiles(names []string, format string, textures bool) []string {
	var out, tex []string
	for _, n := range names {
		if lodName.MatchString(path.Base(n)) {
			continue
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(n)), ".")
		switch {
		case ext == format:
			out = append(out, n)
		case textures && oneOf(ext, "mtl", "png", "jpg", "jpeg", "webp", "tga", "exr", "bmp", "tif", "tiff"):
			tex = append(tex, n)
		}
	}
	return append(out, tex...)
}

// uploadBase is the file name sent for a local file.
func uploadBase(p string) string {
	return safeName(filepath.Base(p))
}

// AnimationPresetsInput filters list_animation_presets.
type AnimationPresetsInput struct {
	Category string
	Template string // a rig template: its category
}

type presetsFront struct {
	Categories []catalog.AnimationCategory `yaml:"categories"`
	Templates  []catalog.RigTemplate       `yaml:"templates,omitempty"`
	Total      int                         `yaml:"total"`
}

// AnimationPresets is list_animation_presets: the web's bundled presets.
func (s *Service) AnimationPresets(in AnimationPresetsInput) (Result, error) {
	cat := strings.ToLower(strings.TrimSpace(in.Category))
	if t := strings.ToLower(strings.TrimSpace(in.Template)); t != "" {
		tm, ok := catalog.RigTemplateNamed(t)
		if !ok {
			return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("template %q is not human, quadruped, bird, dragon or kaiju.", in.Template), "Pass one of the listed templates.")
		}
		if cat != "" && cat != tm.Animations {
			return Result{}, fail(render.CodeInvalidInput, "Pass category or template, not both.", "template picks the category the web shows for that skeleton.")
		}
		cat = tm.Animations
	}
	front := presetsFront{}
	for _, c := range catalog.AnimationPresets {
		if cat == "" || c.Dir == cat {
			front.Categories = append(front.Categories, c)
			front.Total += len(c.Presets)
		}
	}
	if len(front.Categories) == 0 {
		var dirs []string
		for _, c := range catalog.AnimationPresets {
			dirs = append(dirs, c.Dir)
		}
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("category %q is not one of %s.", in.Category, strings.Join(dirs, ", ")), "Pass one of the listed categories, or none for all.")
	}
	if cat == "" {
		front.Templates = catalog.RigTemplates
	}
	body := fmt.Sprintf("%d animation presets in %d categories; pass them to animate_model as <category>/<name> (at most 10 per export).", front.Total, len(front.Categories))
	if cat == "" {
		body += " templates names the category the web shows first for each rig template."
	}
	return Result{Front: front, Body: body}, nil
}
