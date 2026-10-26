package glb

import (
	"encoding/binary"
	"slices"
	"testing"
)

func glbOf(json string) []byte {
	for len(json)%4 != 0 {
		json += " "
	}
	b := make([]byte, 20, 20+len(json))
	copy(b, "glTF")
	binary.LittleEndian.PutUint32(b[4:], 2)
	binary.LittleEndian.PutUint32(b[8:], uint32(20+len(json)))
	binary.LittleEndian.PutUint32(b[12:], uint32(len(json)))
	copy(b[16:], "JSON")
	return append(b, json...)
}

// The part names of a Bang parts file, as live 2026-10-27: a ROOT node
// without a mesh whose children root.0 ... carry one mesh each; three.js
// order (depth first, children in order) and the default scene.
func TestMeshNodeNames(t *testing.T) {
	live := `{"asset":{"version":"2.0"},"scene":0,"scenes":[{"name":"Scene","nodes":[0]}],
		"nodes":[{"name":"ROOT","children":[1,2,3]},{"name":"root.0","mesh":0},{"name":"root.1","mesh":1},{"name":"root.2","mesh":2,"children":[4]},{"name":"tip","mesh":3}],
		"meshes":[{"primitives":[]},{"primitives":[]},{"primitives":[]},{"primitives":[]}]}`
	f, err := Parse(glbOf(live))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.MeshNodeNames(); !slices.Equal(got, []string{"root.0", "root.1", "root.2", "tip"}) {
		t.Fatalf("names %v", got)
	}
	// Another scene is the default: only its nodes count; no scenes: none.
	other := `{"scene":1,"scenes":[{"nodes":[0]},{"nodes":[1]}],"nodes":[{"name":"a","mesh":0},{"name":"b","mesh":0}],"meshes":[{"primitives":[]}]}`
	if f, _ = Parse(glbOf(other)); !slices.Equal(f.MeshNodeNames(), []string{"b"}) {
		t.Fatalf("scene 1: %v", f.MeshNodeNames())
	}
	if f, _ = Parse(glbOf(`{"nodes":[{"name":"a","mesh":0}]}`)); len(f.MeshNodeNames()) != 0 {
		t.Fatalf("no scene: %v", f.MeshNodeNames())
	}
}
