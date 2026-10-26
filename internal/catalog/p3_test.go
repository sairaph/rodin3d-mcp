package catalog

import (
	"slices"
	"testing"
)

// The web's template hierarchies parse to the bone counts and parents it
// sends (the human template's 52 bones were confirmed by the live export of
// 2026-10-27: 52 joints).
func TestTemplateSkeletons(t *testing.T) {
	for _, tm := range RigTemplates {
		bones := ParseSkeleton(tm.Skeleton)
		if len(bones) != tm.Bones {
			t.Errorf("%s: %d bones, want %d", tm.Name, len(bones), tm.Bones)
		}
		if bones[0] != (Bone{"Hips", -1}) {
			t.Errorf("%s: root %+v", tm.Name, bones[0])
		}
		for i, b := range bones {
			if b.Parent >= i || b.Parent < -1 {
				t.Errorf("%s: bone %d %s has parent %d", tm.Name, i, b.Name, b.Parent)
			}
		}
	}
	human := ParseSkeleton(skeletonMixamo)
	idx := func(name string) int {
		return slices.IndexFunc(human, func(b Bone) bool { return b.Name == name })
	}
	for child, parent := range map[string]string{"Spine": "Hips", "Neck": "Spine2", "LeftShoulder": "Spine2", "LeftHandThumb1": "LeftHand", "LeftUpLeg": "Hips", "RightToeBase": "RightFoot"} {
		if c := idx(child); c < 0 || human[c].Parent != idx(parent) {
			t.Errorf("%s: parent %d, want %s (%d)", child, human[c].Parent, parent, idx(parent))
		}
	}
}

// ParseSkeleton follows the web's parser: blank lines, surrounding spaces,
// a skipped "pose" bone whose children attach to its parent.
func TestParseSkeletonRules(t *testing.T) {
	got := ParseSkeleton("  Root\r\n\n-mixamorig:Pose\n--Spine\n-Leg\n--- \n")
	want := []Bone{{"Root", -1}, {"Spine", 0}, {"Leg", 0}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, name := range []string{"pose", "POSE", "a:b:Pose", "mixamorig:pose", "mixamorigpose", "po_se", "po-se"} {
		if !skippedBone(name) {
			t.Errorf("%q not skipped", name)
		}
	}
	for _, name := range []string{"Poser", "Hips", "posed"} {
		if skippedBone(name) {
			t.Errorf("%q skipped", name)
		}
	}
}

func TestAnimationPresets(t *testing.T) {
	n := 0
	for _, c := range AnimationPresets {
		n += len(c.Presets)
	}
	if n != 161 || len(AnimationPresets) != 9 {
		t.Fatalf("%d presets in %d categories", n, len(AnimationPresets))
	}
	for id, ok := range map[string]bool{"human/Walking": true, "canine/Default_Canine_Walk_Anim": true, "human/Default_Canine_Walk_Anim": false, "Walking": false, "human/": false} {
		if IsAnimationPreset(id) != ok {
			t.Errorf("%s: %v", id, !ok)
		}
	}
	for _, tm := range RigTemplates {
		if !slices.ContainsFunc(AnimationPresets, func(c AnimationCategory) bool { return c.Dir == tm.Animations }) {
			t.Errorf("%s: category %s", tm.Name, tm.Animations)
		}
	}
}
