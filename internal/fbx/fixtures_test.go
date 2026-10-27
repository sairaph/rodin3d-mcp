package fbx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fbx/fbxtest"
)

type dumpBone struct {
	Name      string    `json:"name"`
	Parent    int       `json:"parent"`
	Transform []float64 `json:"transform"`
}

func dump(b []Bone) []dumpBone {
	out := make([]dumpBone, len(b))
	for i, x := range b {
		out[i] = dumpBone{x.Name, x.Parent, x.Transform[:]}
	}
	return out
}

// TestDumpFixtures writes the fixture files and this package's reading of
// them to $FBX_OUT, for a comparison with the web's own loader. It runs
// only when FBX_OUT is set.
func TestDumpFixtures(t *testing.T) {
	dir := os.Getenv("FBX_OUT")
	if dir == "" {
		t.Skip("FBX_OUT not set")
	}
	files := map[string][]byte{
		"human.fbx":       fbxtest.WriteBinary(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil)),
		"human-text.fbx":  fbxtest.WriteText(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil)),
		"tricky.fbx":      fbxtest.WriteBinary(fbxtest.Tricky()),
		"tricky-text.fbx": fbxtest.WriteText(fbxtest.Tricky()),
		"nobones.fbx":     fbxtest.WriteBinary(fbxtest.SkeletonFile([]fbxtest.Bone{{ID: 1, Name: "Empty", Type: "Null"}}, nil)),
		"unnamed.fbx":     fbxtest.WriteBinary(fbxtest.SkeletonFile([]fbxtest.Bone{{ID: 1, Name: "", Type: "LimbNode"}}, nil)),
		"notfbx.fbx":      []byte("solid cube\nfacet normal 0 0 1\n"),
		"old.fbx":         oldVersion(fbxtest.WriteBinary(fbxtest.SkeletonFile(fbxtest.HumanBones(), nil))),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		bones, err := Skeleton(data)
		out := map[string]any{"bones": dump(bones)}
		if err != nil {
			out = map[string]any{"error": err.Error()}
		}
		j, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(filepath.Join(dir, name+".go.json"), j, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("FBX_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bones, err := Skeleton(data)
		out := map[string]any{"bones": dump(bones)}
		if err != nil {
			out = map[string]any{"error": err.Error()}
		}
		j, _ := json.MarshalIndent(out, "", " ")
		_ = os.WriteFile(filepath.Join(dir, filepath.Base(path)+".go.json"), j, 0o600)
	}
}

// oldVersion marks a binary file as FBX 6100.
func oldVersion(b []byte) []byte {
	out := append([]byte(nil), b...)
	out[23], out[24], out[25], out[26] = 0xd4, 0x17, 0, 0 // 6100
	return out
}
