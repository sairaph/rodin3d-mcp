package catalog

import "strings"

// AnimationCategory is one folder of the web's bundled animation presets
// (notes 14 section 6): the id sent for a preset is "<Dir>/<name>".
type AnimationCategory struct {
	Dir     string   `yaml:"category"`
	Presets []string `yaml:"presets,flow"`
}

// AnimationPresets is the web's preset catalogue (161 presets; the FBX,
// poster and preview video of each are bundled in the web app, not served
// by an endpoint).
var AnimationPresets = []AnimationCategory{
	{"bird", []string{"Default_Bird_FlyStart_Anim", "Default_Bird_Fly_Anim", "Default_Bird_Glide_Anim", "Default_Bird_Hovering_Anim", "Default_Bird_IdleStanding_Anim", "Default_Bird_Run_Anim", "Default_Bird_Walk_Anim"}},
	{"canine", []string{"Default_Canine_Frightened_Anim", "Default_Canine_Idle_Anim", "Default_Canine_Run_Anim", "Default_Canine_Stand_Anim", "Default_Canine_StartWalk_Anim", "Default_Canine_Trot_Anim", "Default_Canine_WalkStop_Anim", "Default_Canine_Walk_Anim"}},
	{"fish", []string{"Default_Fish_Dash_Anim", "Default_Fish_DeathStruggle_Anim", "Default_Fish_DophinSwim_Anim", "Default_Fish_JumpOut_Anim", "Default_Fish_Swim_Anim"}},
	{"heavy-hooved", []string{"Default_HeavyHooved_BackHoofAttack_Anim", "Default_HeavyHooved_FrontHoofKick_Anim", "Default_HeavyHooved_HeadAttack_Anim", "Default_HeavyHooved_Idle2_Anim", "Default_HeavyHooved_Idle_Anim", "Default_HeavyHooved_RunStop_Anim", "Default_HeavyHooved_Run_Anim", "Default_HeavyHooved_StandUp_Anim", "Default_HeavyHooved_TossHead_Anim", "Default_HeavyHooved_TrotStart_Anim", "Default_HeavyHooved_TrotStop_Anim", "Default_HeavyHooved_Trot_Anim", "Default_HeavyHooved_Walk1Start_Anim", "Default_HeavyHooved_Walk1Stop_Anim", "Default_HeavyHooved_Walk1_Anim", "Default_HeavyHooved_Walk2Start_Anim", "Default_HeavyHooved_Walk2Stop_Anim", "Default_HeavyHooved_Walk2_Anim"}},
	{"human", []string{"ArmStretching", "BatterOnDeck", "BigJump", "BigStomachHit", "Boxing", "BrutalAssassination", "CrawlBackwards", "CrossJumpsRotation", "CrouchedWalking", "CrouchedWalking2", "DancingMaraschinoStep", "DancingTwerk", "DeathFromRight", "DrunkIdle", "DwarfIdle", "Dying", "Dying2", "Entry", "FishingCast", "Flair", "FlyingKick", "ForwardJump", "FrisbeeThrow", "GoalieThrow", "GoalkeeperOverhandThrow", "GolfDrive", "GoofyRunning", "HappyIdle", "HeadHit", "HipHopDancing", "HipHopDancing2", "InjuredRun", "Jogging", "JoyfulJump", "Jumping", "Kicking", "Kicking2", "KidneyHit", "KipUp", "LegSweep", "LowCrawl", "Martelo2", "Moonwalk", "MutantJumpAttack", "NorthernSoulSpin", "OffensiveIdle", "PointingForward", "QuickRollToRun", "ReceivingAnUppercut", "Run", "Run2", "RunBackward", "RunLookBack", "Running", "Running2", "RunningJump", "RunningSlide", "SadIdle", "SalsaDancing", "SalsaDancing2", "Salute", "ScaryClownWalk", "ShoulderHitAndFall", "Singing", "Sitting", "Sitting2", "Sitting3", "Situps", "SnakeHipHopDance", "StandingDeathBackward01", "StandingMeleeKick", "StandingReactDeathLeft", "Swimming", "SwingDancing", "Taunt", "Throw", "Throwing", "TwistDance", "TwoCycleSprint", "TwoHandedSwordDeath", "Walking", "Walking2", "Walking3", "Walking4", "Walking5", "Walking6", "ZombieCrawl", "ZombieScratchIdle", "ZombieStumbling"}},
	{"lean-hooved", []string{"Default_LeanHooved_BackHoofAttack_Anim", "Default_LeanHooved_FrontHoofKick_Anim", "Default_LeanHooved_HeadAttack_Anim", "Default_LeanHooved_Idle2_Anim", "Default_LeanHooved_Idle_Anim", "Default_LeanHooved_RunStop_Anim", "Default_LeanHooved_Run_Anim", "Default_LeanHooved_StandUp_Anim", "Default_LeanHooved_TossHead_Anim", "Default_LeanHooved_TrotStart_Anim", "Default_LeanHooved_TrotStop_Anim", "Default_LeanHooved_Trot_Anim", "Default_LeanHooved_Walk1Start_Anim", "Default_LeanHooved_Walk1Stop_Anim", "Default_LeanHooved_Walk1_Anim", "Default_LeanHooved_Walk2Start_Anim", "Default_LeanHooved_Walk2Stop_Anim", "Default_LeanHooved_Walk2_Anim"}},
	{"lizard", []string{"Default_Lizard_Charge_Anim", "Default_Lizard_Idle_Anim", "Default_Lizard_Run_Anim", "Default_Lizard_Walk_Anim"}},
	{"rodentia", []string{"Default_Rodentia_Idle1_Anim", "Default_Rodentia_Idle2_Anim", "Default_Rodentia_IdleSwitch_Anim", "Default_Rodentia_ItchyScratch_Anim", "Default_Rodentia_Run_Anim", "Default_Rodentia_ScratchWall_Anim", "Default_Rodentia_SniffAround_Anim", "Default_Rodentia_WalkStart_Anim", "Default_Rodentia_WalkStop_Anim", "Default_Rodentia_Walk_Anim"}},
	{"sea-turtle", []string{"Default_SeaTurtle_Climb_Anim", "Default_SeaTurtle_Swim_Anim"}},
}

