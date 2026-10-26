package price

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var creator = Account{Plan: "Creator", Privileges: []string{"RodinUseAdditionalImage", "RodinRemeshAdditionalFeatures", "RodinMediumRedoCount"}}

func credit(t *testing.T, in Input) (float64, Quote) {
	t.Helper()
	q, err := For(in, creator)
	if err != nil {
		t.Fatalf("%+v: %v", in, err)
	}
	if !q.Known || q.Credits == nil {
		t.Fatalf("%+v: unknown quote %+v", in, q)
	}
	return *q.Credits, q
}

func TestLabelTable(t *testing.T) {
	cases := []struct {
		in   Input
		want float64
	}{
		{Input{Operation: "generate_model", Tier: "extreme-low"}, 0},
		{Input{Operation: "generate_model", Tier: "medium"}, 0},
		{Input{Operation: "generate_model", Tier: "high"}, 0.5},
		{Input{Operation: "generate_model", Tier: "extreme-high"}, 1},
		{Input{Operation: "generate_model", Model: "gen-1.5"}, 0},
		{Input{Operation: "confirm_geometry", Tier: "extreme-low", Mesh: "raw"}, 0.5},
		{Input{Operation: "confirm_geometry", Tier: "high", Mesh: "raw"}, 0},
		{Input{Operation: "confirm_geometry", SmartLowPoly: true}, 2},
		{Input{Operation: "generate_texture", TextureMode: "extreme-high"}, 2},
		{Input{Operation: "generate_texture", Tier: "high", TextureMode: "high", FaceEnhance: true}, 0},
		{Input{Operation: "confirm_material", Tier: "high", TextureMode: "high"}, 0},
		{Input{Operation: "fork_task"}, 0.5},
		{Input{Operation: "fork_task", ForkStage: "geometry", Tier: "extreme-low"}, 0},
		{Input{Operation: "generate_agentic", Budget: "pro"}, 2},
		{Input{Operation: "split_parts", BangStep: "generate"}, 0.5},
		{Input{Operation: "split_parts", Tier: "extreme-low"}, 0.5},
		{Input{Operation: "split_parts", BangStep: "plan", Strength: 5, Tier: "extreme-low"}, 0},
		{Input{Operation: "rig_model", Tier: "high", PackType: "material"}, 0},
		{Input{Operation: "texture_model"}, 0},
		{Input{Operation: "generate_agentic", Budget: "basic"}, 0.5},
		{Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1}, 0},
		{Input{Operation: "confirm_geometry", Tier: "extreme-low", Mesh: "raw", Bang: true}, 0.5},
		{Input{Operation: "export_model", Tier: "low"}, 0},
		{Input{Operation: "export_model", Tier: "high", PackType: "material"}, 0},
		{Input{Operation: "redo_geometry", Tier: "extreme-high"}, 2},
		{Input{Operation: "redo_material", Tier: "high", TextureMode: "high", FaceEnhance: true}, 0},
		{Input{Operation: "select_candidate"}, 0},
	}
	for _, c := range cases {
		if got, _ := credit(t, c.in); got != c.want {
			t.Errorf("%+v = %v, want %v", c.in, got, c.want)
		}
	}
	if _, q := credit(t, Input{Operation: "generate_model", Tier: "extreme-low"}); q.FrozenAtSubmit == nil || q.FreeReason == "" {
		t.Errorf("free preview quote %+v", q)
	}
}

func TestUnknownPrices(t *testing.T) {
	for _, in := range []Input{
		{Operation: "generate_texture", TextureMode: "medium"}, {Operation: "generate_texture", Model: "gen-1.5"},
		{Operation: "generate_texture", TextureMode: "low"}, {Operation: "confirm_geometry"}, {Operation: "confirm_material"}, {Operation: "redo_material"},
		{Operation: "rig_model"}, {Operation: "animate_model"}, {Operation: "texture_model", Model: "gen-1.5"},
		{Operation: "rig_model", Tier: "high", PackType: "material", RigTemplate: "dragon"},
		{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 3},
		{Operation: "split_parts"}, {Operation: "split_parts", BangStep: "plan"},
	} {
		q, err := For(in, creator)
		if err != nil || q.Known || q.Credits != nil || q.Basis == "" {
			t.Errorf("%+v: %+v %v", in, q, err)
		}
	}
}

