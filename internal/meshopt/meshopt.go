// Package meshopt decodes the buffer views of glTF EXT_meshopt_compression:
// a Go port of meshopt_decoder_reference.js of meshoptimizer 1.3 (MIT
// License, Arseny Kapoulkine; the reference decoder by Jasper St. Pierre;
// see NOTICE). It reads the vertex codec (versions 0 and 1), the triangle
// and index sequence codecs, and the octahedral, quaternion, exponential
// and colour filters. Malformed input is an error: every read is bounds
// checked, so no input makes it panic.
package meshopt

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// ErrMalformed is returned for data that is not a valid encoding.
var ErrMalformed = errors.New("meshopt: malformed data")

// Decode decodes a buffer view's compressed data: count elements of size
// bytes each, in mode ATTRIBUTES, TRIANGLES or INDICES, with filter NONE
// (or empty), OCTAHEDRAL, QUATERNION, EXPONENTIAL or COLOR applied after
// an ATTRIBUTES decode.
func Decode(count, size int, src []byte, mode, filter string) ([]byte, error) {
	if count < 0 || size <= 0 || size > 256 || count > math.MaxInt32/size {
		return nil, fmt.Errorf("meshopt: %d elements of %d bytes", count, size)
	}
	// The output is allocated only when src is long enough to hold count
	// elements, so a short payload claiming a huge count costs nothing.
	need := 0
	switch mode {
	case "ATTRIBUTES":
		if size%4 != 0 {
			return nil, fmt.Errorf("meshopt: vertex stride %d", size)
		}
		// The header byte, a v1 control byte per 4 bytes of each block (the
		// least a block can take) and the shortest tail.
		maxBlock := min((0x2000/size)&^0x0f, 0x100)
		need = 1 + (count+maxBlock-1)/maxBlock*(size/4) + 24
	case "TRIANGLES":
		need = 1 + count/3 + 16 // a code byte per triangle, the code table
	case "INDICES":
		need = 1 + count + 4 // a byte per index at least, the padding
	}
	if len(src) < need {
		return nil, ErrMalformed
	}
	dst := make([]byte, count*size)
	var err error
	switch mode {
	case "ATTRIBUTES":
		if err = DecodeVertexBuffer(dst, count, size, src); err == nil {
			err = Filter(dst, count, size, filter)
		}
	case "TRIANGLES":
		err = DecodeIndexBuffer(dst, count, size, src)
	case "INDICES":
		err = DecodeIndexSequence(dst, count, size, src)
	default:
		return nil, fmt.Errorf("meshopt: unknown mode %q", mode)
	}
	if err != nil {
		return nil, err
	}
	return dst, nil
}

// reader reads src with bounds checks: a read past end yields 0 and marks
// the reader bad, so a decode loop stays simple and the caller checks once.
type reader struct {
	src []byte
	end int // reads at or past end are out of bounds
	bad bool
}

func (r *reader) at(i int) byte {
	if i < 0 || i >= r.end {
		r.bad = true
		return 0
	}
	return r.src[i]
}

func dezig(v uint32) uint32 { return v>>1 ^ -(v & 1) }

