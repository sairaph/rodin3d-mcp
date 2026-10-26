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
		{Input{Operation: "split_parts"}, 0.5},
		{Input{Operation: "split_parts", BangStep: "plan", Strength: 5}, 0},
		{Input{Operation: "rig_model", Tier: "high", PackType: "material"}, 0},
		{Input{Operation: "texture_model"}, 0},
		{Input{Operation: "generate_agentic", Budget: "basic"}, 0.5},
		{Input{Operation: "animate_model", Tier: "high", PackType: "material", Animations: 1}, 0},
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