func TestGates(t *testing.T) {
	var gate *Gate
	for _, in := range []Input{
		{Operation: "generate_model", Tier: "high", Batch: 3},
		{Operation: "export_model", Resolution: "4k"},
		{Operation: "export_model", Geometry: "high_poly"},
		{Operation: "fork_task", TaskCreated: time.Now().Add(-40 * 24 * time.Hour)},
		{Operation: "split_parts", Strength: 6},
	} {
		if _, err := For(in, creator); !errors.As(err, &gate) {
			t.Errorf("%+v: err %v, want a gate", in, err)
		}
	}
	if _, err := For(Input{Operation: "generate_model", Tier: "extreme-high"}, Account{Plan: "Free"}); !errors.As(err, &gate) {
		t.Errorf("extreme-high without privilege: %v", err)
	}
	// L11: a batch the plan allows has no measured price, so no number.
	if q, err := For(Input{Operation: "generate_model", Tier: "high", Batch: 3}, Account{Plan: "Business"}); err != nil || q.Known || q.Credits != nil {
		t.Errorf("business batch: %+v %v", q, err)
	}
	// A split strength above 5 passes with the privilege; 13 is no strength.
	if _, err := For(Input{Operation: "split_parts", Strength: 12}, Account{Privileges: []string{"RodinBangGenerateAdvancedQuality"}}); err != nil {
		t.Errorf("strength 12 with the privilege: %v", err)
	}
	var bad *InvalidInput
	if _, err := For(Input{Operation: "split_parts", Strength: 13}, Account{Privileges: []string{"RodinBangGenerateAdvancedQuality"}}); !errors.As(err, &bad) {
		t.Errorf("strength 13: %v", err)
	}
	// M4: 4K/12K and high-poly packs pass with the privilege.
	pack := Account{Plan: "Business", Privileges: []string{"RodinPackHighResolution"}}
	for _, in := range []Input{{Operation: "export_model", Resolution: "4k"}, {Operation: "export_model", Resolution: "12k", Geometry: "high_poly"}} {
		if _, err := For(in, pack); err != nil {
			t.Errorf("%+v with the privilege: %v", in, err)
		}
	}
}

// M4: 8k is the sharp_texture chip, not a pack resolution, on every plan.
func TestEightKIsNotAPackResolution(t *testing.T) {
	var bad *InvalidInput
	for _, acct := range []Account{creator, {Plan: "Business", Privileges: []string{"RodinPackHighResolution"}}} {
		_, err := For(Input{Operation: "export_model", Resolution: "8K"}, acct)
		if !errors.As(err, &bad) || !strings.Contains(bad.Reason, "sharp_texture") {
			t.Errorf("%s: %v", acct.Plan, err)
		}
	}
}

