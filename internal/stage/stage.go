// Package stage maps a task card's step and user_state to the stage a user
// thinks in (draft, confirmed geometry, material, pack), the actions that
// stage allows (plan 2.6.1), and the redo counters of the account's plan.
package stage

import "strings"

// Stages.
const (
	GeometryDraft = "geometry_draft" // ModelGenerate: draft awaiting confirm
	Geometry      = "geometry"       // ModelRefine: geometry confirmed
	MaterialDraft = "material_draft" // TextureGenerate: material draft
	Material      = "material"       // TextureRefine: material confirmed
)

// States.
const (
	Running = "running" // a job is working on the task
	Ready   = "ready"   // waiting for the user
	Packed  = "packed"  // a pack finished (DoneBasic / DoneHigh)
	Unknown = "unknown" // a user_state this build does not know
)

// Status is where a task stands.
type Status struct {
	Stage     string `yaml:"stage"`
	State     string `yaml:"state"`
	Step      string `yaml:"step"`
	UserState string `yaml:"user_state"`
}

// Of reads the status from a card's step, user_state and number of pending
// jobs.
func Of(step, userState string, pendingJobs int) Status {
	s := Status{Step: step, UserState: userState}
	switch step {
	case "ModelRefine":
		s.Stage = Geometry
	case "TextureGenerate":
		s.Stage = MaterialDraft
	case "TextureRefine":
		s.Stage = Material
	default: // Created, ModelGenerate
		s.Stage = GeometryDraft
	}
	switch userState {
	case "Generating":
		s.State = Running
	case "DonePreview", "Liked":
		s.State = Ready
	case "DoneBasic", "DoneHigh":
		s.State = Packed
	case "":
		s.State = Running // a task that was just created has no state yet
	default:
		s.State = Unknown
	}
	if step == "Created" || pendingJobs > 0 {
		s.State = Running
	}
	return s
}

// Actions lists the tools that act on a task in status s, in the order a user
// would take them (plan 2.6.1); get_preview is listed for every stage that
// is not running. Callers drop the tools their build does not register.
func Actions(s Status) []string {
	if s.State == Running {
		if s.Stage == Material {
			return []string{"wait_task", "export_model"}
		}
		return []string{"wait_task"}
	}
	if s.State == Unknown {
		// A state this build does not know (a failed task is one, not yet
		// seen live): only looking is safe.
		return []string{"get_preview"}
	}
	var out []string
	switch s.Stage {
	case GeometryDraft:
		// Bang (split_parts) works on the draft only: on a confirmed task
		// its calls answer TASK_ALREADY_CONFIRMED (notes 14 section 5).
		out = []string{"get_preview", "redo_geometry", "confirm_geometry", "select_candidate", "split_parts"}
	// Rigging works from the confirmed geometry on, the material draft
	// included (web rig sources, main.fmt.js:149431). animate_model needs
	// a rig, which the card does not show: get_task reads the rig and offers
	// it.
	case Geometry:
		out = []string{"get_preview", "generate_texture", "export_model", "fork_task", "rig_model"}
	case MaterialDraft:
		out = []string{"get_preview", "redo_material", "confirm_material", "fork_task", "rig_model"}
	case Material:
		out = []string{"get_preview", "export_model", "fork_task", "rig_model"}
	}
	if s.State == Packed {
		out = append(out, "download_model")
	}
	return out
}

// Redo limits per plan (notes 14, section 3.2).
const (
	geoHigh, matHigh     = 50, 15 // RodinHighRedoCount (Business)
	geoMedium, matMedium = 20, 6  // RodinMediumRedoCount (Creator)
	geoFree, matFree     = 10, 3
)

// RedoLimits returns the geometry and material redo limits for privileges.
func RedoLimits(privileges []string) (geometry, material int) {
	has := func(name string) bool {
		for _, p := range privileges {
			if p == name {
				return true
			}
		}
		return false
	}
	switch {
	case has("RodinHighRedoCount"):
		return geoHigh, matHigh
	case has("RodinMediumRedoCount"):
		return geoMedium, matMedium
	}
	return geoFree, matFree
}

// RedosLeft is limit - versions + 1 (the web's own count: the first version
// is not a redo), never below zero.
func RedosLeft(limit, versions int) int {
	if versions <= 0 {
		return limit
	}
	return max(limit-versions+1, 0)
}

// Tier returns the Gen-2.5 tier of a brilliant_mode value
// ("gen-2.5-extreme-low" -> "extreme-low"), or "" when it is not one.
func Tier(brilliantMode string) string {
	const prefix = "gen-2.5-"
	if strings.HasPrefix(brilliantMode, prefix) {
		return strings.TrimPrefix(brilliantMode, prefix)
	}
	return ""
}

// Model names the generation model of a card's settings: "gen-2.5" for
// Brilliant, "gen-1.5" for the Gen-1.5 modes.
func Model(generationMode string) string {
	switch generationMode {
	case "Brilliant":
		return "gen-2.5"
	case "Default", "Detail", "Smooth", "Fast":
		return "gen-1.5"
	}
	return ""
}

// Gen15Mode maps a Gen-1.5 generation_mode to its UI name.
func Gen15Mode(generationMode string) string {
	switch generationMode {
	case "Default":
		return "default"
	case "Detail":
		return "focal"
	case "Smooth":
		return "zero"
	case "Fast":
		return "speedy"
	}
	return ""
}
