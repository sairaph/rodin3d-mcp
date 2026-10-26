// Package price quotes what an operation costs. The web app has no price
// function: every cost it shows is a fixed button label, and the server
// decides and freezes the real amount (notes 14, section 4). A quote is
// known only where a web label names the price for that exact case or a
// wallet delta of that exact case was recorded (notes 35); everything else
// says it is not known, so expected_credits never accepts a price on a
// guess.
package price

import (
	"fmt"
	"strings"
	"time"
)

// Operations quote_cost accepts, each with the [permission] key that allows it.
var Operations = []struct{ Name, Permission string }{
	{"generate_model", "generate"},
	{"redo_geometry", "redo"},
	{"confirm_geometry", "confirm_geometry"},
	{"generate_texture", "generate_texture"},
	{"redo_material", "redo"},
	{"confirm_material", "confirm_material"},
	{"export_model", "export"},
	{"fork_task", "fork"},
	{"select_candidate", "redo"},
	{"generate_agentic", "agentic"},
	{"split_parts", "split_parts"},
	{"rig_model", "rig"},
	{"animate_model", "animate"},
	{"texture_model", "texture_model"},
}

// PermissionKey returns the [permission] key of operation.
func PermissionKey(operation string) (string, bool) {
	for _, o := range Operations {
		if o.Name == operation {
			return o.Permission, true
		}
	}
	return "", false
}

// Input is what a quote depends on. Empty fields are unknown or the tool's
// default; Task fills the ones a task's card answers.
type Input struct {
	Operation    string
	Model        string // gen-2.5 (default) | gen-1.5
	Mode         string // gen-1.5 mode: default focal zero speedy
	Tier         string // extreme-low low medium high extreme-high (Gen-2.5)
	TextureMode  string // extreme-low .. extreme-high, legacy (Gen-1.5)
	SmartLowPoly bool
	Mesh         string // confirm_geometry: raw | quad; empty: DefaultMesh
	// generate_texture and redo_material options as the call sends them
	// (generate_texture: unset is off; redo_material: unset is the draft's).
	HD, Sharp, FaceEnhance, Delight bool
	PackType                        string // export_model: mesh | material
	Resolution                      string // 1k 2k 4k 6k 12k (pack resolutions; 8K is the sharp_texture option)
	Geometry                        string // base (default) | lod | high_poly
	Format                          string // obj (default) fbx glb usdz stl
	Material                        string // all (default) | pbr | shaded (material packs)
	Budget                          string // basic standard pro (Agentic)
	Batch                           int
	ForkStage                       string // geometry | material (fork_task)
	// BangStep is the part of split_parts the call runs: plan, generate or
	// both (empty).
	BangStep string
	// Strength is split_parts' part count (1-12; 0 is the web's 5).
	Strength int
	// RigTemplate is rig_model's template ("" is human; custom for an own
	// skeleton or a description).
	RigTemplate string
	// Animations is animate_model's number of presets and own files.
	Animations int
	// TaskCreated is the creation time of the task an operation acts on, for
	// the 30-day fork rule; zero when not known.
	TaskCreated time.Time
}

// Quote is the price of one call.
type Quote struct {
	Credits *float64 `yaml:"credits"` // nil: not known
	Known   bool     `yaml:"known"`   // Credits is set
	// Measured is true when a wallet delta of this exact case was recorded;
	// a known quote that is not measured rests on the web's label alone.
	Measured   bool   `yaml:"measured"`
	ChargedAt  string `yaml:"charged_at,omitempty"`  // when the amount is taken
	FreeReason string `yaml:"free_reason,omitempty"` // why it costs nothing
	Basis      string `yaml:"basis"`                 // where the number comes from
	// FrozenAtSubmit is an amount the server was seen to freeze at submit
	// although the label says free; nil when none was measured.
	FrozenAtSubmit *float64 `yaml:"frozen_at_submit,omitempty"`
}

// Gate is a plan gate that refuses the operation for this account.
type Gate struct {
	Reason string
}

func (g *Gate) Error() string { return g.Reason }

// InvalidInput is an input the quote cannot be made for.
type InvalidInput struct{ Reason string }

func (e *InvalidInput) Error() string { return e.Reason }

// Account is what the gates read from user/get_info.
type Account struct {
	Privileges []string
	Plan       string // subscriptions.highest_level, e.g. Creator
}