// DecodeVertexBuffer decodes count elements of stride bytes (a multiple of
// 4, at most 256) into dst.
func DecodeVertexBuffer(dst []byte, count, stride int, src []byte) error {
	if stride <= 0 || stride > 256 || stride%4 != 0 || count < 0 || len(dst) < count*stride {
		return fmt.Errorf("meshopt: vertex stride %d", stride)
	}
	if len(src) < 1 || src[0]&0xf0 != 0xa0 || src[0]&0x0f > 1 {
		return ErrMalformed
	}
	version := int(src[0] & 0x0f)
	maxBlock := min((0x2000/stride)&^0x0f, 0x100)
	deltas := make([]byte, maxBlock*stride)
	tailSize := stride
	if version == 1 {
		tailSize = stride + stride/4
	}
	padded := max(tailSize, 32)
	if version == 1 {
		padded = max(tailSize, 24)
	}
	if len(src) < 1+padded {
		return ErrMalformed
	}
	tail := len(src) - tailSize
	// What the deltas are relative to.
	temp := append([]byte(nil), src[tail:tail+stride]...)
	var channels []byte
	if version == 1 {
		channels = src[tail+stride : tail+tailSize]
	}
	r := &reader{src: src, end: len(src) - padded}
	off := 1 // past the header byte
	headerModes := [3][4]int{
		{0, 2, 4, 8}, // v0
		{0, 1, 2, 4}, // v1, control 0
		{1, 2, 4, 8}, // v1, control 1
	}
	for base := 0; base < count; base += maxBlock {
		n := min(count-base, maxBlock)
		groups := (n + 0x0f) &^ 0x0f >> 4
		headerBytes := (groups + 0x03) &^ 0x03 >> 2
		controlOff := off
		if version == 1 {
			off += stride / 4
		}
		clear(deltas)
		for b := 0; b < stride; b++ {
			dbase := b * n
			control := 0
			if version == 1 {
				control = int(r.at(controlOff+b>>2)>>(uint(b&0x03)<<1)) & 0x03
			}
			switch control {
			case 2: // all byte deltas 0; nothing stored
				continue
			case 3: // stored as they are, no header bits
				for k := 0; k < n; k++ {
					deltas[dbase+k] = r.at(off + k)
				}
				off += n
				continue
			}
			headerOff := off
			off += headerBytes
			for g := 0; g < groups; g++ {
				mode := int(r.at(headerOff+g>>2)>>(uint(g&0x03)<<1)) & 0x03
				row := 0
				if version == 1 {
					row = control + 1
				}
				d := dbase + g<<4
				switch headerModes[row][mode] {
				case 0: // all 16 deltas 0
				case 1: // 1-bit sentinels, low bits first
					sb := off
					off += 2
					for m := 0; m < 16; m++ {
						v := r.at(sb+m>>3) >> uint(m&0x07) & 0x01
						if v == 1 {
							v = r.at(off)
							off++
						}
						deltas[d+m] = v
					}
				case 2: // 2-bit sentinels
					sb := off
					off += 4
					for m := 0; m < 16; m++ {
						v := r.at(sb+m>>2) >> uint(6-(m&0x03)<<1) & 0x03
						if v == 3 {
							v = r.at(off)
							off++
						}
						deltas[d+m] = v
					}
				case 4: // 4-bit sentinels
					sb := off
					off += 8
					for m := 0; m < 16; m++ {
						v := r.at(sb+m>>1) >> uint(4-(m&0x01)<<2) & 0x0f
						if v == 0x0f {
							v = r.at(off)
							off++
						}
						deltas[d+m] = v
					}
				default: // 16 deltas stored as they are
					for m := 0; m < 16; m++ {
						deltas[d+m] = r.at(off + m)
					}
					off += 16
				}
			}
			if r.bad {
				return ErrMalformed
			}
		}
		if r.bad {
			return ErrMalformed
		}
		// Apply the deltas.
		for e := 0; e < n; e++ {
			o := (base + e) * stride
			for bg := 0; bg < stride; bg += 4 {
				mode := byte(0)
				if version == 1 {
					mode = channels[bg>>2] & 0x03
				}
				switch mode {
				case 0: // byte deltas, zigzag
					for b := bg; b < bg+4; b++ {
						t := temp[b] + byte(dezig(uint32(deltas[b*n+e])))
						dst[o+b], temp[b] = t, t
					}
				case 1: // 16-bit deltas, zigzag
					for b := bg; b < bg+4; b += 2 {
						delta := dezig(uint32(deltas[b*n+e]) | uint32(deltas[(b+1)*n+e])<<8)
						t := uint16(uint32(temp[b]) | uint32(temp[b+1])<<8 + delta)
						dst[o+b], temp[b] = byte(t), byte(t)
						dst[o+b+1], temp[b+1] = byte(t>>8), byte(t>>8)
					}
				case 2: // 32-bit XOR deltas, rotated
					delta := uint32(deltas[bg*n+e]) | uint32(deltas[(bg+1)*n+e])<<8 | uint32(deltas[(bg+2)*n+e])<<16 | uint32(deltas[(bg+3)*n+e])<<24
					t := uint32(temp[bg]) | uint32(temp[bg+1])<<8 | uint32(temp[bg+2])<<16 | uint32(temp[bg+3])<<24
					t ^= bits.RotateLeft32(delta, -int(channels[bg>>2]>>4))
					for k := 0; k < 4; k++ {
						dst[o+bg+k], temp[bg+k] = byte(t>>(8*k)), byte(t>>(8*k))
					}
				default:
					return ErrMalformed
				}
			}
		}
	}
	if off != len(src)-padded {
		return ErrMalformed
	}
	return nil
}

// round is JavaScript's Math.round (halves towards +Inf); NaN and the
// infinities give 0, as storing them in a typed array does.
func round(x float64) int64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return int64(math.Floor(x + 0.5))
}