func TestInvalidInput(t *testing.T) {
	var bad *InvalidInput
	for _, in := range []Input{
		{Operation: "make_coffee"}, {Operation: "generate_model"}, {Operation: "generate_model", Tier: "ultra"},
		{Operation: "generate_texture", TextureMode: "x"}, {Operation: "generate_agentic", Budget: "max"}, {Operation: "export_model", Resolution: "3k"},
	} {
		if _, err := For(in, creator); !errors.As(err, &bad) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	for _, o := range Operations {
		if k, ok := PermissionKey(o.Name); !ok || k == "" {
			t.Errorf("%s has no permission key", o.Name)
		}
	}
}

// M1, L2: a measured quote holds only for the inputs it was measured with;
// changing any one of them gives the label (when the web shows one for
// every case) or no price.
func TestMeasuredOnlyForTheMeasuredCase(t *testing.T) {
	type want struct {
		credits  float64 // -1: not known
		measured bool
	}
	measuredAt := func(c float64) want { return want{c, true} }
	labelAt := func(c float64) want { return want{c, false} }
	none := want{-1, false}
	el := "extreme-low"
	cases := []struct {
		name string
		in   Input
		want want
	}{
		// Confirm of a Bang result: measured raw on gen-2.5 extreme-low only.
		{"bang confirm measured", Input{Operation: "confirm_geometry", Tier: el, Mesh: "raw", Bang: true}, measuredAt(0.5)},
		{"bang confirm default mesh", Input{Operation: "confirm_geometry", Tier: el, Bang: true}, measuredAt(0.5)},
		{"bang confirm high", Input{Operation: "confirm_geometry", Tier: "high", Mesh: "raw", Bang: true}, none},
		{"bang confirm quad", Input{Operation: "confirm_geometry", Tier: el, Mesh: "quad", Bang: true}, none},
		{"bang confirm smart", Input{Operation: "confirm_geometry", Tier: el, SmartLowPoly: true, Bang: true}, none},
		{"bang confirm gen-1.5", Input{Operation: "confirm_geometry", Model: "gen-1.5", Bang: true}, none},
		{"plain confirm high stays", Input{Operation: "confirm_geometry", Tier: "high", Mesh: "raw"}, measuredAt(0)},
		// split_parts: plan, split and both at strength 5 of the whole model
		// of a gen-2.5 extreme-low draft.
		{"plan measured", Input{Operation: "split_parts", BangStep: "plan", Tier: el}, measuredAt(0)},
		{"plan strength 6", Input{Operation: "split_parts", BangStep: "plan", Tier: el, Strength: 6}, none},
		{"plan high", Input{Operation: "split_parts", BangStep: "plan", Tier: "high"}, none},
		{"plan part", Input{Operation: "split_parts", BangStep: "plan", Tier: el, BangPart: true}, none},
		{"split measured", Input{Operation: "split_parts", BangStep: "generate", Tier: el, Strength: 5}, measuredAt(0.5)},
		{"split high", Input{Operation: "split_parts", BangStep: "generate", Tier: "high"}, labelAt(0.5)},
		{"split part", Input{Operation: "split_parts", BangStep: "generate", Tier: el, BangPart: true}, labelAt(0.5)},
		{"split strength 3", Input{Operation: "split_parts", BangStep: "generate", Tier: el, Strength: 3}, labelAt(0.5)},
		{"both measured", Input{Operation: "split_parts", BangStep: "both", Tier: el}, measuredAt(0.5)},
		{"both high", Input{Operation: "split_parts", Tier: "high"}, none},
		{"both part", Input{Operation: "split_parts", Tier: el, BangPart: true}, none},
		{"both gen-1.5", Input{Operation: "split_parts", Model: "gen-1.5", Tier: el}, none},
		// rig_model: human with core skinning on a textured gen-2.5 high task.
		{"rig measured", Input{Operation: "rig_model", Tier: "high", PackType: "material", RigTemplate: "human", Skinning: "core"}, measuredAt(0)},
		{"rig default skinning", Input{Operation: "rig_model", Tier: "high", PackType: "material"}, measuredAt(0)},
		{"rig shell", Input{Operation: "rig_model", Tier: "high", PackType: "material", Skinning: "shell"}, none},
		{"rig custom", Input{Operation: "rig_model", Tier: "high", PackType: "material", RigTemplate: "custom"}, none},
		{"rig mesh stage", Input{Operation: "rig_model", Tier: "high", PackType: "mesh"}, none},
		// animate_model: one preset, no own file, glb 2k all maps.
		{"animate measured", Input{Operation: "animate_model", Tier: "high", PackType: "material", Format: "glb", Resolution: "2k", Material: "all", Animations: 1}, measuredAt(0)},
		{"animate own file", Input{Operation: "animate_model", Tier: "high", PackType: "material", AnimationFiles: 1}, none},
		{"animate preset and file", Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1, AnimationFiles: 1}, none},
		{"animate pbr", Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1, Material: "pbr"}, none},
		{"animate fbx", Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1, Format: "fbx"}, none},
		{"animate 1k", Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1, Resolution: "1k"}, none},
		{"animate no animation", Input{Operation: "animate_model", Tier: "high", PackType: "material"}, none},
		// texture_model: gen-2.5 with one reference image (or the render).
		{"texture_model render", Input{Operation: "texture_model"}, measuredAt(0)},
		{"texture_model one image", Input{Operation: "texture_model", Images: 1}, measuredAt(0)},
		{"texture_model two images", Input{Operation: "texture_model", Images: 2}, none},
		{"texture_model gen-1.5", Input{Operation: "texture_model", Model: "gen-1.5", Images: 1}, none},
	}
	acct := Account{Plan: "Creator", Privileges: []string{"RodinMeshAdditionalFeatures", "RodinRemeshAdditionalFeatures", "RodinBangGenerateAdvancedQuality"}}
	for _, c := range cases {
		q, err := For(c.in, acct)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := want{-1, q.Measured}
		if q.Known {
			got.credits = *q.Credits
		}
		if got != c.want || q.Known != (q.Credits != nil) || q.Basis == "" {
			t.Errorf("%s: credits %v measured %v known %v, want %+v (basis %q)", c.name, got.credits, q.Measured, q.Known, c.want, q.Basis)
		}
	}
}
