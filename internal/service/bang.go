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
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/glb"
	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
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
	Strength  int       `yaml:"strength"`
	Step      string    `yaml:"step"`
	Part      string    `yaml:"part,omitempty"`
	Plan      string    `yaml:"plan"`             // done | running | failed | canceled | existing
	Split     string    `yaml:"split,omitempty"`  // done | running | failed | canceled
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
	strength := in.Strength
	if strength == 0 {
		strength = 5 // the web slider's default
	}
	if strength < 1 || strength > 12 {
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
	pin := price.Input{BangStep: step, Strength: strength, BangPart: part != ""}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "split_parts", uuid, pin, in.Expected, true)
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
	if step == "generate" {
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
	sp.open()
	if step == "generate" {
		detail.Plan = "existing"
	} else {
		reply, err := s.client.BangPlan(ctx, req, strength)
		if err != nil {
			sp.close(ctx, err)
			return Result{}, sendFailure("split_parts (bang_plan)", uuid, err)
		}
		jr := jobsResult{}
		if wait {
			if jr, err = s.waitJobs(ctx, reply.UUIDs(), deadline); err != nil {
				sp.close(ctx, nil)
				return Result{}, wrapf(err, "the Bang plan was started; split_parts with step generate splits by it once it is made")
			}
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
	}
	reply, err := s.client.BangGenerate(ctx, uuid, version)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("split_parts (bang_generate)", uuid, err)
	}
	detail.Result = &bangPart{Version: reply.Version}
	jr := jobsResult{}
	if wait {
		if jr, err = s.waitJobs(ctx, reply.UUIDs(), deadline); err != nil {
			sp.close(ctx, nil)
			return Result{}, wrapf(err, "the split was started (Bang version %s); follow it with get_task", reply.Version)
		}
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
		// No split was sent: the plan comes first, and get_task does not
		// show a plan.
		b.WriteString("\n\nNext: in a minute, split_parts with step generate (and " + same + ") splits the draft by this plan; " +
			"it answers conflict when the draft has no plan yet. No split was sent.")
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