// Filter applies a vertex filter in place to count elements of stride
// bytes; NONE or empty does nothing.
func Filter(dst []byte, count, stride int, filter string) error {
	le16 := func(i int) int16 { return int16(uint16(dst[i]) | uint16(dst[i+1])<<8) }
	put16 := func(i int, v int64) { dst[i], dst[i+1] = byte(v), byte(v>>8) }
	switch filter {
	case "", "NONE":
		return nil
	case "OCTAHEDRAL":
		if stride != 4 && stride != 8 {
			return fmt.Errorf("meshopt: OCTAHEDRAL filter on stride %d", stride)
		}
		maxInt := 127.0
		get := func(e, c int) float64 { return float64(int8(dst[e*4+c])) }
		put := func(e, c int, v int64) { dst[e*4+c] = byte(v) }
		if stride == 8 {
			maxInt = 32767
			get = func(e, c int) float64 { return float64(le16(e*8 + c*2)) }
			put = func(e, c int, v int64) { put16(e*8+c*2, v) }
		}
		for e := 0; e < count; e++ {
			one := get(e, 2)
			x, y := get(e, 0)/one, get(e, 1)/one
			z := 1 - math.Abs(x) - math.Abs(y)
			t := math.Max(-z, 0)
			if x >= 0 {
				x -= t
			} else {
				x += t
			}
			if y >= 0 {
				y -= t
			} else {
				y += t
			}
			h := maxInt / math.Sqrt(x*x+y*y+z*z)
			put(e, 0, round(x*h))
			put(e, 1, round(y*h))
			put(e, 2, round(z*h))
		}
	case "QUATERNION":
		if stride != 8 {
			return fmt.Errorf("meshopt: QUATERNION filter on stride %d", stride)
		}
		for e := 0; e < count; e++ {
			o := e * 8
			w := int32(le16(o + 6))
			maxc := int(w & 0x03)
			s := math.Sqrt2 / 2 / float64(w|0x03)
			x, y, z := float64(le16(o))*s, float64(le16(o+2))*s, float64(le16(o+4))*s
			ww := math.Sqrt(math.Max(0, 1-x*x-y*y-z*z))
			put16(o+(maxc+1)%4*2, round(x*32767))
			put16(o+(maxc+2)%4*2, round(y*32767))
			put16(o+(maxc+3)%4*2, round(z*32767))
			put16(o+maxc*2, round(ww*32767))
		}
	case "EXPONENTIAL":
		if stride%4 != 0 {
			return fmt.Errorf("meshopt: EXPONENTIAL filter on stride %d", stride)
		}
		for i := 0; i+4 <= count*stride; i += 4 {
			v := int32(uint32(dst[i]) | uint32(dst[i+1])<<8 | uint32(dst[i+2])<<16 | uint32(dst[i+3])<<24)
			exp, mant := v>>24, v<<8>>8
			scale := math.Float32frombits(uint32((exp + 127) << 23))
			f := math.Float32bits(float32(float64(scale) * float64(mant)))
			dst[i], dst[i+1], dst[i+2], dst[i+3] = byte(f), byte(f>>8), byte(f>>16), byte(f>>24)
		}
	case "COLOR":
		if stride != 4 && stride != 8 {
			return fmt.Errorf("meshopt: COLOR filter on stride %d", stride)
		}
		maxInt := float64(int(1)<<(stride*2) - 1)
		for e := 0; e < count; e++ {
			var yv, a uint32
			var co, cg int32
			if stride == 4 {
				o := e * 4
				yv, co, cg, a = uint32(dst[o]), int32(int8(dst[o+1])), int32(int8(dst[o+2])), uint32(dst[o+3])
			} else {
				o := e * 8
				yv, co, cg, a = uint32(uint16(le16(o))), int32(le16(o+2)), int32(le16(o+4)), uint32(uint16(le16(o+6)))
			}
			// The scale is in the alpha's highest set bit.
			as := int32(1)<<uint(32-bits.LeadingZeros32(a)) - 1
			r := float64(int32(yv) + co - cg)
			g := float64(int32(yv) + cg)
			b := float64(int32(yv) - co - cg)
			al := int32(a) & (as >> 1)
			al = al<<1 | al&1
			ss := maxInt / float64(as)
			out := [4]int64{round(r * ss), round(g * ss), round(b * ss), round(float64(al) * ss)}
			for c, v := range out {
				if stride == 4 {
					dst[e*4+c] = byte(v)
				} else {
					put16(e*8+c*2, v)
				}
			}
		}
	default:
		return fmt.Errorf("meshopt: unknown filter %q", filter)
	}
	return nil
}

// fifo is a ring of recent edges or vertices; its length is a power of 2.
type fifo struct {
	v   []uint32
	off int
}

func (f *fifo) read(n int) uint32 { return f.v[(f.off-1-n)&(len(f.v)-1)] }

func (f *fifo) push(x uint32) {
	f.v[f.off] = x
	f.off = (f.off + 1) & (len(f.v) - 1)
}

