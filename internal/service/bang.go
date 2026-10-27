package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Bang (split into parts) works on a geometry draft version of the same task
// (notes 14 section 5; web call sites main.fmt.js:14416-14600 and
// 29948-30160): bang_plan lays out the parts (Strength = the part count),
// its picture is read with bang_plan_result, and bang_generate ("Bang to
// Parts") splits the draft by that plan into a Bang version of the draft
// (bang_history). confirm_geometry with bang_version then confirms the
// parts. The jobs are followed on the scheduler: the draft's card stays a
// draft.

// SplitInput is the input of split_parts.
type SplitInput struct {
	TaskID      string
	Version     string // geometry draft version; default the newest
	Strength    int    // 1-12; 0 is the web's 5
	Step        string // both (default) | plan | generate
	BangVersion string // split one part of this Bang result again
	Part        string // the part (bang_component_id) of BangVersion
	Expected    *float64
	Wait        bool
}

type bangPart struct {
	Version   string   `yaml:"bang_version"`
	Generated string   `yaml:"generated,omitempty"`
	Parts     []string `yaml:"parts,omitempty,flow"`
	File      string   `yaml:"file,omitempty"`     // where the parts file was saved
	Download  string   `yaml:"download,omitempty"` // the name download_model saves it by
}

type splitDetail struct {
	Version   string    `yaml:"geometry_version"`
	Strength  int       `yaml:"strength,omitempty"` // 0: the plan of step generate was made elsewhere, strength not known
	Step      string    `yaml:"step"`
	Part      string    `yaml:"part,omitempty"`
	Plan      string    `yaml:"plan"`             // succeeded | running | failed | canceled | existing
	Split     string    `yaml:"split,omitempty"`  // succeeded | running | failed | canceled
	Result    *bangPart `yaml:"result,omitempty"` // the new Bang version
	TimedOut  bool      `yaml:"-"`
	PlanImage bool      `yaml:"-"`
}

