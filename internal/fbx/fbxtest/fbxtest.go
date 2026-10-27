// Package fbxtest writes small FBX files (binary and text 7400) for tests:
// models with transform properties, skin clusters and connections.
package fbxtest

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

type Node struct {
	name     string
	props    []any
	children []*Node
}

func N(name string, props []any, children ...*Node) *Node {
	return &Node{name: name, props: props, children: children}
}

func WriteBinary(nodes []*Node) []byte {
	var b bytes.Buffer
	b.WriteString("Kaydara FBX Binary  \x00\x1a\x00")
	binary.Write(&b, binary.LittleEndian, uint32(7400))
	for _, n := range nodes {
		writeNode(&b, n)
	}
	b.Write(make([]byte, 13)) // null record
	end := b.Len() - 13
	// The loader stops 176 bytes before the end of the file.
	for b.Len() < end+176 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func writeNode(b *bytes.Buffer, n *Node) { writeNodeAt(b, n, b.Len()) }

// writeNodeAt writes n into kids; base is the absolute file offset where n
// starts.
func writeNodeAt(kids *bytes.Buffer, n *Node, base int) {
	var props bytes.Buffer
	for _, p := range n.props {
		writeProp(&props, p)
	}
	var inner bytes.Buffer
	if len(n.children) > 0 {
		for _, c := range n.children {
			writeNodeAt(&inner, c, base+13+len(n.name)+props.Len()+inner.Len())
		}
		inner.Write(make([]byte, 13))
	}
	endOff := base + 13 + len(n.name) + props.Len() + inner.Len()
	binary.Write(kids, binary.LittleEndian, uint32(endOff))
	binary.Write(kids, binary.LittleEndian, uint32(len(n.props)))
	binary.Write(kids, binary.LittleEndian, uint32(props.Len()))
	kids.WriteByte(byte(len(n.name)))
	kids.WriteString(n.name)
	kids.Write(props.Bytes())
	kids.Write(inner.Bytes())
}

func writeProp(b *bytes.Buffer, p any) {
	le := binary.LittleEndian
	switch v := p.(type) {
	case int64:
		b.WriteByte('L')
		binary.Write(b, le, v)
	case int32:
		b.WriteByte('I')
		binary.Write(b, le, v)
	case float64:
		b.WriteByte('D')
		binary.Write(b, le, v)
	case string:
		b.WriteByte('S')
		binary.Write(b, le, uint32(len(v)))
		b.WriteString(v)
	case []float64:
		b.WriteByte('d')
		binary.Write(b, le, uint32(len(v)))
		binary.Write(b, le, uint32(0))
		binary.Write(b, le, uint32(len(v)*8))
		for _, f := range v {
			binary.Write(b, le, f)
		}
	default:
		panic(fmt.Sprintf("prop %T", p))
	}
}

// writeText writes the same nodes as a text FBX (tab indentation, as the
// loader needs).
func WriteText(nodes []*Node) []byte {
	var b strings.Builder
	b.WriteString("; FBX 7.4.0 project file\n")
	b.WriteString("FBXHeaderExtension:  {\n\tFBXHeaderVersion: 1003\n\tFBXVersion: 7400\n}\n")
	for _, n := range nodes {
		writeTextNode(&b, n, 0)
	}
	return []byte(b.String())
}

func textProp(p any) string {
	switch v := p.(type) {
	case int64:
		return fmt.Sprint(v)
	case int32:
		return fmt.Sprint(v)
	case float64:
		return formatFloat(v)
	case string:
		// "Name\x00\x01Model" is written "Model::Name" in text files.
		if i := strings.Index(v, "\x00\x01"); i >= 0 {
			v = v[i+2:] + "::" + v[:i]
		}
		return `"` + v + `"`
	}
	panic(fmt.Sprintf("prop %T", p))
}

func formatFloat(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprint(v)
}

func writeTextNode(b *strings.Builder, n *Node, depth int) {
	tabs := strings.Repeat("\t", depth)
	if arr, ok := single(n); ok {
		fmt.Fprintf(b, "%s%s: *%d {\n%s\ta: %s\n%s}\n", tabs, n.name, len(arr), tabs, joinFloats(arr), tabs)
		return
	}
	parts := make([]string, len(n.props))
	for i, p := range n.props {
		parts[i] = textProp(p)
	}
	if len(n.children) == 0 && n.name != "Properties70" && n.name != "Geometry" && n.name != "Deformer" && n.name != "Model" {
		if n.name == "C" || n.name == "P" {
			// As exporters write them: quoted strings joined by ", ", numbers by ",".
			line := ""
			for i, p := range parts {
				switch {
				case i == 0:
					line = p
				case strings.HasPrefix(p, `"`) && n.name == "P":
					line += ", " + p
				default:
					line += "," + p
				}
			}
			fmt.Fprintf(b, "%s%s: %s\n", tabs, n.name, line)
		} else {
			fmt.Fprintf(b, "%s%s: %s\n", tabs, n.name, strings.Join(parts, ", "))
		}
		return
	}
	fmt.Fprintf(b, "%s%s: %s {\n", tabs, n.name, strings.Join(parts, ", "))
	for _, c := range n.children {
		writeTextNode(b, c, depth+1)
	}
	fmt.Fprintf(b, "%s}\n", tabs)
}

func single(n *Node) ([]float64, bool) {
	if len(n.props) == 1 {
		a, ok := n.props[0].([]float64)
		return a, ok
	}
	return nil, false
}

func joinFloats(a []float64) string {
	s := make([]string, len(a))
	for i, f := range a {
		s[i] = formatFloat(f)
	}
	return strings.Join(s, ",")
}

// --- skeleton fixtures ---

type Bone struct {
	ID     int64
	Name   string
	Type   string // LimbNode, Null, Root, Mesh
	Parent int64  // 0: the scene
	T, R   [3]float64
	Pre    *[3]float64
	Order  int32 // RotationOrder; 0 omitted
	Scale  *[3]float64
	// Inherit is the InheritType; 0 omitted.
	Inherit int32
}

func props70(b Bone) *Node {
	vec := func(name, typ, typ2 string, v [3]float64) *Node {
		return N("P", []any{name, typ, typ2, "A", v[0], v[1], v[2]})
	}
	ps := []*Node{vec("Lcl Translation", "Lcl Translation", "", b.T), vec("Lcl Rotation", "Lcl Rotation", "", b.R)}
	if b.Pre != nil {
		ps = append(ps, N("P", []any{"PreRotation", "Vector3D", "Vector", "", b.Pre[0], b.Pre[1], b.Pre[2]}))
	}
	if b.Order != 0 {
		ps = append(ps, N("P", []any{"RotationOrder", "enum", "", "", b.Order}))
	}
	if b.Scale != nil {
		ps = append(ps, vec("Lcl Scaling", "Lcl Scaling", "", *b.Scale))
	}
	if b.Inherit != 0 {
		ps = append(ps, N("P", []any{"InheritType", "enum", "", "", b.Inherit}))
	}
	return N("Properties70", nil, ps...)
}

// SkeletonFile builds the nodes of a file with bones and, for each
// (cluster id, model id) pair, a skin cluster linking the model.
func SkeletonFile(bones []Bone, clusters [][2]int64) []*Node {
	var objects, conns []*Node
	for _, b := range bones {
		objects = append(objects, N("Model", []any{b.ID, b.Name + "\x00\x01Model", b.Type}, N("Version", []any{int32(232)}), props70(b)))
		conns = append(conns, N("C", []any{"OO", b.ID, b.Parent}))
	}
	if len(clusters) > 0 {
		const geom, skinID = int64(900001), int64(900002)
		objects = append(objects, N("Geometry", []any{geom, "\x00\x01Geometry", "Mesh"}))
		objects = append(objects, N("Deformer", []any{skinID, "\x00\x01Deformer", "Skin"}))
		conns = append(conns, N("C", []any{"OO", skinID, geom}))
		for _, c := range clusters {
			objects = append(objects, N("Deformer", []any{c[0], "\x00\x01SubDeformer", "Cluster"},
				N("TransformLink", []any{[]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}})))
			conns = append(conns, N("C", []any{"OO", c[0], skinID}), N("C", []any{"OO", c[1], c[0]}))
		}
	}
	return []*Node{
		N("GlobalSettings", nil, N("Version", []any{int32(1000)})),
		N("Objects", nil, objects...),
		N("Connections", nil, conns...),
	}
}

