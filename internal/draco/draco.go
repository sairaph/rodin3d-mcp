// Package draco decodes Draco-compressed geometry (the .drc geometry drafts
// and the KHR_draco_mesh_compression primitives of Rodin GLBs) with Google's
// own Draco 1.5.7 decoder, compiled to wasm32-wasip1 and run by wazero: pure
// Go, no cgo. The embedded module is built by tools/draco-wasm.
package draco

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed draco_decoder.wasm
var wasmBin []byte

// Attribute types, as draco::GeometryAttribute::Type.
const (
	Position = 0
	Normal   = 1
	Color    = 2
	TexCoord = 3
	Generic  = 4
)

// Attribute is one decoded attribute: Components floats per point, with the
// attribute's normalisation applied.
type Attribute struct {
	Type       int
	UniqueID   uint32
	Components int
	Data       []float32
}

// Geometry is a decoded point cloud or triangle mesh. Indices hold three
// point indices per face; it is nil for a point cloud.
type Geometry struct {
	Mesh    bool
	Points  int
	Faces   int
	Indices []uint32
	Attrs   []Attribute
}

// ByType returns the first attribute of type t, or nil.
func (g *Geometry) ByType(t int) *Attribute {
	for i := range g.Attrs {
		if g.Attrs[i].Type == t {
			return &g.Attrs[i]
		}
	}
	return nil
}

// ByUniqueID returns the attribute with unique id id (glTF
// KHR_draco_mesh_compression maps attribute names to these), or nil.
func (g *Geometry) ByUniqueID(id uint32) *Attribute {
	for i := range g.Attrs {
		if g.Attrs[i].UniqueID == id {
			return &g.Attrs[i]
		}
	}
	return nil
}

// IsDraco reports whether b starts with the Draco bitstream magic.
func IsDraco(b []byte) bool { return bytes.HasPrefix(b, []byte("DRACO")) }

// Decoder holds the compiled module. Decode instantiates a fresh module per
// call and closes it after, so a trap or a large decode leaves nothing
// behind; calls are serialised.
type Decoder struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
}

// New compiles the embedded decoder (about 0.25 s). A non-empty cacheDir
// keeps wazero's compiled code there, so later processes skip the compile.
func New(ctx context.Context, cacheDir string) (*Decoder, error) {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if cacheDir != "" {
		if cache, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(cache)
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("draco: wasi: %w", err)
	}
	cm, err := rt.CompileModule(ctx, wasmBin)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("draco: compile: %w", err)
	}
	return &Decoder{rt: rt, compiled: cm}, nil
}

// Close releases the runtime.
func (d *Decoder) Close(ctx context.Context) error { return d.rt.Close(ctx) }

// Decode decodes a Draco buffer (a .drc file or a glTF Draco bufferView). A
// done ctx stops a running decode.
func (d *Decoder) Decode(ctx context.Context, buf []byte) (*Geometry, error) {
	if len(buf) == 0 || len(buf) > math.MaxInt32 {
		return nil, errors.New("draco: empty or oversized buffer")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mod, err := d.rt.InstantiateModule(ctx, d.compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return nil, fmt.Errorf("draco: instantiate: %w", err)
	}
	defer mod.Close(ctx)
	mem := mod.Memory()
	call := func(name string, args ...uint64) (uint32, error) {
		f := mod.ExportedFunction(name)
		if f == nil {
			return 0, fmt.Errorf("draco: export %s missing", name)
		}
		r, err := f.Call(ctx, args...)
		if err != nil {
			return 0, fmt.Errorf("draco: %s: %w", name, err)
		}
		return uint32(r[0]), nil
	}
	in, err := call("dw_alloc", uint64(len(buf)))
	if err != nil {
		return nil, err
	}
	if in == 0 {
		return nil, fmt.Errorf("draco: cannot allocate %d bytes", len(buf))
	}
	if !mem.Write(in, buf) {
		return nil, errors.New("draco: input out of range")
	}
	rp, err := call("dw_decode", uint64(in), uint64(len(buf)))
	if err != nil {
		return nil, err
	}
	hdr, ok := mem.Read(rp, 32)
	if !ok {
		return nil, errors.New("draco: result out of range")
	}
	u := func(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[i*4:]) }
	if st := u(hdr, 0); st != 0 {
		return nil, fmt.Errorf("draco: decode failed (status %d): %s", st, cString(mem, u(hdr, 7)))
	}
	g := &Geometry{Mesh: u(hdr, 1) == 1, Points: int(u(hdr, 2)), Faces: int(u(hdr, 3))}
	read := func(ptr uint32, count, width int) ([]byte, error) {
		n := uint64(count) * uint64(width)
		if n > math.MaxUint32 {
			return nil, errors.New("draco: result too large")
		}
		b, ok := mem.Read(ptr, uint32(n))
		if !ok {
			return nil, errors.New("draco: result out of range")
		}
		return b, nil
	}
	if g.Mesh {
		raw, err := read(u(hdr, 4), g.Faces*3, 4)
		if err != nil {
			return nil, err
		}
		g.Indices = make([]uint32, g.Faces*3)
		for i := range g.Indices {
			g.Indices[i] = binary.LittleEndian.Uint32(raw[i*4:])
		}
	}
	n := int(u(hdr, 5))
	recs, err := read(u(hdr, 6), n, 16)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		r := recs[i*16:]
		a := Attribute{Type: int(u(r, 0)), UniqueID: u(r, 1), Components: int(u(r, 2))}
		raw, err := read(u(r, 3), g.Points*a.Components, 4)
		if err != nil {
			return nil, err
		}
		a.Data = make([]float32, g.Points*a.Components)
		for j := range a.Data {
			a.Data[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[j*4:]))
		}
		g.Attrs = append(g.Attrs, a)
	}
	return g, nil
}

func cString(mem api.Memory, p uint32) string {
	if p == 0 {
		return ""
	}
	var out []byte
	for i := uint32(0); i < 512; i++ {
		b, ok := mem.ReadByte(p + i)
		if !ok || b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}