// SplitParts is split_parts.
func (s *Service) SplitParts(ctx context.Context, in SplitInput) (Result, error) {
	if _, err := s.permit("split_parts"); err != nil {
		return Result{}, err
	}
	step := strings.ToLower(strings.TrimSpace(in.Step))
	if step == "" {
		step = "both"
	}
	if !oneOf(step, "both", "plan", "generate") {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("step %q is not both, plan or generate.", in.Step),
			"Pass plan (lay out the parts and show the plan), generate (split by the existing plan) or both.")
	}
	if in.Strength < 0 || in.Strength > 12 {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("strength %d is not 1-12.", in.Strength), "Pass the number of parts to aim for, 1-12 (default 5).")
	}
	part, bangVer := strings.TrimSpace(in.Part), strings.TrimSpace(in.BangVersion)
	if part != "" && bangVer == "" {
		return Result{}, fail(render.CodeInvalidInput, "part needs bang_version: the Bang result the part belongs to.", "get_task lists the Bang results with their parts.")
	}
	if bangVer != "" && part == "" {
		return Result{}, fail(render.CodeInvalidInput, "bang_version needs part: split_parts splits one part of an earlier Bang result again.",
			"Pass part (a part name of that result; get_task lists them), or leave bang_version out to split the whole draft.")
	}
	uuid, card, _, err := s.actionCard(ctx, in.TaskID, "split_parts")
	if err != nil {
		return Result{}, err
	}
	hist, err := s.client.GeometryHistory(ctx, uuid)
	if err != nil {
		return Result{}, wrapf(err, "read the geometry versions")
	}
	version, err := pickVersion(in.Version, keysOf(hist), "geometry")
	if err != nil {
		return Result{}, err
	}
	strength, planUnknown, err := s.splitStrength(uuid, version, step, in.Strength)
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{BangStep: step, Strength: strength, BangPart: part != "", PlanStrengthUnknown: planUnknown}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "split_parts", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	req := rodin.BangRequest{UUID: uuid, ModelVersion: version, BangVersion: bangVer, ComponentID: part}
	if part != "" {
		bh, err := s.client.BangHistory(ctx, uuid, version)
		if err != nil {
			return Result{}, wrapf(err, "read the Bang results of %s", version)
		}
		if _, ok := bh[bangVer]; !ok {
			return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("Geometry %s has no Bang result %q.", version, bangVer), "get_task lists the Bang results with their parts.")
		}
	}
	if step != "plan" {
		if err := s.splitBusy(ctx, uuid, version); err != nil {
			return Result{}, err
		}
	}
	if step == "generate" {
		if err := s.planFits(ctx, uuid, version, bangVer, part); err != nil {
			return Result{}, err
		}
		if _, err := s.client.BangPlanResult(ctx, req); err != nil {
			if isCode(err, "NO_SUCH_A_PLAN") {
				return Result{}, fail(render.CodeConflict, fmt.Sprintf("Geometry %s has no Bang plan yet.", version), "Call split_parts with step plan (or both) first.")
			}
			return Result{}, wrapf(err, "read the Bang plan")
		}
	}

	detail := splitDetail{Version: version, Strength: strength, Step: step, Part: part}
	deadline := time.Now().Add(FlowWait)
	// wait false returns right after the request of step plan or generate;
	// step both needs the finished plan before it can split, so it waits.
	wait := in.Wait || step == "both"
	var images []Image
	// Each call is recorded before it is sent, in one store step with the
	// check that no split (nor, before a split, a plan) of the version runs:
	// two sessions cannot both pass splitBusy or planFits and send.
	var c claim
	var planID int64
	if step == "generate" {
		var busy *store.Job
		if c, busy, err = s.claimJob(ctx, uuid, jobSplit, version, nil, splitKinds, 0); err != nil {
			return Result{}, err
		}
		if busy != nil {
			return Result{}, s.splitRunning(uuid, version, *busy)
		}
	} else {
		// A plan is blocked by nothing (as before the record): it is
		// recorded first so a split in another session sees it running.
		if c, _, err = s.claimJob(ctx, uuid, jobPlan, version, map[string]string{"bang_version": bangVer, "part": part, "strength": strconv.Itoa(strength)}, nil, 0); err != nil {
			return Result{}, err
		}
	}
	sp.open()
	if step == "generate" {
		detail.Plan = "existing"
	} else {
		reply, err := s.client.BangPlan(ctx, req, strength)
		if err != nil {
			c.drop()
			sp.close(ctx, err)
			return Result{}, sendFailure("split_parts (bang_plan)", uuid, err)
		}
		id := c.fill(reply.UUIDs(), nil)
		planID = id
		jr := jobsResult{}
		if wait {
			if jr, err = s.waitJobs(ctx, reply.UUIDs(), deadline); err != nil {
				sp.close(ctx, nil)
				return Result{}, wrapf(err, "the Bang plan was started; get_task with %s shows it (bang_plan) once it is made, then split_parts with step generate splits by it", uuid)
			}
			s.endJobs(id, jr)
		}
		detail.Plan = jobWord(jr)
		if jr.Done && jr.Status == "Succeeded" {
			if img, ok := s.planImage(ctx, req); ok {
				images = append(images, img)
				detail.PlanImage = true
			}
		}
		if !jr.Done || jr.Status != "Succeeded" || step == "plan" {
			detail.TimedOut = jr.TimedOut
			sp.close(ctx, nil)
			return s.finishSplit(ctx, sp, uuid, detail, images), nil
		}
		// Step both: the split is claimed after its own plan ended.
		var busy *store.Job
		if c, busy, err = s.claimJob(ctx, uuid, jobSplit, version, nil, splitKinds, planID); err != nil || busy != nil {
			sp.close(ctx, nil)
			if err == nil {
				err = s.splitRunning(uuid, version, *busy)
			}
			e := Classify(err)
			return Result{}, failFields(e.Code, fmt.Sprintf("The Bang plan of geometry %s was made, but the split was not sent: %s", version, e.Message), e.Hint, e.Fields)
		}
	}
	reply, err := s.client.BangGenerate(ctx, uuid, version)
	if err != nil {
		c.drop()
		sp.close(ctx, err)
		return Result{}, sendFailure("split_parts (bang_generate)", uuid, err)
	}
	detail.Result = &bangPart{Version: reply.Version}
	id := c.fill(reply.UUIDs(), map[string]string{"result": reply.Version})
	jr := jobsResult{}
	if wait {
		if jr, err = s.waitJobs(ctx, reply.UUIDs(), deadline); err != nil {
			sp.close(ctx, nil)
			return Result{}, wrapf(err, "the split was started (Bang version %s); follow it with get_task", reply.Version)
		}
		s.endJobs(id, jr)
	}
	sp.close(ctx, nil)
	detail.Split, detail.TimedOut = jobWord(jr), jr.TimedOut
	if jr.Done && jr.Status == "Succeeded" {
		if bp, img, err := s.bangResult(ctx, uuid, card.Prompt, version, reply.Version); err == nil {
			detail.Result = bp
			if img != nil {
				images = append(images, *img)
			}
		}
	}
	return s.finishSplit(ctx, sp, uuid, detail, images), nil
}

