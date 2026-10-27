package service

import (
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/catalog"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
)

// rig_model's settings: the live human rig of 2026-10-27 sent the Mixamo
// hierarchy with core skinning and template human; every other choice as
// the web builds it (main.fmt.js:55804-55975).
func TestRigSettings(t *testing.T) {
	st, front, err := rigSettings(RigInput{}, rodin.RigSourceTexture, nil)
	if err != nil {
		t.Fatal(err)
	}
	bones, _ := st["rodinrig_hierarchy"].([]catalog.Bone)
	if st["rodinrig_template"] != "human" || st["skinning_method"] != "core" || len(bones) != 52 || front.Bones != 52 {
		t.Fatalf("default: %v %+v", st, front)
	}
	if src, _ := st["source"].(map[string]string); src["stage"] != "texture_refine" {
		t.Fatalf("source %v", st["source"])
	}
	st, _, err = rigSettings(RigInput{Template: "Dragon"}, rodin.RigSourceModel, nil)
	if err != nil || st["skinning_method"] != "shell" || st["rodinrig_template"] != "dragon" {
		t.Fatalf("dragon: %v %v", st, err)
	}
	st, front, err = rigSettings(RigInput{Skeleton: "Root\n-Tail"}, rodin.RigSourceModel, nil)
	if err != nil || st["rodinrig_template"] != "custom" || front.Bones != 2 || st["skinning_method"] != "shell" {
		t.Fatalf("own skeleton: %v %v", st, err)
	}
	st, _, err = rigSettings(RigInput{Description: "a snake"}, rodin.RigSourceModel, nil)
	if err != nil || st["rigging_mode"] != "generate" || st["rodinrig_description"] != "a snake" || st["rodinrig_hierarchy"] != nil {
		t.Fatalf("description: %v %v", st, err)
	}
	var f *Failure
	for _, in := range []RigInput{
		{Template: "human", Description: "x"}, {Template: "robot"}, {Template: "quadruped", Skinning: "core"},
		{Skeleton: "-\n--"}, {Skinning: "bones"}, {Description: "x", Skinning: "core"},
	} {
		if _, _, err := rigSettings(in, rodin.RigSourceModel, nil); !errors.As(err, &f) || f.E.Code != "invalid_input" {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

// texture_model's settings: what the live submit of 2026-10-27 sent and
// the server stored (Brilliant, no brilliant_mode, identity rotation, "?"
// labels, height 100, no face enhancement), and the Gen-1.5 variants.
func TestEditSettings(t *testing.T) {
	st := editSettings("gen-2.5", "", 1, "Meditating woman seated in lotus pose. complex geometry, character.")
	if st["generation_mode"] != "Brilliant" || st["brilliant_mode"] != nil || st["condition_mode"] != "concat" || st["height"] != 100 ||
		st["image_mode"] != "recon" || st["allow_face_enhance"] != false || st["default_face_enhance"] != false {
		t.Fatalf("gen-2.5: %v", st)
	}
	if r, _ := st["model_reference_rotation"].([]float64); !slices.Equal(r, []float64{1, 0, 0, 0, 1, 0, 0, 0, 1}) {
		t.Fatalf("rotation %v", st["model_reference_rotation"])
	}
	if l, _ := st["image_label"].([]string); !slices.Equal(l, []string{"?"}) {
		t.Fatalf("labels %v", st["image_label"])
	}
	st = editSettings("gen-1.5", "speedy", 3, "A chair. wooden")
	if st["generation_mode"] != "Fast" || st["fast_texture"] != true || st["condition_mode"] != "fuse" || st["image_mode"] != nil {
		t.Fatalf("gen-1.5 speedy: %v", st)
	}
	if w, _ := st["view_weights"].([]float64); !slices.Equal(w, []float64{0.33, 0.33, 0.34}) {
		t.Fatalf("weights %v", st["view_weights"])
	}
	if st = editSettings("gen-1.5", "focal", 1, "x"); st["sharp_bias"] != 2.5 || st["condition_mode"] != "concat" {
		t.Fatalf("gen-1.5 focal: %v", st)
	}
	for p, want := range map[string]bool{"A doll. character": true, "A character. sword": false, "character": true, "x. Character design": true} {
		if hasCharacterTag(p) != want {
			t.Errorf("%q: %v", p, !want)
		}
	}
}

// animate_model's file list: the live export of 2026-10-27 added two glb
// files to a task whose list still held its obj pack.
func TestExportedFiles(t *testing.T) {
	live := []string{"base.obj", "texture_diffuse.png", "texture_metallic.png", "texture_normal.png", "texture_pbr.png", "texture_roughness.png",
		"shaded.png", "base_basic_pbr.glb", "base_basic_shaded.glb"}
	if got := exportedFiles(live, "glb", false); !slices.Equal(got, []string{"base_basic_pbr.glb", "base_basic_shaded.glb"}) {
		t.Fatalf("glb: %v", got)
	}
	if got := exportedFiles([]string{"lod_a.fbx", "LOD.fbx", "highpoly-x.fbx", "lodge.fbx", "a/b.fbx", "c.png"}, "fbx", true); !slices.Equal(got, []string{"lodge.fbx", "a/b.fbx", "c.png"}) {
		t.Fatalf("fbx: %v", got)
	}
}

func TestRequestIDAndBangNames(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if a, b := newRequestID(), newRequestID(); !re.MatchString(a) || a == b {
		t.Fatalf("request ids %s %s", a, b)
	}
	name := bangFileName("v2", "562c67")
	if m := bangFileRE.FindStringSubmatch(name); name != "bang-v2-562c67.glb" || m == nil || m[1] != "v2" || m[2] != "562c67" {
		t.Fatalf("%s %v", name, m)
	}
	if regexpVersion.MatchString("562c67") || !regexpVersion.MatchString("V3") {
		t.Fatal("version pattern")
	}
}
