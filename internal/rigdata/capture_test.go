package rigdata

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fbx"
)

// TestAgainstCapture compares Reskin with a captured web run: $RIG_CAPTURE
// holds the FBX the web imported (skeleton.fbx), the rig file of its first
// call (rig1.rodinrig) and the body of its second call (call2.json). It
// runs only when RIG_CAPTURE is set.
func TestAgainstCapture(t *testing.T) {
	dir := os.Getenv("RIG_CAPTURE")
	if dir == "" {
		t.Skip("RIG_CAPTURE not set")
	}
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	bones, err := fbx.Skeleton(read("skeleton.fbx"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := DecodeHeader(read("rig1.rodinrig"))
	if err != nil {
		t.Fatal(err)
	}
	joints, err := Joints(h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Reskin(bones, joints)
	if err != nil {
		t.Fatal(err)
	}
	var call struct {
		Settings struct {
			Hierarchy []Bone `json:"rodinrig_hierarchy"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(read("call2.json"), &call); err != nil {
		t.Fatal(err)
	}
	want := call.Settings.Hierarchy
	if len(got) != len(want) {
		t.Fatalf("%d bones, want %d", len(got), len(want))
	}
	maxd := 0.0
	for i := range got {
		g, w := got[i], want[i]
		if g.Name != w.Name || g.Parent != w.Parent || (g.Length == nil) != (w.Length == nil) || len(g.Transform) != len(w.Transform) {
			t.Errorf("#%d %s/%d/%v, want %s/%d/%v", i, g.Name, g.Parent, g.Length, w.Name, w.Parent, w.Length)
			continue
		}
		for k := range g.Transform {
			if d := math.Abs(g.Transform[k] - w.Transform[k]); d > maxd {
				maxd = d
			}
			if d := math.Abs(g.Transform[k] - w.Transform[k]); d > 1e-6 {
				t.Errorf("%s element %d = %v, want %v", g.Name, k, g.Transform[k], w.Transform[k])
			}
		}
	}
	t.Logf("max |difference| %g over %d bones", maxd, len(got))
}