// splitStrength is the strength split_parts sends, quotes and reports for
// step on geometry version of task uuid, given the strength passed (0: none).
// Step generate splits by the plan already made, so its strength is the
// plan's: the newest plan split_parts recorded on the version (a different
// strength passed is refused), else (a plan made elsewhere, or no task) the
// strength passed, else not known (unknown true, strength 0). The other
// steps make a plan at the strength passed, or the web slider's 5.
func (s *Service) splitStrength(uuid, version, step string, given int) (strength int, unknown bool, err error) {
	if step != "generate" {
		if given == 0 {
			return 5, false, nil
		}
		return given, false, nil
	}
	if uuid != "" {
		if j, ok := s.latestJob(uuid, jobPlan, version); ok {
			ps, _ := strconv.Atoi(j.Detail["strength"])
			if given != 0 && given != ps {
				return 0, false, fail(render.CodeInvalidInput, fmt.Sprintf("strength %d is not the strength of the plan the split follows: split_parts made the newest plan of geometry %s at strength %d (sent at %s).", given, version, ps, sentAt(j)),
					"Leave strength out for step generate, or make a new plan with step plan (or both) at the strength wanted.")
			}
			return ps, false, nil
		}
	}
	if given == 0 {
		return 0, true, nil
	}
	return given, false, nil
}

func isCode(err error, code string) bool {
	var se *rodin.ServiceError
	return errors.As(err, &se) && se.Code == code
}

func jobWord(jr jobsResult) string {
	if !jr.Done {
		return "running"
	}
	return strings.ToLower(jr.Status) // succeeded | failed | canceled
}

// planOf names what a plan or split is of, for the texts.
func planOf(bangVer, part string) string {
	if part == "" {
		return "the whole model"
	}
	return "part " + part + " of Bang result " + bangVer
}

// splitBusy refuses a split while a split split_parts sent on the same
// draft version still runs (the web's panel takes one at a time; a second
// call would repeat a paid request).
func (s *Service) splitBusy(ctx context.Context, uuid, version string) error {
	j, ok := s.latestJob(uuid, jobSplit, version)
	if !ok {
		return nil
	}
	state, err := s.jobState(ctx, j)
	if err != nil {
		return wrapf(err, "read the state of the split sent at %s", sentAt(j))
	}
	if state != "running" {
		return nil
	}
	return s.splitRunning(uuid, version, j)
}

// splitKinds are the calls that block a split of a version: a split, and a
// plan (the split would follow whichever plan the server takes; planFits).
var splitKinds = []string{jobSplit, jobPlan}

// splitRunning is the refusal of a split while the recorded call j (a
// split, or a plan) of the version runs.
func (s *Service) splitRunning(uuid, version string, j store.Job) error {
	if j.Kind == jobPlan {
		return failFields(render.CodeConflict, fmt.Sprintf("The Bang plan of geometry %s that split_parts sent at %s is still running.", version, sentAt(j)),
			"In a minute, get_task with "+uuid+": its bang_plan block says when the plan is made and attaches its picture; then split_parts with step generate splits by it.",
			map[string]any{"geometry_version": version, "plan_of": planOf(j.Detail["bang_version"], j.Detail["part"]), "plan_state": "running"})
	}
	what := ""
	if r := j.Detail["result"]; r != "" {
		what = " (Bang version " + r + ")"
	}
	return failFields(render.CodeConflict, fmt.Sprintf("The split of geometry %s that split_parts sent at %s%s is still running.", version, sentAt(j), what),
		"In a minute or two, get_task with "+uuid+": its bang block lists the result once it is done. Do not call split_parts again for it.",
		map[string]any{"geometry_version": version, "bang_version": j.Detail["result"]})
}