func V(x, y, z float64) [3]float64 { return [3]float64{x, y, z} }

func PV(x, y, z float64) *[3]float64 { a := V(x, y, z); return &a }

// HumanBones is a Mixamo-named human skeleton (cm, Y up) under an
// Armature node, with ids above and below 2^32, a pre-rotation, a
// rotation order and a scaled root.
func HumanBones() []Bone {
	const big = int64(5000000000)
	return []Bone{
		{ID: 1000, Name: "Armature", Type: "Null", Parent: 0, Scale: PV(1, 1, 1)},
		{ID: big + 1, Name: "mixamorig:Hips", Type: "LimbNode", Parent: 1000, T: V(0, 100, 0)},
		{ID: 2001, Name: "mixamorig:Spine", Type: "LimbNode", Parent: big + 1, T: V(0, 10, 0), R: V(4, 0, 0)},
		{ID: 2002, Name: "mixamorig:Spine1", Type: "LimbNode", Parent: 2001, T: V(0, 12, 0)},
		{ID: big + 3, Name: "mixamorig:Spine2", Type: "LimbNode", Parent: 2002, T: V(0, 12, 0), R: V(-3, 0, 0)},
		{ID: 2004, Name: "mixamorig:Neck", Type: "LimbNode", Parent: big + 3, T: V(0, 15, 0)},
		{ID: 2005, Name: "mixamorig:Head", Type: "LimbNode", Parent: 2004, T: V(0, 10, 0), R: V(-5, 0, 0)},
		{ID: 3001, Name: "mixamorig:LeftShoulder", Type: "LimbNode", Parent: big + 3, T: V(5, 10, 0), R: V(0, 0, -90)},
		{ID: big + 11, Name: "mixamorig:LeftArm", Type: "LimbNode", Parent: 3001, T: V(0, 12, 0)},
		{ID: 3003, Name: "mixamorig:LeftForeArm", Type: "LimbNode", Parent: big + 11, T: V(0, 28, 0), R: V(10, 20, 30), Order: 4},
		{ID: 3004, Name: "mixamorig:LeftHand", Type: "LimbNode", Parent: 3003, T: V(0, 25, 0)},
		{ID: 4001, Name: "mixamorig:RightShoulder", Type: "LimbNode", Parent: big + 3, T: V(-5, 10, 0), R: V(0, 0, 90)},
		{ID: 4002, Name: "mixamorig:RightArm", Type: "LimbNode", Parent: 4001, T: V(0, 12, 0), Pre: PV(0, 0, 5)},
		{ID: 4003, Name: "mixamorig:RightForeArm", Type: "LimbNode", Parent: 4002, T: V(0, 28, 0)},
		{ID: 4004, Name: "mixamorig:RightHand", Type: "LimbNode", Parent: 4003, T: V(0, 25, 0)},
		{ID: 5001, Name: "mixamorig:LeftUpLeg", Type: "LimbNode", Parent: big + 1, T: V(9, -5, 0), R: V(0, 0, 180)},
		{ID: 5002, Name: "mixamorig:LeftLeg", Type: "LimbNode", Parent: 5001, T: V(0, 42, 0)},
		{ID: 5003, Name: "mixamorig:LeftFoot", Type: "LimbNode", Parent: 5002, T: V(0, 40, 0), R: V(-60, 0, 0)},
		{ID: 5004, Name: "mixamorig:LeftToeBase", Type: "LimbNode", Parent: 5003, T: V(0, 12, 0)},
		{ID: 6001, Name: "mixamorig:RightUpLeg", Type: "LimbNode", Parent: big + 1, T: V(-9, -5, 0), R: V(0, 0, 180)},
		{ID: 6002, Name: "mixamorig:RightLeg", Type: "LimbNode", Parent: 6001, T: V(0, 42, 0)},
		{ID: 6003, Name: "mixamorig:RightFoot", Type: "LimbNode", Parent: 6002, T: V(0, 40, 0), R: V(-60, 0, 0)},
		{ID: 6004, Name: "mixamorig:RightToeBase", Type: "LimbNode", Parent: 6003, T: V(0, 12, 0)},
	}
}

