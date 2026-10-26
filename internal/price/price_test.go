package price

import (
	"errors"
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
		{Input{Operation: "confirm_geometry"}, 0.5},
		{Input{Operation: "confirm_geometry", SmartLowPoly: true}, 2},
		{Input{Operation: "generate_texture", TextureMode: "extreme-high"}, 2},
		{Input{Operation: "generate_texture", TextureMode: "low"}, 0.5},
		{Input{Operation: "fork_task"}, 0.5},
		{Input{Operation: "generate_agentic", Budget: "pro"}, 2},
		{Input{Operation: "split_parts"}, 0.5},
		{Input{Operation: "export_model", Tier: "low"}, 0},
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
		{Operation: "generate_texture"}, {Operation: "confirm_material"}, {Operation: "export_model", Tier: "high"},
		{Operation: "rig_model"}, {Operation: "animate_model"}, {Operation: "texture_model"},
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
	} {
		if _, err := For(in, creator); !errors.As(err, &gate) {
			t.Errorf("%+v: err %v, want a gate", in, err)
		}
	}
	if _, err := For(Input{Operation: "generate_model", Tier: "extreme-high"}, Account{Plan: "Free"}); !errors.As(err, &gate) {
		t.Errorf("extreme-high without privilege: %v", err)
	}
	if _, err := For(Input{Operation: "generate_model", Tier: "high", Batch: 3}, Account{Plan: "Business"}); err != nil {
		t.Errorf("business batch: %v", err)
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