// planFits checks, for step generate, the newest plan split_parts sent on
// the version: it must be made, and of the same part. bang_generate names
// no part, so when a version has plans of several parts the server picks
// the plan it splits by (not settled; the web always splits right after its
// own plan); a split is sent only after a plan of the part it is quoted
// and reported for. A plan made elsewhere (the web) is not recorded here:
// then only bang_plan_result's answer for the part is checked.
func (s *Service) planFits(ctx context.Context, uuid, version, bangVer, part string) error {
	j, ok := s.latestJob(uuid, jobPlan, version)
	if !ok {
		return nil
	}
	state, err := s.jobState(ctx, j)
	if err != nil {
		return wrapf(err, "read the state of the Bang plan sent at %s", sentAt(j))
	}
	fields := map[string]any{"geometry_version": version, "plan_of": planOf(j.Detail["bang_version"], j.Detail["part"]), "plan_state": state}
	switch {
	case state == "running":
		return s.splitRunning(uuid, version, j)
	case state != "succeeded":
		return failFields(render.CodeConflict, fmt.Sprintf("The newest Bang plan of geometry %s that split_parts sent (at %s) ended %s.", version, sentAt(j), state),
			"split_parts with step plan (or both) makes a new plan.", fields)
	case j.Detail["part"] != part || j.Detail["bang_version"] != bangVer:
		return failFields(render.CodeConflict, fmt.Sprintf("The newest Bang plan of geometry %s that split_parts sent is of %s, not of %s.", version, planOf(j.Detail["bang_version"], j.Detail["part"]), planOf(bangVer, part)),
			"bang_generate names no part, so which plan a split follows is the server's choice: pass the plan's part and bang_version, or split_parts with step both (or step plan, then generate) for "+planOf(bangVer, part)+".", fields)
	}
	return nil
}

// planFront is get_task's bang_plan block: the newest Bang plan split_parts
// sent on the task, while no split of its version was sent after it.
type planFront struct {
	Version     string `yaml:"geometry_version"`
	BangVersion string `yaml:"bang_version,omitempty"`
	Part        string `yaml:"part,omitempty"`
	Strength    int    `yaml:"strength,omitempty"`
	State       string `yaml:"state"` // running | succeeded | failed | canceled
	Sent        string `yaml:"sent"`
}

// planState reads the plan get_task shows, with its picture once it is made.
func (s *Service) planState(ctx context.Context, uuid string) (*planFront, *Image) {
	j, ok := s.latestJob(uuid, jobPlan, "")
	if !ok {
		return nil, nil
	}
	if split, ok := s.latestJob(uuid, jobSplit, j.Key); ok && !split.At.Before(j.At) {
		return nil, nil // split already: the bang block lists the result
	}
	state, err := s.jobState(ctx, j)
	if err != nil {
		return nil, nil
	}
	strength, _ := strconv.Atoi(j.Detail["strength"])
	p := &planFront{Version: j.Key, BangVersion: j.Detail["bang_version"], Part: j.Detail["part"], Strength: strength,
		State: state, Sent: j.At.UTC().Format(time.RFC3339)}
	if state != "succeeded" {
		return p, nil
	}
	img, ok := s.planImage(ctx, rodin.BangRequest{UUID: uuid, ModelVersion: j.Key, BangVersion: p.BangVersion, ComponentID: p.Part})
	if !ok {
		return p, nil
	}
	return p, &img
}

