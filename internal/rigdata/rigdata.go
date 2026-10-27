// Package rigdata reads Hyper3D's rig result (the DRCRIG01 file of
// task/rodin_rigging_result) and computes the web's reskin of an imported
// FBX skeleton: after the rig from the FBX's bone names (the first
// rodin_rigging call), the web's rigging panel sends a second call that
// keeps the generated bone positions and turns each bone to the FBX bone's
// frame (main.fmt.js:55804-56120; skeleton editor cf/7859: 3748-3895 k,
// 13054-13110 import positions, 17267-17760 joints, bind transforms and
// bone displays). Both are ported here so rig_model sends the web's body.
package rigdata

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/fbx"
)

// Header is the part of a rig file's header the reskin reads.
type Header struct {
	Bones []struct {
		Name   string   `json:"name"`
		Parent int      `json:"parent"`
		Length *float64 `json:"length"`
	} `json:"bones"`
	BoneInverses [][]float64 `json:"boneInverses"`
}

// maxRigFile bounds a rig file after decompression.
const maxRigFile = 256 << 20

// DecodeHeader reads the header of a rig file (gzipped or not).
func DecodeHeader(data []byte) (*Header, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("the rig file does not unzip: %w", err)
		}
		if data, err = io.ReadAll(io.LimitReader(zr, maxRigFile)); err != nil {
			return nil, fmt.Errorf("the rig file does not unzip: %w", err)
		}
	}
	if len(data) < 16 || string(data[:8]) != "DRCRIG01" {
		return nil, errors.New("the rig file is not a DRCRIG01 file")
	}
	n := binary.LittleEndian.Uint32(data[8:])
	if uint64(16)+uint64(n) > uint64(len(data)) {
		return nil, errors.New("the rig file's header is cut short")
	}
	var h Header
	if err := json.Unmarshal(data[16:16+n], &h); err != nil {
		return nil, fmt.Errorf("the rig file's header: %w", err)
	}
	return &h, nil
}

// Joint is a bone of the generated skeleton as the web's skeleton editor
// lists it (_getRodinSkeletonEditBindTransforms): display label, parent
// joint index, length, bind transform in the model's space.
type Joint struct {
	Label     string
	Parent    int
	Length    *float64
	Transform fbx.Mat4
}

var mixamo = regexp.MustCompile(`(?i)^mixamorig:?`)

func afterColon(s string) string {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// normalizeName is the editor's _normalizeSkeletonEditBoneName.
func normalizeName(s string) string {
	s = mixamo.ReplaceAllString(afterColon(s), "")
	s = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || isSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.ToLower(s)
}

func isSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trim(s string) string { return strings.TrimFunc(s, isSpace) }

// Joints lists the generated skeleton's joints: header bones whose name is
// not empty or "pose" (after normalizing), labelled without namespace and
// mixamorig prefix, parented to the nearest listed ancestor, with the bind
// transform inverse(boneInverses[i]).
func Joints(h *Header) ([]Joint, error) {
	ignored := func(name string) bool { n := normalizeName(name); return n == "" || n == "pose" }
	index := map[int]int{} // header bone -> joint index
	var out []Joint
	var bones []int // header index of each joint
	for i, b := range h.Bones {
		if ignored(b.Name) {
			continue
		}
		label := trim(mixamo.ReplaceAllString(afterColon(b.Name), ""))
		if label == "" {
			label = fmt.Sprintf("Bone%d", i)
		}
		if b.Length != nil && (math.IsNaN(*b.Length) || math.IsInf(*b.Length, 0) || *b.Length <= 0) {
			return nil, errors.New("bone length must be a positive finite number")
		}
		if i >= len(h.BoneInverses) || len(h.BoneInverses[i]) != 16 {
			return nil, fmt.Errorf("the rig file has no bind matrix for bone %q", b.Name)
		}
		var inv fbx.Mat4
		copy(inv[:], h.BoneInverses[i])
		index[i] = len(out)
		bones = append(bones, i)
		out = append(out, Joint{Label: label, Parent: -1, Length: b.Length, Transform: inv.Invert()})
	}
	// The visible parent: the nearest ancestor that is listed, wherever it
	// stands in the list.
	for j, i := range bones {
		p := h.Bones[i].Parent
		for seen := 0; p >= 0 && p < len(h.Bones) && ignored(h.Bones[p].Name) && seen <= len(h.Bones); seen++ {
			p = h.Bones[p].Parent
		}
		if k, ok := index[p]; ok && p >= 0 {
			out[j].Parent = k
		}
	}
	return out, nil
}

var mixamoNoColon = regexp.MustCompile(`(?i)^mixamorig`)
