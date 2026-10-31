// Package basisu transcodes KTX2 textures (glTF KHR_texture_basisu: ETC1S
// with BasisLZ and UASTC, zstd supercompressed or not) to RGBA8 with
// Binomial's own basis_universal v2.50 transcoder, compiled to
// wasm32-wasip1 and run by wazero: pure Go, no cgo. The embedded module is
// built by tools/basisu-wasm.
package basisu

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

//go:embed ktx2_transcoder.wasm
var wasmBin []byte

// ktx2Magic is the identifier a KTX2 file starts with.
var ktx2Magic = []byte("\xabKTX 20\xbb\r\n\x1a\n")

// IsKTX2 reports whether b starts with the KTX2 file identifier.
func IsKTX2(b []byte) bool { return bytes.HasPrefix(b, ktx2Magic) }

// Image is a transcoded texture: W x H RGBA8 pixels, rows top to bottom.
type Image struct {
	W, H int
	Pix  []uint8
}

// Decoder holds the compiled module. Decode instantiates a fresh module per
// call and closes it after, so a trap or a large transcode leaves nothing
// behind; calls are serialised.
type Decoder struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
}

// New compiles the embedded transcoder. A non-empty cacheDir keeps
// wazero's compiled code there, so later processes skip the compile.
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
		return nil, fmt.Errorf("basisu: wasi: %w", err)
	}
	cm, err := rt.CompileModule(ctx, wasmBin)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("basisu: compile: %w", err)
	}
	return &Decoder{rt: rt, compiled: cm}, nil
}

// Close releases the runtime.
func (d *Decoder) Close(ctx context.Context) error { return d.rt.Close(ctx) }

// Decode transcodes level 0 of a KTX2 file to RGBA8. A done ctx stops a
// running transcode.
func (d *Decoder) Decode(ctx context.Context, buf []byte) (*Image, error) {
	if !IsKTX2(buf) || len(buf) > math.MaxInt32 {
		return nil, errors.New("basisu: not a KTX2 file")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mod, err := d.rt.InstantiateModule(ctx, d.compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return nil, fmt.Errorf("basisu: instantiate: %w", err)
	}
	defer mod.Close(ctx)
	mem := mod.Memory()
	call := func(name string, args ...uint64) (uint32, error) {
		f := mod.ExportedFunction(name)
		if f == nil {
			return 0, fmt.Errorf("basisu: export %s missing", name)
		}
		r, err := f.Call(ctx, args...)
		if err != nil {
			return 0, fmt.Errorf("basisu: %s: %w", name, err)
		}
		return uint32(r[0]), nil
	}
	in, err := call("kt_alloc", uint64(len(buf)))
	if err != nil {
		return nil, err
	}
	if in == 0 {
		return nil, fmt.Errorf("basisu: cannot allocate %d bytes", len(buf))
	}
	if !mem.Write(in, buf) {
		return nil, errors.New("basisu: input out of range")
	}
	rp, err := call("kt_decode", uint64(in), uint64(len(buf)))
	if err != nil {
		return nil, err
	}
	hdr, ok := mem.Read(rp, 20)
	if !ok {
		return nil, errors.New("basisu: result out of range")
	}
	u := func(i int) uint32 { return binary.LittleEndian.Uint32(hdr[i*4:]) }
	if st := u(0); st != 0 {
		return nil, fmt.Errorf("basisu: transcode failed (status %d): %s", st, cString(mem, u(4)))
	}
	w, h := u(1), u(2)
	n := uint64(w) * uint64(h) * 4
	if n == 0 || n > math.MaxUint32 {
		return nil, errors.New("basisu: result too large")
	}
	px, ok := mem.Read(u(3), uint32(n))
	if !ok {
		return nil, errors.New("basisu: result out of range")
	}
	return &Image{W: int(w), H: int(h), Pix: bytes.Clone(px)}, nil
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
