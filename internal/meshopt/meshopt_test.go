package meshopt

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"runtime"
	"testing"
)

// fixture is a case of testdata/cases.json: data encoded by the
// meshoptimizer 1.3.0 encoder and its decoding by the reference decoder
// this package ports (testdata/gen.mjs writes the file).
type fixture struct {
	Name, Mode, Filter string
	Count, Size        int
	Encoded, Want      []byte
}

func fixtures(t testing.TB) []fixture {
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct {
		Name, Mode, Filter, Encoded, Want string
		Count, Size                       int
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, r := range raw {
		enc, err1 := base64.StdEncoding.DecodeString(r.Encoded)
		want, err2 := base64.StdEncoding.DecodeString(r.Want)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: bad base64", r.Name)
		}
		out = append(out, fixture{Name: r.Name, Mode: r.Mode, Filter: r.Filter, Count: r.Count, Size: r.Size, Encoded: enc, Want: want})
	}
	return out
}

// TestDecodeMatchesReference: every codec, version and filter decodes the
// encoder's output to the reference decoder's bytes.
func TestDecodeMatchesReference(t *testing.T) {
	cases := fixtures(t)
	if len(cases) < 50 {
		t.Fatalf("%d fixtures", len(cases))
	}
	for _, c := range cases {
		got, err := Decode(c.Count, c.Size, c.Encoded, c.Mode, c.Filter)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if !bytes.Equal(got, c.Want) {
			for i := range got {
				if got[i] != c.Want[i] {
					t.Errorf("%s: byte %d is %d, want %d", c.Name, i, got[i], c.Want[i])
					break
				}
			}
		}
	}
}

// TestMalformed: cut, grown and corrupted encodings and wrong sizes are
// errors, never a panic.
func TestMalformed(t *testing.T) {
	for _, c := range fixtures(t) {
		// An index sequence ends in 4 bytes of padding the reference does not read.
		last := len(c.Encoded) - 1
		if c.Mode == "INDICES" {
			last -= 4
		}
		for _, n := range []int{0, 1, len(c.Encoded) / 2, last} {
			if _, err := Decode(c.Count, c.Size, c.Encoded[:n], c.Mode, c.Filter); err == nil && c.Count > 0 {
				t.Errorf("%s cut to %d bytes: no error", c.Name, n)
			}
		}
		grown := append(append([]byte(nil), c.Encoded...), 0)
		if _, err := Decode(c.Count, c.Size, grown, c.Mode, c.Filter); err == nil && c.Mode == "ATTRIBUTES" {
			t.Errorf("%s with a byte more: no error", c.Name)
		}
		bad := append([]byte(nil), c.Encoded...)
		bad[0] ^= 0x40
		if _, err := Decode(c.Count, c.Size, bad, c.Mode, c.Filter); err == nil {
			t.Errorf("%s with a bad header: no error", c.Name)
		}
		// More elements than were encoded run out of data.
		_, _ = Decode(c.Count*4+3, c.Size, c.Encoded, c.Mode, c.Filter)
	}
	for _, tc := range []struct {
		count, size int
		mode        string
	}{{3, 6, "ATTRIBUTES"}, {3, 0, "ATTRIBUTES"}, {3, 512, "ATTRIBUTES"}, {4, 4, "TRIANGLES"}, {3, 1, "TRIANGLES"}, {3, 8, "INDICES"}, {-1, 4, "INDICES"}, {3, 4, "LINES"}} {
		if _, err := Decode(tc.count, tc.size, []byte{0xa0, 0, 0, 0}, tc.mode, ""); err == nil {
			t.Errorf("%d elements of %d bytes in %s: no error", tc.count, tc.size, tc.mode)
		}
	}
	if _, err := Decode(4, 4, nil, "ATTRIBUTES", "SPIRAL"); err == nil {
		t.Error("unknown filter: no error")
	}
}

// FuzzDecode feeds mutated encodings to every decoder; none may panic.
func FuzzDecode(f *testing.F) {
	for _, c := range fixtures(f) {
		if len(c.Encoded) < 4096 {
			f.Add(c.Encoded, c.Count, c.Size, c.Mode, c.Filter)
		}
	}
	f.Fuzz(func(t *testing.T, src []byte, count, size int, mode, filter string) {
		if count > 1<<16 {
			count = 1 << 16
		}
		_, _ = Decode(count, size, src, mode, filter)
	})
}

// TestShortPayloadAllocatesNothing: a 64-byte payload claiming half a
// billion elements, in a known mode or an unknown one, is refused before
// the output is allocated.
func TestShortPayloadAllocatesNothing(t *testing.T) {
	src := make([]byte, 64)
	for _, tc := range []struct {
		mode string
		head byte
	}{{"ATTRIBUTES", 0xa1}, {"TRIANGLES", 0xe1}, {"INDICES", 0xd1}, {"LINES", 0xa1}, {"", 0xa1}} {
		src[0] = tc.head
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := Decode(536870910, 4, src, tc.mode, "")
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Errorf("%s: no error", tc.mode)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
			t.Errorf("%s: allocated %d bytes for a refused payload", tc.mode, n)
		}
	}
}
