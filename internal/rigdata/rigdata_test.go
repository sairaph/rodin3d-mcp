package rigdata

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fbx"
)

// rigFile builds a gzipped DRCRIG01 file whose header lists bones (name,
// parent) with the bind matrices binds.
func rigFile(t *testing.T, names []string, parents []int, binds []fbx.Mat4) []byte {
	t.Helper()
	type bone struct {
		Name   string `json:"name"`
		Parent int    `json:"parent"`
	}
	h := map[string]any{"version": 1}
	var bs []bone
	var inv [][]float64
	for i := range names {
		bs = append(bs, bone{names[i], parents[i]})
		m := binds[i].Invert()
		inv = append(inv, m[:])
	}
	h["bones"], h["boneInverses"] = bs, inv
	j, _ := json.Marshal(h)
	var raw bytes.Buffer
	raw.WriteString("DRCRIG01")
	binary.Write(&raw, binary.LittleEndian, uint32(len(j)))
	binary.Write(&raw, binary.LittleEndian, uint32(len(j)))
	raw.Write(j)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(raw.Bytes())
	w.Close()
	return gz.Bytes()
}

func near(a, b fbx.Mat4, tol float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > tol {
			return false
		}
	}
	return true
}

// A chain with a branch, built from rotations, so frames carry a roll.
func chain() []fbx.Bone {
	rot := func(x, y, z float64) fbx.Quat {
		return fbx.QuatFromRotation(fbx.RotationFromEuler(x, y, z, "XYZ"))
	}
	one := fbx.Vec3{X: 1, Y: 1, Z: 1}
	return []fbx.Bone{
		{Name: "mixamorig:Hips", Parent: -1, Transform: fbx.Compose(fbx.Vec3{Y: 1}, rot(0.1, 0.2, 0), one)},
		{Name: "mixamorig:Spine", Parent: 0, Transform: fbx.Compose(fbx.Vec3{Y: 1.2, Z: 0.05}, rot(0.05, 0.3, 0.1), one)},
		{Name: "mixamorig:Head", Parent: 1, Transform: fbx.Compose(fbx.Vec3{Y: 1.6, Z: 0.1}, rot(-0.1, 0.4, 0.2), one)},
		{Name: "mixamorig:LeftArm", Parent: 1, Transform: fbx.Compose(fbx.Vec3{X: 0.3, Y: 1.4}, rot(0, 0, -1.5), one)},
		{Name: "mixamorig:LeftHand", Parent: 3, Transform: fbx.Compose(fbx.Vec3{X: 0.8, Y: 1.4, Z: 0.1}, rot(0.2, 0, -1.4), one)},
	}
}

// rig builds the joints of a generated skeleton that is the imported one
// moved by turn.
func rig(t *testing.T, imported []fbx.Bone, turn fbx.Mat4) ([]Joint, []fbx.Mat4) {
	t.Helper()
	var names []string
	var parents []int
	var binds []fbx.Mat4
	for _, b := range imported {
		names = append(names, b.Name)
		parents = append(parents, b.Parent)
		binds = append(binds, fbx.Mul(turn, b.Transform))
	}
	h, err := DecodeHeader(rigFile(t, names, parents, binds))
	if err != nil {
		t.Fatal(err)
	}
	joints, err := Joints(h)
	if err != nil {
		t.Fatal(err)
	}
	return joints, binds
}

func yAxis(m []float64) fbx.Vec3 { return fbx.Vec3{X: m[4], Y: m[5], Z: m[6]}.Normalize() }

