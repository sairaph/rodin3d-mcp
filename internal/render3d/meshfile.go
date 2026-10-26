package render3d

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// meshMagic starts a mesh file: then flags (1 normals, 2 texture
// coordinates, 4 decoded from a draft), the vertex count and the index
// count, then positions (3 float32 per vertex), normals (3), texture
// coordinates (2) and indices (uint32); all little-endian.
const meshMagic = "R3DMESH1"

const (
	meshNormals = 1 << iota
	meshUV
	meshDraft
)

// EncodeMesh writes m in the mesh file format, so a stage mesh is decoded
// once and read back in milliseconds.
func EncodeMesh(m *Mesh, draft bool) []byte {
	var flags uint32
	if m.Nrm != nil {
		flags |= meshNormals
	}
	if m.UV != nil {
		flags |= meshUV
	}
	if draft {
		flags |= meshDraft
	}
	n := len(m.Pos)
	size := len(meshMagic) + 12 + n*12 + len(m.Idx)*4
	if m.Nrm != nil {
		size += n * 12
	}
	if m.UV != nil {
		size += n * 8
	}
	out := make([]byte, 0, size)
	out = append(out, meshMagic...)
	out = binary.LittleEndian.AppendUint32(out, flags)
	out = binary.LittleEndian.AppendUint32(out, uint32(n))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(m.Idx)))
	f := func(v float32) { out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v)) }
	for _, p := range m.Pos {
		f(p[0])
		f(p[1])
		f(p[2])
	}
	for _, p := range m.Nrm {
		f(p[0])
		f(p[1])
		f(p[2])
	}
	for _, p := range m.UV {
		f(p[0])
		f(p[1])
	}
	for _, i := range m.Idx {
		out = binary.LittleEndian.AppendUint32(out, i)
	}
	return out
}

// DecodeMesh reads a mesh file and reports whether it came from a draft.
func DecodeMesh(b []byte) (*Mesh, bool, error) {
	if !bytes.HasPrefix(b, []byte(meshMagic)) || len(b) < len(meshMagic)+12 {
		return nil, false, errors.New("render3d: not a mesh file")
	}
	p := b[len(meshMagic):]
	flags := binary.LittleEndian.Uint32(p)
	n := int(binary.LittleEndian.Uint32(p[4:]))
	ni := int(binary.LittleEndian.Uint32(p[8:]))
	p = p[12:]
	want := uint64(n)*12 + uint64(ni)*4
	if flags&meshNormals != 0 {
		want += uint64(n) * 12
	}
	if flags&meshUV != 0 {
		want += uint64(n) * 8
	}
	if uint64(len(p)) != want {
		return nil, false, fmt.Errorf("render3d: mesh file holds %d bytes of data, its header says %d", len(p), want)
	}
	f := func() float32 {
		v := math.Float32frombits(binary.LittleEndian.Uint32(p))
		p = p[4:]
		return v
	}
	m := &Mesh{Pos: make([][3]float32, n), Idx: make([]uint32, ni)}
	for i := range m.Pos {
		m.Pos[i] = [3]float32{f(), f(), f()}
	}
	if flags&meshNormals != 0 {
		m.Nrm = make([][3]float32, n)
		for i := range m.Nrm {
			m.Nrm[i] = [3]float32{f(), f(), f()}
		}
	}
	if flags&meshUV != 0 {
		m.UV = make([][2]float32, n)
		for i := range m.UV {
			m.UV[i] = [2]float32{f(), f()}
		}
	}
	for i := range m.Idx {
		m.Idx[i] = binary.LittleEndian.Uint32(p[i*4:])
	}
	if err := m.Validate(); err != nil {
		return nil, false, err
	}
	return m, flags&meshDraft != 0, nil
}

// Validate checks that m is a renderable triangle list: whole triangles,
// indices in range, attribute arrays as long as the positions.
func (m *Mesh) Validate() error {
	if len(m.Pos) == 0 || len(m.Idx) < 3 || len(m.Idx)%3 != 0 {
		return errors.New("render3d: the mesh has no triangles")
	}
	if m.Nrm != nil && len(m.Nrm) != len(m.Pos) {
		return errors.New("render3d: the mesh's normals do not match its positions")
	}
	if m.UV != nil && len(m.UV) != len(m.Pos) {
		return errors.New("render3d: the mesh's texture coordinates do not match its positions")
	}
	n := uint32(len(m.Pos))
	for _, i := range m.Idx {
		if i >= n {
			return errors.New("render3d: a mesh index is out of range")
		}
	}
	return nil
}