// IsAnimationPreset reports whether id ("<category>/<name>") is a preset.
func IsAnimationPreset(id string) bool {
	dir, name, ok := strings.Cut(id, "/")
	if !ok {
		return false
	}
	for _, c := range AnimationPresets {
		if c.Dir == dir {
			for _, p := range c.Presets {
				if p == name {
					return true
				}
			}
		}
	}
	return false
}

// RigTemplate is a skeleton template of the web's rigging panel.
type RigTemplate struct {
	Name string `yaml:"name"`
	// Skeleton is the bone hierarchy the web sends for the template: one
	// bone per line, depth by leading "-" characters.
	Skeleton string `yaml:"-"`
	// Skinning is the web's skinning for the template: core for human,
	// shell for the others (main.fmt.js:55808).
	Skinning string `yaml:"skinning"`
	// Animations is the preset category the web's animation panel opens on
	// for the template (main.fmt.js:50609).
	Animations string `yaml:"animations"`
	Bones      int    `yaml:"bones"`
}

// Bone hierarchies of the web's templates (main.fmt.js:51473-51503). The
// human template is the Mixamo skeleton; quadruped, bird and kaiju share the
// quadruped skeleton; the dragon adds wings.
const (
	skeletonMixamo    = "Hips\n-Spine\n--Spine1\n---Spine2\n----Neck\n-----Head\n----LeftShoulder\n-----LeftArm\n------LeftForeArm\n-------LeftHand\n--------LeftHandThumb1\n---------LeftHandThumb2\n----------LeftHandThumb3\n--------LeftHandIndex1\n---------LeftHandIndex2\n----------LeftHandIndex3\n--------LeftHandMiddle1\n---------LeftHandMiddle2\n----------LeftHandMiddle3\n--------LeftHandRing1\n---------LeftHandRing2\n----------LeftHandRing3\n--------LeftHandPinky1\n---------LeftHandPinky2\n----------LeftHandPinky3\n----RightShoulder\n-----RightArm\n------RightForeArm\n-------RightHand\n--------RightHandThumb1\n---------RightHandThumb2\n----------RightHandThumb3\n--------RightHandIndex1\n---------RightHandIndex2\n----------RightHandIndex3\n--------RightHandMiddle1\n---------RightHandMiddle2\n----------RightHandMiddle3\n--------RightHandRing1\n---------RightHandRing2\n----------RightHandRing3\n--------RightHandPinky1\n---------RightHandPinky2\n----------RightHandPinky3\n-LeftUpLeg\n--LeftLeg\n---LeftFoot\n----LeftToeBase\n-RightUpLeg\n--RightLeg\n---RightFoot\n----RightToeBase"
	skeletonQuadruped = "Hips\n-Spine\n--Spine1\n---Spine2\n----Spine3\n-----Neck\n------Neck1\n-------Head\n--------Jaw\n-----LeftShoulder\n------LeftArm\n-------LeftForeArm\n--------LeftFrontFoot\n---------LeftFrontToeBase\n-----RightShoulder\n------RightArm\n-------RightForeArm\n--------RightFrontFoot\n---------RightFrontToeBase\n-Tail1\n--Tail2\n---Tail3\n----Tail4\n-----Tail5\n------Tail6\n-LeftUpLeg\n--LeftLeg\n---LeftHindFoot\n----LeftHindToeBase\n-RightUpLeg\n--RightLeg\n---RightHindFoot\n----RightHindToeBase"
	skeletonDragon    = "Hips\n-Spine\n--Spine1\n---Spine2\n----Spine3\n-----Neck\n------Neck1\n-------Head\n--------Jaw\n-----LeftShoulder\n------LeftArm\n-------LeftForeArm\n--------LeftFrontFoot\n---------LeftFrontToeBase\n-----RightShoulder\n------RightArm\n-------RightForeArm\n--------RightFrontFoot\n---------RightFrontToeBase\n-----LeftWing\n------LeftWing1\n-------LeftWing2\n-----RightWing\n------RightWing1\n-------RightWing2\n-Tail1\n--Tail2\n---Tail3\n----Tail4\n-----Tail5\n------Tail6\n-LeftUpLeg\n--LeftLeg\n---LeftHindFoot\n----LeftHindToeBase\n-RightUpLeg\n--RightLeg\n---RightHindFoot\n----RightHindToeBase"
)