func (a Account) has(p string) bool {
	for _, x := range a.Privileges {
		if x == p {
			return true
		}
	}
	return false
}

// DefaultMesh is the mesh confirm_geometry uses when none is given: raw
// (Triangle) when the plan allows it, as every confirmed task in the
// owner's history is, else quad.
func DefaultMesh(acct Account) string {
	if acct.has("RodinMeshAdditionalFeatures") {
		return "raw"
	}
	return "quad"
}

func credits(v float64) *float64 { return &v }

// label is a price the web's label names and no wallet delta of a call of
// this tool confirmed.
func label(c float64, chargedAt, basis string) Quote {
	return Quote{Credits: credits(c), Known: true, ChargedAt: chargedAt, Basis: basis + " (the web's label; not measured through this tool)"}
}

// measured is a price a recorded wallet delta of this exact case shows.
func measured(c float64, chargedAt, basis string) Quote {
	return Quote{Credits: credits(c), Known: true, Measured: true, ChargedAt: chargedAt, Basis: basis}
}

func unknown(why string) Quote {
	return Quote{Known: false, Basis: why}
}

var tiers = []string{"extreme-low", "low", "medium", "high", "extreme-high"}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// task names the model and tier of a quote for the texts.
func task(model, tier string) string {
	if tier == "" {
		return model
	}
	return model + " " + tier
}