// leb128 reads an unsigned LEB128 number of at most 5 bytes at *off.
func (r *reader) leb128(off *int) uint32 {
	var n uint32
	for i := 0; i < 35; i += 7 {
		b := r.at(*off)
		*off++
		n |= uint32(b&0x7f) << i
		if b < 0x80 {
			return n
		}
	}
	r.bad = true
	return 0
}

// put stores index i of dst as a stride-byte little-endian number.
func put(dst []byte, stride, i int, v uint32) {
	if stride == 2 {
		dst[i*2], dst[i*2+1] = byte(v), byte(v>>8)
		return
	}
	dst[i*4], dst[i*4+1], dst[i*4+2], dst[i*4+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// DecodeIndexBuffer decodes count triangle indices (a multiple of 3) of
// stride 2 or 4 into dst.
func DecodeIndexBuffer(dst []byte, count, stride int, src []byte) error {
	if count < 0 || count%3 != 0 || (stride != 2 && stride != 4) || len(dst) < count*stride {
		return fmt.Errorf("meshopt: %d triangle indices of %d bytes", count, stride)
	}
	tris := count / 3
	if len(src) < 1+tris+16 || src[0] != 0xe1 {
		return ErrMalformed
	}
	codeAux := len(src) - 16
	r := &reader{src: src, end: codeAux} // the data runs up to the code table
	code, data := 1, 1+tris
	var next, last uint32
	edges := &fifo{v: make([]uint32, 32)}
	verts := &fifo{v: make([]uint32, 16)}
	index := func(v uint32) uint32 {
		last += dezig(v)
		return last
	}
	o := 0
	for i := 0; i < tris; i++ {
		c := src[code]
		code++
		b0, b1 := int(c>>4), int(c&0x0f)
		var a, b, cc uint32
		if b0 < 0x0f {
			a, b = edges.read(b0<<1), edges.read(b0<<1+1)
			switch {
			case b1 == 0x00:
				cc = next
				next++
				verts.push(cc)
			case b1 < 0x0d:
				cc = verts.read(b1)
			case b1 == 0x0d:
				last--
				cc = last
				verts.push(cc)
			case b1 == 0x0e:
				last++
				cc = last
				verts.push(cc)
			default:
				cc = index(r.leb128(&data))
				verts.push(cc)
			}
			// fifo pushes happen backwards
			edges.push(b)
			edges.push(cc)
			edges.push(cc)
			edges.push(a)
		} else {
			if b1 < 0x0e {
				e := src[codeAux+b1]
				z, w := int(e>>4), int(e&0x0f)
				a = next
				next++
				if z == 0 {
					b = next
					next++
				} else {
					b = verts.read(z - 1)
				}
				if w == 0 {
					cc = next
					next++
				} else {
					cc = verts.read(w - 1)
				}
				verts.push(a)
				if z == 0 {
					verts.push(b)
				}
				if w == 0 {
					verts.push(cc)
				}
			} else {
				e := r.at(data)
				data++
				if e == 0 {
					next = 0
				}
				z, w := int(e>>4), int(e&0x0f)
				if b1 == 0x0e {
					a = next
					next++
				} else {
					a = index(r.leb128(&data))
				}
				switch z {
				case 0x00:
					b = next
					next++
				case 0x0f:
					b = index(r.leb128(&data))
				default:
					b = verts.read(z - 1)
				}
				switch w {
				case 0x00:
					cc = next
					next++
				case 0x0f:
					cc = index(r.leb128(&data))
				default:
					cc = verts.read(w - 1)
				}
				verts.push(a)
				if z == 0x00 || z == 0x0f {
					verts.push(b)
				}
				if w == 0x00 || w == 0x0f {
					verts.push(cc)
				}
			}
			edges.push(a)
			edges.push(b)
			edges.push(b)
			edges.push(cc)
			edges.push(cc)
			edges.push(a)
		}
		if r.bad {
			return ErrMalformed
		}
		put(dst, stride, o, a)
		put(dst, stride, o+1, b)
		put(dst, stride, o+2, cc)
		o += 3
	}
	return nil
}

// DecodeIndexSequence decodes count indices of stride 2 or 4 (any
// primitive topology) into dst.
func DecodeIndexSequence(dst []byte, count, stride int, src []byte) error {
	if count < 0 || (stride != 2 && stride != 4) || len(dst) < count*stride {
		return fmt.Errorf("meshopt: %d indices of %d bytes", count, stride)
	}
	if len(src) < 1 || src[0] != 0xd1 {
		return ErrMalformed
	}
	r := &reader{src: src, end: len(src)}
	data := 1
	var last [2]uint32
	for i := 0; i < count; i++ {
		v := r.leb128(&data)
		if r.bad {
			return ErrMalformed
		}
		b := v & 1
		last[b] += dezig(v >> 1)
		put(dst, stride, i, last[b])
	}
	return nil
}