// When the generated skeleton is the imported one moved, the reskin gives
// the generated bind transforms back, and a bone's own roll about its Y
// axis is kept. When it is also turned, a bone with two children gets the
// whole turn (the fit fixes the roll from the second child), and every
// bone's Y axis lies along its generated bone.
func TestReskinFrames(t *testing.T) {
	imported := chain()
	joints, binds := rig(t, imported, fbx.Translation(fbx.Vec3{X: 0.2, Y: -0.1, Z: 0.3}))
	got, err := Reskin(imported, joints)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(imported) {
		t.Fatalf("%d bones", len(got))
	}
	for i, b := range got {
		if want := strings.TrimPrefix(imported[i].Name, "mixamorig:"); b.Name != want || b.Parent != imported[i].Parent || b.Length != nil {
			t.Errorf("#%d %s/%d", i, b.Name, b.Parent)
		}
		var m fbx.Mat4
		copy(m[:], b.Transform)
		if !near(m, binds[i], 1e-9) {
			t.Errorf("%s: %v, want %v", b.Name, m, binds[i])
		}
	}
	// A frame turned about its own Y axis keeps that roll.
	rolled := append([]fbx.Bone(nil), imported...)
	r := fbx.RotationFromEuler(0, 0.9, 0, "XYZ")
	rolled[2].Transform = fbx.Mul(rolled[2].Transform, r)
	got, err = Reskin(rolled, joints)
	if err != nil {
		t.Fatal(err)
	}
	var head fbx.Mat4
	copy(head[:], got[2].Transform)
	if want := fbx.Mul(binds[2], r); !near(head, want, 1e-9) {
		t.Errorf("rolled head %v, want %v", head, want)
	}

	turn := fbx.Mul(fbx.Translation(fbx.Vec3{X: 0.2}), fbx.RotationFromEuler(0.3, -0.7, 0.4, "XYZ"))
	joints, binds = rig(t, imported, turn)
	if got, err = Reskin(imported, joints); err != nil {
		t.Fatal(err)
	}
	var spine fbx.Mat4
	copy(spine[:], got[1].Transform)
	if !near(spine, binds[1], 1e-9) {
		t.Errorf("spine %v, want %v", spine, binds[1])
	}
	for i, b := range got {
		if d := yAxis(b.Transform).DistanceTo(yAxis(binds[i][:])); d > 1e-9 {
			t.Errorf("%s: Y axis off by %g", b.Name, d)
		}
	}
}

// The editor's joint list: pose and nameless bones are left out, labels
// lose the namespace and mixamorig prefix,
// a parent is the nearest listed ancestor wherever it stands, lengths stay.
func TestJoints(t *testing.T) {
	l := 0.25
	h := &Header{}
	add := func(name string, parent int, length *float64) {
		h.Bones = append(h.Bones, struct {
			Name   string   `json:"name"`
			Parent int      `json:"parent"`
			Length *float64 `json:"length"`
		}{name, parent, length})
		id := fbx.Identity()
		h.BoneInverses = append(h.BoneInverses, id[:])
	}
	add("Armature:Hips", -1, nil)
	add("mixamorig:pose", 0, nil)
	add("Chest", 4, &l) // its parent comes after it
	add("mixamorig:", 1, nil)
	add("Spine", 1, nil)
	js, err := Joints(h)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		label  string
		parent int
	}
	want := []row{{"Hips", -1}, {"Chest", 2}, {"Spine", 0}}
	if len(js) != len(want) {
		t.Fatalf("%+v", js)
	}
	for i, j := range js {
		if j.Label != want[i].label || j.Parent != want[i].parent {
			t.Errorf("#%d %s/%d, want %+v", i, j.Label, j.Parent, want[i])
		}
	}
	if js[1].Length == nil || *js[1].Length != l {
		t.Errorf("length %v", js[1].Length)
	}
	bad := -1.0
	h.Bones[2].Length = &bad
	if _, err := Joints(h); err == nil {
		t.Error("negative length accepted")
	}
}

// The reskin refuses what the web's refuses: an FBX bone the rig lacks,
// and a hierarchy that does not match.
func TestReskinRefusals(t *testing.T) {
	imported := chain()
	var joints []Joint
	for _, b := range imported {
		joints = append(joints, Joint{Label: strings.TrimPrefix(b.Name, "mixamorig:"), Parent: b.Parent, Transform: b.Transform})
	}
	if _, err := Reskin(append(append([]fbx.Bone(nil), imported...), fbx.Bone{Name: "Tail", Parent: 0, Transform: fbx.Identity()}), joints); err == nil || !strings.Contains(err.Error(), "cannot match imported bone: Tail") {
		t.Errorf("extra bone: %v", err)
	}
	moved := append([]Joint(nil), joints...)
	moved[4].Parent = 0 // the hand hangs from the hips in the rig
	if _, err := Reskin(imported, moved); err == nil || !strings.Contains(err.Error(), "parent mismatch") {
		t.Errorf("parent mismatch: %v", err)
	}
	dup := append(append([]Joint(nil), joints...), Joint{Label: "Head", Parent: 1, Transform: fbx.Identity()})
	if _, err := Reskin(imported, dup); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := DecodeHeader([]byte("not a rig")); err == nil {
		t.Error("not a rig accepted")
	}
}
