package service

import (
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
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
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
	Expected    *float64
	Wait        bool
}

type rigFront struct {
	Source   string `yaml:"source"`
	Template string `yaml:"template,omitempty"`
	Skinning string `yaml:"skinning,omitempty"`
	Bones    int    `yaml:"bones,omitempty"`
	State    string `yaml:"state"` // succeeded (rig_model) | done (get_task) | running | failed | canceled | none
}

// rigSettings builds the rodin_rigging settings of in for source.
func rigSettings(in RigInput, source string) (map[string]any, rigFront, error) {
	bad := func(msg, hint string) error { return fail(render.CodeInvalidInput, msg, hint) }
	tmpl := strings.ToLower(strings.TrimSpace(in.Template))
	skel, desc := strings.TrimSpace(in.Skeleton), strings.TrimSpace(in.Description)
	given := 0
	for _, v := range []string{tmpl, skel, desc} {
		if v != "" {
			given++
		}
	}
	if given > 1 {
		return nil, rigFront{}, bad("Pass one of template, skeleton or description.", "template picks a built-in skeleton; skeleton gives your own bone hierarchy; description lets Hyper3D plan the bones from words.")
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

// RigModel is rig_model.
func (s *Service) RigModel(ctx context.Context, in RigInput) (Result, error) {
	if _, err := s.permit("rig_model"); err != nil {
		return Result{}, err
	}
	uuid, card, st, err := s.actionCard(ctx, in.TaskID, "rig_model")
	if err != nil {
		return Result{}, err
	}
	source, _ := rigSource(st)
	settings, rig, err := rigSettings(in, source)
	if err != nil {
		return Result{}, err
	}
	pin := price.Input{Operation: "rig_model", RigTemplate: rig.Template, Skinning: rig.Skinning}
	pinFromCard(&pin, card)
	sp, err := s.prepare(ctx, "rig_model", uuid, pin, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	sp.open()
	reply, err := s.client.Rigging(ctx, uuid, settings)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("rig_model", uuid, err)
	}
	jr := jobsResult{TimedOut: true}
	if in.Wait {
		if jr, err = s.waitJobs(ctx, reply.Job.UUIDs, time.Now().Add(FlowWait)); err != nil {
			sp.close(ctx, nil)
			return Result{}, wrapf(err, "the rigging was started; get_task shows when it is done")
		}
	}
	sp.close(ctx, nil)
	rig.State = jobWord(jr)
	if jr.Done && jr.Status == "Succeeded" {
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
	front := flowFront{Task: s.taskFront(card2), Charge: sp.front(), TimedOut: !jr.Done, Extra: map[string]any{"rig": rig}}
	if rig.State == "succeeded" && registered("animate_model") {
		front.Task.Actions = append(front.Task.Actions, "animate_model")
	}
	var b strings.Builder
	switch rig.State {
	case "succeeded":
		fmt.Fprintf(&b, "Rigged with the %s skeleton (%s skinning).", rig.Template, rig.Skinning)
	case "running":
		b.WriteString("Rigging started and is still running.")
	default:
		fmt.Fprintf(&b, "Rigging ended %s.", rig.State)
	}
	b.WriteString("\n\n" + sp.walletText())
	switch rig.State {
	case "succeeded":
		t, _ := catalog.RigTemplateNamed(rig.Template)
		next := "\n\nNext: animate_model exports the rigged model, with animations or without (list_animation_presets lists them"
		if t.Animations != "" {
			next += "; category " + t.Animations + " fits this skeleton"
		}
		b.WriteString(next + "); download_model then saves the files.")
	case "running":
		b.WriteString("\n\nNext: in a minute or two, get_task with " + uuid + ": its rig block says when the rig is done. Do not call rig_model again for it.")
	}
	return Result{Front: front, Body: b.String()}, nil
}

// rigState reads the rig of a task at status st for get_task: nil when the
// stage has no rig source.
func (s *Service) rigState(ctx context.Context, uuid string, st stage.Status) *rigFront {
	source, ok := rigSource(st)
	if !ok || st.State == stage.Running {
		return nil
	}
	res, err := s.client.RiggingResult(ctx, uuid, source)
	switch {
	case isCode(err, "RIGGING_NOT_FOUND"):
		return &rigFront{Source: source, State: "none"}
	case err != nil:
		return nil
	}
	return &rigFront{Source: source, Template: res.Template, Skinning: scalarOf(res.Raw["skinning_method"]), State: "done"}
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
			names = exportedFiles(names, format, false)
			detail["files"] = names
		}
	}
	res := s.finishFlow(ctx, sp, set, fmt.Sprintf("Animation export requested (%d presets, %d own files, %s).", len(in.Presets), len(files), format), detail)
	if len(names) > 0 {
		res.Body += "\n\nExported files: " + strings.Join(names, ", ") + ". download_model with these as files saves them " +
			"(the task's file list also holds its earlier packs)."
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