// RigTemplates in the order of the web's panel.
var RigTemplates = []RigTemplate{
	{Name: "human", Skeleton: skeletonMixamo, Skinning: "core", Animations: "human", Bones: 52},
	{Name: "quadruped", Skeleton: skeletonQuadruped, Skinning: "shell", Animations: "canine", Bones: 33},
	{Name: "bird", Skeleton: skeletonQuadruped, Skinning: "shell", Animations: "bird", Bones: 33},
	{Name: "dragon", Skeleton: skeletonDragon, Skinning: "shell", Animations: "canine", Bones: 39},
	{Name: "kaiju", Skeleton: skeletonQuadruped, Skinning: "shell", Animations: "human", Bones: 33},
}

// RigTemplateNamed returns the template called name.
func RigTemplateNamed(name string) (RigTemplate, bool) {
	for _, t := range RigTemplates {
		if t.Name == name {
			return t, true
		}
	}
	return RigTemplate{}, false
}

// Bone is one entry of rodinrig_hierarchy.
type Bone struct {
	Name   string `json:"name"`
	Parent int    `json:"parent"`
}

// ParseSkeleton turns a hierarchy text (one bone per line, depth by leading
// "-" characters) into rodinrig_hierarchy the way the web does
// (main.fmt.js:55923-55950): blank lines are skipped, a bone's parent is the
// nearest earlier line of smaller depth (-1 at the top), and a bone named
// "pose" (after an optional "...:" prefix and "mixamorig") is left out while
// its children still attach through it.
func ParseSkeleton(text string) []Bone {
	type frame struct{ depth, index int }
	var out []Bone
	var stack []frame
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		depth := len(line) - len(strings.TrimLeft(line, "-"))
		name := strings.TrimSpace(strings.TrimLeft(line, "-"))
		if name == "" {
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			stack = stack[:len(stack)-1]
		}
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1].index
		}
		if skippedBone(name) {
			stack = append(stack, frame{depth, parent})
			continue
		}
		stack = append(stack, frame{depth, len(out)})
		out = append(out, Bone{Name: name, Parent: parent})
	}
	return out
}

// skippedBone is the web's Mn (main.fmt.js:55402): the bone "pose", after
// the last ":" and a leading "mixamorig", ignoring spaces, "_", "-" and case.
func skippedBone(name string) bool {
	parts := strings.Split(name, ":")
	last := parts[len(parts)-1]
	if strings.HasPrefix(strings.ToLower(last), "mixamorig") {
		last = last[len("mixamorig"):]
	}
	last = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, last)
	return strings.EqualFold(last, "pose")
}
