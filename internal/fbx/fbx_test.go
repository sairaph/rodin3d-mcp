package fbx

import (
	"math"
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fbx/fbxtest"
)

// want is a bone as the web's own loader and import (three.js r180 FBXLoader
// and the rigging panel's kn, run in the browser on these fixture files)
// read it: name, parent, world position, and world Y axis.
type want struct {
	name   string
	parent int
	pos, y [3]float64
}

var webHuman = []want{
	{"mixamorig:Hips", -1, [3]float64{0, 100, 0}, [3]float64{0, 1, 0}},
	{"mixamorig:Spine", 0, [3]float64{0, 110, 0}, [3]float64{0, 0.9975640502598241, 0.0697564737441253}},
	{"mixamorig:Spine1", 1, [3]float64{0, 121.97076860311789, 0.8370776849295036}, [3]float64{0, 0.9975640502598241, 0.0697564737441253}},
	{"mixamorig:Spine2", 2, [3]float64{0, 133.94153720623578, 1.6741553698590081}, [3]float64{0, 0.9998476951563909, 0.017452406437283498}},
	{"mixamorig:Neck", 3, [3]float64{0, 148.93925263358162, 1.9359414664182606}, [3]float64{0, 0.9998476951563912, 0.017452406437283505}},
	{"mixamorig:Head", 4, [3]float64{0, 158.93772958514552, 2.1104655307910956}, [3]float64{0, 0.9975640502598242, -0.0697564737441253}},
	{"mixamorig:LeftShoulder", 3, [3]float64{5, 143.94001415779968, 1.8486794342318438}, [3]float64{0.9999999999999997, 2.219502362578858e-16, 7.344131230735873e-18}},
	{"mixamorig:LeftArm", 6, [3]float64{16.999999999999993, 143.9400141577997, 1.8486794342318444}, [3]float64{0.9999999999999996, 2.2195023625788577e-16, 7.34413123073587e-18}},
	{"mixamorig:LeftForeArm", 7, [3]float64{44.99999999999997, 143.9400141577997, 1.848679434231845}, [3]float64{0.8528685319524426, 0.4128975031437609, 0.3195792845334822}},
	{"mixamorig:LeftHand", 8, [3]float64{66.32171329881105, 154.26245173639373, 9.838161547568891}, [3]float64{0.8528685319524428, 0.412897503143761, 0.3195792845334823}},
	{"mixamorig:RightShoulder", 3, [3]float64{-5, 143.94001415779968, 1.8486794342318438}, [3]float64{-0.9999999999999997, 2.219502362578858e-16, 7.344131230735873e-18}},
	{"mixamorig:RightArm", 10, [3]float64{-16.999999999999993, 143.9400141577997, 1.8486794342318444}, [3]float64{-0.9961946980917451, -0.08714246850588915, -0.0015210774457754472}},
	{"mixamorig:RightForeArm", 11, [3]float64{-44.893451546568855, 141.5000250396348, 1.8060892657501322}, [3]float64{-0.9961946980917451, -0.08714246850588915, -0.001521077445775447}},
	{"mixamorig:RightHand", 12, [3]float64{-69.7983189988625, 139.3214633269876, 1.768062329605746}, [3]float64{-0.9961946980917451, -0.08714246850588916, -0.0015210774457754472}},
	{"mixamorig:LeftUpLeg", 0, [3]float64{9, 95, 0}, [3]float64{-1.2246467991473532e-16, -1, 0}},
	{"mixamorig:LeftLeg", 14, [3]float64{8.999999999999995, 53, 0}, [3]float64{-1.2246467991473532e-16, -1, 0}},
	{"mixamorig:LeftFoot", 15, [3]float64{8.99999999999999, 13, 0}, [3]float64{-6.123233995736766e-17, -0.5, -0.8660254037844385}},
	{"mixamorig:LeftToeBase", 16, [3]float64{8.99999999999999, 6.999999999999997, -10.392304845413264}, [3]float64{-6.123233995736765e-17, -0.5, -0.8660254037844385}},
	{"mixamorig:RightUpLeg", 0, [3]float64{-9, 95, 0}, [3]float64{-1.2246467991473532e-16, -1, 0}},
	{"mixamorig:RightLeg", 18, [3]float64{-9.000000000000005, 53, 0}, [3]float64{-1.2246467991473532e-16, -1, 0}},
	{"mixamorig:RightFoot", 19, [3]float64{-9.00000000000001, 13, 0}, [3]float64{-6.123233995736766e-17, -0.5, -0.8660254037844385}},
	{"mixamorig:RightToeBase", 20, [3]float64{-9.000000000000007, 7.000000000000002, -10.392304845413264}, [3]float64{-6.123233995736765e-17, -0.5, -0.8660254037844385}},
}