// For returns the quote of in for account, a *Gate when the account's plan
// refuses the operation, or an *InvalidInput.
func For(in Input, acct Account) (Quote, error) {
	model := strings.ToLower(in.Model)
	if model == "" {
		model = "gen-2.5"
	}
	tier := strings.ToLower(in.Tier)
	switch in.Operation {
	case "generate_model":
		return generate(in, acct)
	case "redo_geometry":
		if tier == "extreme-high" {
			return label(2, "submit", `web hover label "2 Credits" on Redo of an extreme-high draft`), nil
		}
		const redoLabel = `the web shows Redo with its counter and no price on extreme-low and high drafts, and its tip says "Redos are free, but confirmation costs credits"`
		var q Quote
		if model == "gen-2.5" && tier == "extreme-low" {
			q = measured(0, "", "measured free on a gen-2.5 extreme-low draft (2026-10-26); "+redoLabel)
		} else {
			q = label(0, "", redoLabel+"; a geometry redo was measured free only on a gen-2.5 extreme-low draft, not on "+task(model, tier))
		}
		q.FreeReason = "geometry redos are free below the extreme-high tier; the plan limits their number (redo_left)"
		return q, nil
	case "redo_material":
		mode := strings.ToLower(in.TextureMode)
		if mode == "extreme-high" {
			return label(2, "submit", `web hover label "2 Credits" on material Redo at the extreme-high texture tier`), nil
		}
		// The web shows no price on material Redo below extreme-high (its
		// generic tip names no tier), so only the measured case is known.
		if model == "gen-2.5" && tier == "high" && mode == "high" && textureOptions(in) == "face_enhance" {
			q := measured(0, "", "wallet delta of a material redo at texture_mode high with face_enhance on a gen-2.5 high task, 2026-10-26")
			q.FreeReason = "this material redo took no credits"
			return q, nil
		}
		return unknown(fmt.Sprintf("the web shows no price on material Redo below the extreme-high texture tier, and a material redo was measured only at texture_mode high with face_enhance on a gen-2.5 high task (free); this is texture_mode %s with %s on a %s task",
			orNone(mode), textureOptions(in), task(model, tier))), nil
	case "select_candidate":
		var q Quote
		if model == "gen-2.5" && tier == "extreme-low" {
			q = measured(0, "", "measured free on a gen-2.5 extreme-low draft (2026-10-26); the web's batch view shows no price for the pick")
		} else {
			q = Quote{Credits: credits(0), Known: true, Basis: "not measured on " + task(model, tier) +
				" (measured free only on a gen-2.5 extreme-low draft); the web's batch view shows no price for the pick, which only chooses among drafts already made"}
		}
		q.FreeReason = "picking one draft of a batch changes nothing that is charged"
		return q, nil
	case "confirm_geometry":
		return confirmGeometry(in, acct, model, tier)
	case "generate_texture":
		return texture(in, model, tier)
	case "confirm_material":
		mode := strings.ToLower(in.TextureMode)
		if model == "gen-2.5" && ((tier == "extreme-low" && mode == "low") || (tier == "high" && mode == "high")) {
			q := measured(0, "", fmt.Sprintf("wallet delta of a material confirm on a gen-2.5 %s task with texture_mode %s, 2026-10-26 (the web shows no price)", tier, mode))
			q.FreeReason = "this material confirm took no credits"
			return q, nil
		}
		return unknown(fmt.Sprintf("the web shows no price for confirm_material, and it was measured only on a gen-2.5 extreme-low task with texture_mode low and a gen-2.5 high task with texture_mode high (both free); this task is %s with texture_mode %s",
			task(model, tier), orNone(mode))), nil
	case "export_model":
		return export(in, acct, model, tier)
	case "fork_task":
		if !in.TaskCreated.IsZero() && time.Since(in.TaskCreated) > 30*24*time.Hour {
			return Quote{}, &Gate{"the web offers fork only for tasks younger than 30 days"}
		}
		switch strings.ToLower(in.ForkStage) {
		case "geometry":
			if model == "gen-2.5" && tier == "extreme-low" {
				q := measured(0, "", "wallet deltas of two geometry forks of a gen-2.5 extreme-low task, 2026-10-26")
				q.FreeReason = "a geometry fork of this tier took no credits"
				return q, nil
			}
			return unknown("a geometry fork was measured only on a gen-2.5 extreme-low task (free), and the web's fork menu does not settle the price for " + task(model, tier)), nil
		case "material", "":
		default:
			return Quote{}, &InvalidInput{fmt.Sprintf("fork stage %q is not geometry or material", in.ForkStage)}
		}
		return label(0.5, "submit", `web label "0.5 Credits" on the Material fork ("Revert to branch. (0.5 Credits)")`), nil
	case "generate_agentic":
		switch strings.ToLower(in.Budget) {
		case "basic", "":
			return measured(0.5, "submit (frozen), taken during the run", "web Agentic timer: basic ~3 min, 0.5 credits; measured 2026-10-27: 0.5 frozen at submit and taken by the end of the run (high-poly producer)"), nil
		case "standard":
			return label(1, "submit (frozen), taken during the run", "web Agentic timer: standard ~5 min, 1 credit"), nil
		case "pro":
			return label(2, "submit (frozen), taken during the run", "web Agentic timer: pro ~10 min, 2 credits"), nil
		}
		return Quote{}, &InvalidInput{fmt.Sprintf("budget %q is not one of basic, standard, pro", in.Budget)}
	case "split_parts":
		return splitParts(in, acct)
	case "rig_model":
		tmpl := strings.ToLower(in.RigTemplate)
		if tmpl == "" {
			tmpl = "human"
		}
		if model == "gen-2.5" && tier == "high" && in.PackType == "material" && tmpl == "human" {
			q := measured(0, "", "wallet delta of a human-template rig of a textured gen-2.5 high task, 2026-10-27 (the web shows no price for rigging)")
			q.FreeReason = "this rig took no credits"
			return q, nil
		}
		return unknown(fmt.Sprintf("the web shows no price for rigging, and a rig was measured only with the human template on a textured gen-2.5 high task (free); this is template %s on a %s task (%s stage)",
			tmpl, task(model, tier), orNone(in.PackType))), nil
	case "animate_model":
		format := strings.ToLower(in.Format)
		if format == "" {
			format = "glb"
		}
		res := strings.ToLower(in.Resolution)
		if model == "gen-2.5" && tier == "high" && in.PackType == "material" && format == "glb" && (res == "" || res == "2k") && in.Animations == 1 {
			q := measured(0, "", "wallet delta of a 2k glb export with one animation preset of a rigged, textured gen-2.5 high task, 2026-10-27 (the web shows no price)")
			q.FreeReason = "this export took no credits"
			return q, nil
		}
		return unknown(fmt.Sprintf("the web shows no price for animation exports, and one was measured only as a 2k glb export with one preset of a rigged, textured gen-2.5 high task (free); this is %s at %s with %d animation(s) of a %s task (%s stage)",
			format, orNone(res), in.Animations, task(model, tier), orNone(in.PackType))), nil
	case "texture_model":
		if model == "gen-2.5" {
			// The web's 3D Editing submit sends no tier (brilliant_mode).
			q := measured(0, "confirm_geometry", "wallet delta of a gen-2.5 3D Editing submit, 2026-10-27: nothing taken, 0.5 frozen at submit (the web shows no price)")
			q.FreeReason = "the draft is free; the 0.5 frozen at submit is taken when the geometry is confirmed, or released"
			q.FrozenAtSubmit = credits(0.5)
			return q, nil
		}
		return unknown("a 3D Editing submit was measured only with gen-2.5 (nothing taken, 0.5 frozen); not with " + model), nil
	}
	names := make([]string, len(Operations))
	for i, o := range Operations {
		names[i] = o.Name
	}
	return Quote{}, &InvalidInput{fmt.Sprintf("operation %q is not one of %s", in.Operation, strings.Join(names, ", "))}
}