// Tricky is a file that exercises the loader's rules: a "pose" bone between
// bones, a Null between bones, a Null that a skin cluster makes a bone, a
// model in two clusters (nested copies), inherit types 1 and 2, rotation
// order 6, a non-uniformly scaled parent, a negative id and post-order file
// ids.
func Tricky() []*Node {
	bones := []Bone{
		{ID: 70, Name: "Root", Type: "Root", Parent: 0, T: V(1, 2, 3), R: V(10, 0, 0), Scale: PV(2, 1, 1)},
		{ID: 60, Name: "mixamorig:pose", Type: "LimbNode", Parent: 70, T: V(0, 5, 0), R: V(0, 30, 0)},
		{ID: 50, Name: "Chest", Type: "LimbNode", Parent: 60, T: V(0, 4, 0), R: V(0, 0, 15), Order: 6, Inherit: 1},
		{ID: 40, Name: "Group", Type: "Null", Parent: 50, T: V(1, 1, 0), R: V(5, 5, 5), Inherit: 2},
		{ID: -30, Name: "Arm", Type: "LimbNode", Parent: 40, T: V(3, 0, 0), R: V(0, 0, -80)},
		{ID: 20, Name: "SkinOnly", Type: "Null", Parent: -30, T: V(0, 6, 0)},
		{ID: 10, Name: "Twice", Type: "Null", Parent: 20, T: V(0, 2, 0), R: V(20, 0, 0)},
		{ID: 9, Name: "Tip", Type: "LimbNode", Parent: 10, T: V(0, 1, 0)},
		{ID: 8, Name: "Leg", Type: "LimbNode", Parent: 70, T: V(0, -3, 0), R: V(180, 0, 0)},
	}
	return SkeletonFile(bones, [][2]int64{{800, 20}, {801, 10}, {802, 10}})
}

// RigFile is a gzipped DRCRIG01 rig file whose header lists the bones
// (name, parent) with their bone inverse matrices (column-major).
func RigFile(names []string, parents []int, inverses [][16]float64) []byte {
	type bone struct {
		Name   string `json:"name"`
		Parent int    `json:"parent"`
	}
	var bs []bone
	var inv [][]float64
	for i := range names {
		bs = append(bs, bone{names[i], parents[i]})
		inv = append(inv, append([]float64(nil), inverses[i][:]...))
	}
	j, _ := json.Marshal(map[string]any{"version": 1, "bones": bs, "boneInverses": inv})
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

// Human is the binary FBX file of HumanBones.
func Human() []byte { return WriteBinary(SkeletonFile(HumanBones(), nil)) }