// planImage reads the plan's picture as a PNG.
func (s *Service) planImage(ctx context.Context, req rodin.BangRequest) (Image, bool) {
	u, err := s.client.BangPlanResult(ctx, req)
	if err != nil || u == "" {
		return Image{}, false
	}
	var buf bytes.Buffer
	if _, _, err := s.client.Fetch(ctx, u, &buf, 64<<20); err != nil {
		return Image{}, false
	}
	png, _, _, err := imaging.ToPNG(buf.Bytes(), 1024)
	if err != nil {
		return Image{}, false
	}
	return Image{PNG: png, What: "Bang plan"}, true
}

// bangResult reads Bang version bv of geometry version gv: its parts file is
// saved beside the task's downloads, its part names read and a render of it
// made.
func (s *Service) bangResult(ctx context.Context, uuid, prompt, gv, bv string) (*bangPart, *Image, error) {
	bh, err := s.client.BangHistory(ctx, uuid, gv)
	if err != nil {
		return nil, nil, err
	}
	entry, ok := bh[bv]
	if !ok {
		return &bangPart{Version: bv}, nil, fmt.Errorf("bang_history of %s has no version %s", gv, bv)
	}
	out := &bangPart{Version: bv, Generated: entry.GenerationTime}
	if entry.Model == "" {
		return out, nil, nil
	}
	out.Download = bangFileName(gv, bv)
	target := filepath.Join(s.downloadDir(uuid, prompt), bangFileName(gv, bv))
	if _, err := s.saveResultFile(ctx, uuid, entry.Model, target); err != nil {
		return out, nil, err
	}
	out.File = target
	if names, err := bangPartNames(target); err == nil {
		out.Parts = names
	}
	png, err := s.renderModelFile(ctx, target, render3d.ModeClassic, render3d.ViewThreeQuarter, 512)
	if err != nil {
		return out, nil, nil
	}
	return out, &Image{PNG: png, What: "Bang parts " + bv}, nil
}

// bangGroups returns model_multi_udim for confirming Bang result bv of
// geometry version gv: the web's default UV grouping, one group holding
// every part (cf/7859 22545-22581: a part not in a saved group joins group
// 0), or no group for a single part (main.fmt.js:28391).
func (s *Service) bangGroups(ctx context.Context, uuid, prompt, gv, bv string) (any, error) {
	if regexpVersion.MatchString(bv) {
		return nil, fail(render.CodeInvalidInput, fmt.Sprintf("bang_version %q is a geometry version, not a Bang result.", bv), "get_task lists the Bang results of each draft version.")
	}
	bh, err := s.client.BangHistory(ctx, uuid, gv)
	if err != nil {
		return nil, wrapf(err, "read the Bang results of %s", gv)
	}
	entry, ok := bh[bv]
	if !ok {
		return nil, fail(render.CodeInvalidInput, fmt.Sprintf("Geometry %s has no Bang result %q.", gv, bv),
			"get_task lists the Bang results of each draft version; pass version for the draft version it belongs to.")
	}
	if entry.Model == "" {
		return nil, fail(render.CodeConflict, fmt.Sprintf("Bang result %s lists no parts file yet.", bv), "Call get_task in a minute, then confirm again.")
	}
	target := filepath.Join(s.downloadDir(uuid, prompt), bangFileName(gv, bv))
	if _, err := s.saveResultFile(ctx, uuid, entry.Model, target); err != nil {
		return nil, wrapf(err, "read the parts of Bang result %s", bv)
	}
	names, err := bangPartNames(target)
	if err != nil {
		return nil, wrapf(err, "read the parts of Bang result %s", bv)
	}
	if len(names) < 2 {
		return []any{}, nil
	}
	return [][]string{names}, nil
}

var regexpVersion = regexp.MustCompile(`(?i)^v\d+$`)

// bangPartNames reads the part names of a Bang parts GLB.
func bangPartNames(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := glb.Parse(data)
	if err != nil {
		return nil, err
	}
	return f.MeshNodeNames(), nil
}