var webTricky = []want{
	{"Root", -1, [3]float64{1, 2, 3}, [3]float64{0, 0.9848077530122079, 0.17364817766693033}},
	{"Leg", 0, [3]float64{1, -0.9544232590366235, 2.479055466999209}, [3]float64{0, -0.9848077530122079, -0.17364817766693022}},
	{"Chest", 0, [3]float64{0.9999999999999998, 10.86326977710987, 4.5628335990023725}, [3]float64{-0.5836614518517759, 0.9205346447860262, 0.3419337420363737}},
	{"Arm", 2, [3]float64{8.706766423295427, 13.62278194534509, 2.346251075549362}, [3]float64{1.0465837962627533, 0.3752468312137818, -0.30598175866985605}},
	{"SkinOnly", 3, [3]float64{14.986269200871945, 15.87426293262778, 0.5103605235302273}, [3]float64{1.0465837962627529, 0.37524683121378183, -0.30598175866985605}},
	{"Twice", 4, [3]float64{17.079436793397456, 16.62475659505534, -0.10160299380948457}, [3]float64{1.3731338584896868, 0.2908174181414214, -0.060115980275844444}},
	{"Tip", 5, [3]float64{18.45257065188714, 16.915574013196764, -0.1617189740853303}, [3]float64{1.3731338584896866, 0.2908174181414214, -0.06011598027584433}},
}

func check(t *testing.T, label string, got []Bone, w []want) {
	t.Helper()
	if len(got) != len(w) {
		t.Fatalf("%s: %d bones, want %d", label, len(got), len(w))
	}
	for i, b := range got {
		x := w[i]
		if b.Name != x.name || b.Parent != x.parent {
			t.Errorf("%s #%d: %s/%d, want %s/%d", label, i, b.Name, b.Parent, x.name, x.parent)
		}
		m := b.Transform
		for k, v := range [6]float64{m[12], m[13], m[14], m[4], m[5], m[6]} {
			e := append(x.pos[:], x.y[:]...)[k]
			if math.Abs(v-e) > 1e-9 {
				t.Errorf("%s %s element %d = %v, want %v", label, b.Name, k, v, e)
			}
		}
	}
}

// The bones of the fixtures come out as the web reads them, binary and
// text alike: order, parents, world matrices (including pre-rotation,
// rotation orders 4 and 6, inherit types 1 and 2, a non-uniform scale,
// a pose bone, a null made a bone by a skin cluster, a model in two
// clusters, ids above 2^32 and below 0).
func TestSkeletonMatchesTheWeb(t *testing.T) {
	for label, data := range map[string][]byte{
		"human binary": fbxtest.WriteBinary(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil)),
		"human text":   fbxtest.WriteText(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil)),
	} {
		got, err := Skeleton(data)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		check(t, label, got, webHuman)
	}
	for label, data := range map[string][]byte{
		"tricky binary": fbxtest.WriteBinary(fbxtest.Tricky()),
		"tricky text":   fbxtest.WriteText(fbxtest.Tricky()),
	} {
		got, err := Skeleton(data)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		check(t, label, got, webTricky)
	}
}

// The files the web refuses are refused with its reason.
func TestSkeletonRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"no bones": {fbxtest.WriteBinary(fbxtest.SkeletonFile([]fbxtest.Bone{{ID: 1, Name: "Empty", Type: "Null"}}, nil)), "no usable skeleton bones"},
		"unnamed":  {fbxtest.WriteBinary(fbxtest.SkeletonFile([]fbxtest.Bone{{ID: 1, Name: "", Type: "LimbNode"}}, nil)), "unnamed bone"},
		"not fbx":  {[]byte("solid cube\nfacet normal 0 0 1\n"), "not an FBX file"},
		"old":      {oldVersion(fbxtest.WriteBinary(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil))), "6100 is not supported"},
	} {
		if _, err := Skeleton(c.data); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
