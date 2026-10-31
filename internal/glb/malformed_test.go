package glb

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// glbWith is a GLB of json and a BIN chunk bin.
func glbWith(json string, bin []byte) []byte {
	for len(json)%4 != 0 {
		json += " "
	}
	for len(bin)%4 != 0 {
		bin = append(bin, 0)
	}
	b := make([]byte, 12, 12+8+len(json)+8+len(bin))
	copy(b, "glTF")
	binary.LittleEndian.PutUint32(b[4:], 2)
	binary.LittleEndian.PutUint32(b[8:], uint32(cap(b)))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(json)))
	b = append(b, "JSON"...)
	b = append(b, json...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(bin)))
	b = append(b, "BIN\x00"...)
	return append(b, bin...)
}

// triangle is the BIN chunk of one triangle: three float VEC3 positions
// (bytes 0-35), then three uint16 indices (36-41).
func triangle() []byte {
	var b []byte
	for _, v := range []float32{0, 0, 0, 1, 0, 0, 0, 1, 0} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	for _, i := range []uint16{0, 1, 2} {
		b = binary.LittleEndian.AppendUint16(b, i)
	}
	return b
}

// triangleDoc is the JSON of triangle() with the given position accessor
// count, index count and position bufferView stride, and extra top-level
// members.
func triangleDoc(posCount, idxCount, stride int, extra string) string {
	return fmt.Sprintf(`{"asset":{"version":"2.0"},%s
		"nodes":[{"mesh":0}],"meshes":[{"primitives":[{"attributes":{"POSITION":0},"indices":1}]}],
		"buffers":[{"byteLength":44}],
		"bufferViews":[{"buffer":0,"byteOffset":0,"byteLength":36,"byteStride":%d},{"buffer":0,"byteOffset":36,"byteLength":6}],
		"accessors":[{"bufferView":0,"componentType":5126,"count":%d,"type":"VEC3"},{"bufferView":1,"componentType":5123,"count":%d,"type":"SCALAR"}]}`,
		extra, stride, posCount, idxCount)
}

// TestMalformedNoPanic: malformed files are errors, never a panic.
func TestMalformedNoPanic(t *testing.T) {
	ok := triangleDoc(3, 3, 0, `"scenes":[{"nodes":[0]}],`)
	for name, tc := range map[string]struct {
		json    string
		wantErr bool
	}{
		"valid":                {ok, false},
		"scene -1":             {triangleDoc(3, 3, 0, `"scene":-1,"scenes":[{"nodes":[0]}],`), false},
		"negative stride":      {triangleDoc(3, 3, -12, `"scenes":[{"nodes":[0]}],`), true},
		"huge position count":  {triangleDoc(math.MaxInt64/2, 3, 0, `"scenes":[{"nodes":[0]}],`), true},
		"huge count, stride":   {triangleDoc(1<<40, 3, 12, `"scenes":[{"nodes":[0]}],`), true},
		"huge index count":     {triangleDoc(3, math.MaxInt64/2, 0, `"scenes":[{"nodes":[0]}],`), true},
		"index accessor -1":    {strings.Replace(ok, `"indices":1`, `"indices":-1`, 1), true},
		"position accessor -1": {strings.Replace(ok, `"POSITION":0`, `"POSITION":-1`, 1), true},
	} {
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("%s: panic %v", name, v)
				}
			}()
			f, err := Parse(glbWith(tc.json, triangle()))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			_ = f.MeshNodeNames()
			m, err := f.LoadMesh(context.Background(), nil)
			switch {
			case tc.wantErr && err == nil:
				t.Errorf("%s: no error", name)
			case !tc.wantErr && (err != nil || len(m.Idx) != 3):
				t.Errorf("%s: %v", name, err)
			}
		}()
	}
}

// TestSharedNodesRefused: nodes listing the same child twice, level after
// level, would be walked 2^25 times; the reader refuses the second visit at
// once, and MeshNodeNames skips it.
func TestSharedNodesRefused(t *testing.T) {
	var nodes []string
	for i := 0; i < 25; i++ {
		nodes = append(nodes, fmt.Sprintf(`{"children":[%d,%d]}`, i+1, i+1))
	}
	nodes = append(nodes, `{"mesh":0}`)
	doc := strings.Replace(triangleDoc(3, 3, 0, `"scenes":[{"nodes":[0]}],`), `"nodes":[{"mesh":0}]`, `"nodes":[`+strings.Join(nodes, ",")+`]`, 1)
	f, err := Parse(glbWith(doc, triangle()))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := f.LoadMesh(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("LoadMesh: %v", err)
	}
	if names := f.MeshNodeNames(); len(names) != 1 {
		t.Errorf("MeshNodeNames: %d names", len(names))
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	// A done context stops the walk.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g, _ := Parse(glbWith(triangleDoc(3, 3, 0, `"scenes":[{"nodes":[0]}],`), triangle()))
	if _, err := g.LoadMesh(ctx, nil); err == nil {
		t.Error("cancelled: no error")
	}
}