// finishSplit builds the reply of split_parts.
func (s *Service) finishSplit(ctx context.Context, sp *spend, uuid string, d splitDetail, images []Image) Result {
	card, err := s.client.Card(ctx, uuid)
	var front flowFront
	if err == nil {
		s.saveCard(card)
		front.Task = s.taskFront(card)
	}
	front.Charge, front.TimedOut, front.Extra = sp.front(), d.TimedOut, d
	same := "the same version"
	if d.Part != "" {
		same = "the same version, bang_version and part"
	}
	var b strings.Builder
	switch {
	case d.Plan == "running" && d.TimedOut:
		fmt.Fprintf(&b, "The Bang plan of geometry %s is still running after %d seconds.", d.Version, int(FlowWait.Seconds()))
	case d.Plan == "running":
		fmt.Fprintf(&b, "The Bang plan of geometry %s was sent and is running.", d.Version)
	case d.Plan != "succeeded" && d.Plan != "existing":
		fmt.Fprintf(&b, "The Bang plan of geometry %s ended %s.", d.Version, d.Plan)
	case d.Step == "plan":
		fmt.Fprintf(&b, "Bang plan of geometry %s made (strength %d).", d.Version, d.Strength)
		if d.PlanImage {
			b.WriteString(" Its picture is attached: the planned parts drawn apart in boxes, from the front and the top.")
		} else {
			b.WriteString(" Its picture could not be read now; get_task with " + uuid + " attaches it (bang_plan).")
		}
	case d.Split == "running":
		fmt.Fprintf(&b, "The split of geometry %s into parts is running (Bang version %s).", d.Version, d.Result.Version)
	case d.Split == "succeeded":
		fmt.Fprintf(&b, "Geometry %s split into parts: Bang version %s", d.Version, d.Result.Version)
		if n := len(d.Result.Parts); n > 0 {
			fmt.Fprintf(&b, " with %d parts", n)
		}
		b.WriteString(".")
		if d.Result.File != "" {
			b.WriteString(" The parts file is saved as " + d.Result.File + " (show_in_viewer model_file shows it).")
		}
	default:
		fmt.Fprintf(&b, "The split of geometry %s ended %s.", d.Version, d.Split)
	}
	b.WriteString("\n\n" + sp.walletText())
	switch {
	case d.Plan == "running":
		// No split was sent: the plan comes first.
		b.WriteString("\n\nNext: in a minute, get_task with " + uuid + ": its bang_plan block says when the plan is made and attaches its picture for the user " +
			"to approve; then split_parts with step generate (and " + same + ") splits the draft by it. No split was sent.")
	case d.Split == "running":
		b.WriteString("\n\nNext: in a minute or two, get_task with " + uuid + ": its bang block lists the results of each draft version once they are done, " +
			"with the name download_model saves the parts file by. Do not call split_parts again for this split.")
	case d.Step == "plan" && d.Plan == "succeeded":
		b.WriteString("\n\nNext: split_parts with step generate (and " + same + ") splits the draft by this plan; step plan with another strength makes a new plan.")
	case d.Split == "succeeded":
		fmt.Fprintf(&b, "\n\nNext: confirm_geometry with bang_version %s confirms the draft as these parts; split_parts again (another strength, or bang_version and part) makes another result.", d.Result.Version)
	}
	return Result{Front: front, Body: b.String(), Images: images}
}

// bangVersions lists the Bang results of every geometry version that has
// one (get_task), newest first per version.
func (s *Service) bangVersions(ctx context.Context, uuid string, hist map[string]rodin.GeometryVersion) []bangListFront {
	var out []bangListFront
	for _, v := range sortedKeys(hist) {
		if !hist[v].HasBang {
			continue
		}
		bh, err := s.client.BangHistory(ctx, uuid, v)
		if err != nil {
			continue
		}
		row := bangListFront{Version: v}
		for bv, e := range bh {
			p := bangPart{Version: bv, Generated: e.GenerationTime}
			if e.Model != "" {
				p.Download = bangFileName(v, bv)
			}
			row.Results = append(row.Results, p)
		}
		sort.Slice(row.Results, func(i, j int) bool { return row.Results[i].Generated > row.Results[j].Generated })
		out = append(out, row)
	}
	return out
}

type bangListFront struct {
	Version string     `yaml:"geometry_version"`
	Results []bangPart `yaml:"results"`
}

func sortedKeys[V any](m map[string]V) []string {
	k := keysOf(m)
	sort.Strings(k)
	return k
}