// splitParts prices split_parts: the plan (the part layout, run by the web
// as soon as its Bang panel opens) and the split ("Bang to Parts").
func splitParts(in Input, acct Account) (Quote, error) {
	if in.Strength < 0 || in.Strength > 12 {
		return Quote{}, &InvalidInput{fmt.Sprintf("strength %d is not 1-12", in.Strength)}
	}
	if in.Strength > 5 && !acct.has("RodinBangGenerateAdvancedQuality") {
		return Quote{}, &Gate{"a part split strength above 5 needs the RodinBangGenerateAdvancedQuality privilege (Creator plan or higher)"}
	}
	// Measured 2026-10-27 on a gen-2.5 extreme-low draft (notes 35, P3): the
	// plan took nothing; the split froze 0.5 at submit and took it when done.
	const plan = "wallet delta of a Bang plan, strength 5, on a gen-2.5 extreme-low draft, 2026-10-27 (the web shows no price for it)"
	const generate = `web label "0.5 Credits" on Bang to Parts; measured 2026-10-27: a split of a gen-2.5 extreme-low draft froze 0.5 at submit and took it when done`
	switch strings.ToLower(in.BangStep) {
	case "plan":
		q := measured(0, "", plan)
		q.FreeReason = "the Bang plan took no credits"
		return q, nil
	case "generate", "", "both":
		return measured(0.5, "submit (frozen), taken when the split is done", generate+"; the plan step before it took nothing"), nil
	}
	return Quote{}, &InvalidInput{fmt.Sprintf("step %q is not plan, generate or both", in.BangStep)}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func generate(in Input, acct Account) (Quote, error) {
	if in.Batch > 1 {
		plan := strings.ToLower(acct.Plan)
		if plan != "business" && plan != "enterprise" {
			return Quote{}, &Gate{fmt.Sprintf("batch %d needs the Business plan (this account: %s)", in.Batch, acct.Plan)}
		}
		// The web shows no price for a batch and none was measured: one
		// draft's label times the count is a guess, so no number.
		return unknown(fmt.Sprintf("the price of a batch of %d drafts is not shown by the web and has not been measured", in.Batch)), nil
	}
	model := strings.ToLower(in.Model)
	if model == "" {
		model = "gen-2.5"
	}
	// The Free Preview label was seen to hide a freeze on extreme-low, so a
	// label-only Free Preview says so.
	const freeze = `; on extreme-low the same label still froze 0.5 at submit`
	switch model {
	case "gen-1.5":
		if strings.ToLower(in.Mode) == "speedy" {
			q := measured(0, "confirm_geometry", `web label "Free Preview" on GENERATE for all four Gen-1.5 modes; measured 2026-10-27: a speedy draft took and froze nothing`)
			q.FreeReason = "Free Preview: the Gen-1.5 draft is free; confirming the geometry costs credits"
			return q, nil
		}
		q := label(0, "confirm_geometry", `web label "Free Preview" on GENERATE for all four Gen-1.5 modes`+freeze+`; a speedy draft froze nothing`)
		q.FreeReason = "Free Preview: the Gen-1.5 draft is free; confirming the geometry costs credits"
		return q, nil
	case "gen-2.5":
	default:
		return Quote{}, &InvalidInput{fmt.Sprintf("model %q is not gen-2.5 or gen-1.5", in.Model)}
	}
	tier := strings.ToLower(in.Tier)
	if tier == "" {
		return Quote{}, &InvalidInput{"a Gen-2.5 quote needs tier: one of " + strings.Join(tiers, ", ")}
	}
	switch tier {
	case "extreme-low":
		// The web froze 0.5 at an Extreme-Low submit (2026-10-07, again
		// 2026-10-26); confirm_geometry took it, and an unconfirmed draft
		// kept it frozen for over an hour (notes 35).
		q := measured(0, "confirm_geometry", `web label "Free Preview" on GENERATE for tier extreme-low; measured 2026-10-26: nothing taken at submit, 0.5 frozen and taken by confirm_geometry`)
		q.FreeReason = "Free Preview: the draft is free; confirming the geometry costs credits"
		q.FrozenAtSubmit = credits(0.5)
		return q, nil
	case "low", "medium":
		q := label(0, "confirm_geometry", `web label "Free Preview" on GENERATE for tier `+tier+freeze)
		q.FreeReason = "Free Preview: the draft is free; confirming the geometry costs credits"
		return q, nil
	case "high":
		return measured(0.5, "submit", `web label "0.5 Credits" on GENERATE for tier high; measured 2026-10-26: 0.5 frozen at submit, taken when the draft finished`), nil
	case "extreme-high":
		if !acct.has("RodinUseAdditionalImage") {
			return Quote{}, &Gate{"tier extreme-high needs the RodinUseAdditionalImage privilege (Creator plan or higher)"}
		}
		return label(1, "submit", `web label "1 Credits" on GENERATE for tier extreme-high`), nil
	}
	return Quote{}, &InvalidInput{fmt.Sprintf("tier %q is not one of %s", in.Tier, strings.Join(tiers, ", "))}
}

func confirmGeometry(in Input, acct Account, model, tier string) (Quote, error) {
	if in.SmartLowPoly {
		if !acct.has("RodinRemeshAdditionalFeatures") {
			return Quote{}, &Gate{"smart low-poly needs the RodinRemeshAdditionalFeatures privilege (Creator plan or higher)"}
		}
		return label(2, "confirm", `web label "2 Credits" on Confirm in Smart Low-poly mode`), nil
	}
	mesh := strings.ToLower(in.Mesh)
	if mesh == "" {
		mesh = DefaultMesh(acct)
	}
	if mesh != "raw" && mesh != "quad" {
		return Quote{}, &InvalidInput{fmt.Sprintf("mesh %q is not raw or quad", in.Mesh)}
	}
	if model == "gen-2.5" {
		switch {
		case tier == "extreme-low" && mesh == "raw":
			return measured(0.5, "confirm (it takes the 0.5 frozen at submit)",
				`web label "0.5 Credits" on Confirm of an extreme-low draft; measured 2026-10-26: a raw confirm took the 0.5 frozen at submit`), nil
		case tier == "extreme-low":
			return label(0.5, "confirm (it takes the 0.5 frozen at submit)",
				`web label "0.5 Credits" on Confirm of an extreme-low draft; a raw confirm there took the 0.5 frozen at submit, a quad confirm has not been measured`), nil
		case tier == "high" && mesh == "raw":
			q := measured(0, "", `wallet delta of a raw confirm of a high draft, 2026-10-26 (the web's Confirm label reads "0.5 Credits")`)
			q.FreeReason = "the 0.5 of the high-tier submit covers it: a raw confirm took nothing more"
			return q, nil
		case tier == "high":
			return unknown(`the web labels Confirm "0.5 Credits" on a high draft, but a raw confirm there took nothing more after the submit; a quad confirm has not been measured`), nil
		}
	}
	return unknown(`the web's Confirm label ("0.5 Credits") was read and the price measured only on gen-2.5 extreme-low and high drafts; not on ` + task(model, tier)), nil
}

const noOptions = "no options"

// textureOptions names the texture options that are on, in a fixed order.
func textureOptions(in Input) string {
	var opts []string
	for _, o := range []struct {
		on   bool
		name string
	}{{in.HD, "hd_texture"}, {in.Sharp, "sharp"}, {in.FaceEnhance, "face_enhance"}, {in.Delight, "delight"}} {
		if o.on {
			opts = append(opts, o.name)
		}
	}
	if len(opts) == 0 {
		return noOptions
	}
	return strings.Join(opts, ", ")
}

func texture(in Input, model, tier string) (Quote, error) {
	mode := strings.ToLower(in.TextureMode)
	if mode == "" {
		mode = "high" // the web panel's default tier
	}
	if mode == "legacy" || model == "gen-1.5" {
		return unknown("the web shows no price for a Gen-1.5 (legacy) texture and it has not been measured"), nil
	}
	if !oneOf(mode, tiers) {
		return Quote{}, &InvalidInput{fmt.Sprintf("texture_mode %q is not one of %s", in.TextureMode, strings.Join(tiers, ", "))}
	}
	if mode == "extreme-high" {
		// hd_texture, sharp and face_enhance do not apply at this tier.
		return label(2, "submit (frozen), taken on completion", `web label "2 Credits" for the extreme-high texture tier`), nil
	}
	options := textureOptions(in)
	switch {
	case model == "gen-2.5" && tier == "high" && mode == "high" && options == "face_enhance":
		q := measured(0, "", "wallet delta of a texture_mode high run with face_enhance on a gen-2.5 high task, 2026-10-26 (the web shows no price)")
		q.FreeReason = "this texture run took no credits"
		return q, nil
	case mode == "low" && options == noOptions:
		return unknown("texture_mode low with no options was measured twice with different results: 0.5 taken on 2026-10-07, nothing on 2026-10-26 (the web shows no price)"), nil
	}
	return unknown(fmt.Sprintf("the web shows no price for texture_mode %s, and texture runs were measured only for texture_mode high with face_enhance on a gen-2.5 high task (free) and texture_mode low with no options (0.5 once, 0 once); this is %s on a %s task",
		mode, options, task(model, tier))), nil
}

func export(in Input, acct Account, model, tier string) (Quote, error) {
	res := strings.ToLower(in.Resolution)
	high := false
	switch res {
	case "", "1k", "2k", "6k":
	case "4k", "12k":
		high = true
	default:
		if res == "8k" {
			return Quote{}, &InvalidInput{"8k is not a pack resolution: the web's 8K chip is the sharp_texture option of generate_texture; packs are 1k, 2k, 6k (Basic) or 4k, 12k (High)"}
		}
		return Quote{}, &InvalidInput{fmt.Sprintf("resolution %q is not one of 1k, 2k, 4k, 6k, 12k", in.Resolution)}
	}
	geo := strings.ToLower(in.Geometry)
	switch geo {
	case "", "base":
		geo = "base"
	case "lod":
	case "high_poly":
		high = true
	default:
		return Quote{}, &InvalidInput{fmt.Sprintf("geometry %q is not one of base, lod, high_poly", in.Geometry)}
	}
	if high && !acct.has("RodinPackHighResolution") {
		return Quote{}, &Gate{"4K and 12K packs and high-poly geometry need the RodinPackHighResolution privilege (Business plan); use 2k (or 6k for extreme-high textures) with base geometry"}
	}
	format := strings.ToLower(in.Format)
	if format == "" {
		format = "obj" // the tool's default, as the web's
	}
	material := strings.ToLower(in.Material)
	if material == "" {
		material = "all"
	}
	typ := strings.ToLower(in.PackType)
	if model == "gen-2.5" && (tier == "extreme-low" || tier == "low") {
		q := label(0, "", "web pack tip (i18n TIP_PACK_TITLE_3): downloads of Gen-2.5 extreme-low and low models are free")
		if tier == "extreme-low" && typ == "material" && !high && geo == "base" && format == "glb" && material == "all" {
			q = measured(0, "", "web pack tip (i18n TIP_PACK_TITLE_3); measured 2026-10-26: a 2k glb material pack of a gen-2.5 extreme-low task took nothing")
		}
		q.FreeReason = "downloads of Gen-2.5 extreme-low and low models are free"
		return q, nil
	}
	if model == "gen-2.5" && tier == "high" && typ == "material" && !high && geo == "base" && format == "obj" && material == "all" {
		q := measured(0, "", "wallet delta of a 2k obj material pack (base geometry, all maps) of a gen-2.5 high task, 2026-10-26 (the web shows no price on the pack panel)")
		q.FreeReason = "this pack took no credits"
		return q, nil
	}
	desc := fmt.Sprintf("geometry %s, %s", geo, format)
	switch typ {
	case "material":
		if res == "" {
			res = "default"
		}
		desc = fmt.Sprintf("material pack (resolution %s, %s, maps %s)", res, desc, material)
	case "mesh":
		desc = "mesh pack (" + desc + ")"
	default:
		desc = "pack of an unknown type (" + desc + ")"
	}
	return unknown(fmt.Sprintf("the web shows no price on the pack panel, and packs were measured only for gen-2.5 extreme-low tasks (free by the web's tip, as low is) and one 2k obj material pack with base geometry and all maps of a gen-2.5 high task (free); this is a %s of a %s task",
		desc, task(model, tier))), nil
}
