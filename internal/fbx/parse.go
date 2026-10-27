package fbx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// The two FBX parsers of three.js r180 FBXLoader (the web bundle's module
// 58005, BinaryParser and TextParser), ported so the tree they build, with
// its key order, first-wins and last-wins rules, is the same.

var errTruncated = errors.New("the FBX file ends in the middle of a node")

const binaryMagic = "Kaydara FBX Binary  \x00"

// parseTree returns the FBX tree of data, binary or text, as the web's
// loader reads it.
func parseTree(data []byte) (*obj, error) {
	if len(data) >= len(binaryMagic) && string(data[:len(binaryMagic)]) == binaryMagic {
		return parseBinary(data)
	}
	text := decodeUTF8(data)
	if !isASCIIFBX(text) {
		return nil, errors.New("the file is not an FBX file (THREE.FBXLoader: Unknown format)")
	}
	v, err := textVersion(text)
	if err != nil {
		return nil, err
	}
	if v < 7000 {
		return nil, fmt.Errorf("FBX version %d is not supported: the web reads text FBX from version 7000 on", v)
	}
	return parseText(text), nil
}

// decodeUTF8 is TextDecoder("utf-8").decode: every invalid byte becomes
// U+FFFD.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		sb.WriteRune(r) // RuneError for an invalid byte
		b = b[n:]
	}
	return sb.String()
}

// isASCIIFBX is the loader's isFbxFormatASCII: its odd check reads the
// characters at offsets 0, 1, 3, 6, 10, ... of the text and refuses the
// file only when one of them equals the matching character of
// "Kaydara\FBX\Binary\\".
func isASCIIFBX(text string) bool {
	want := []string{"K", "a", "y", "d", "a", "r", "a", "\\", "F", "B", "X", "\\", "B", "i", "n", "a", "r", "y", "\\", "\\"}
	t := []rune(text)
	n := 0
	read := func(off int) string {
		var c string
		if off-1 >= 0 && off-1 < len(t) {
			c = string(t[off-1])
		}
		if n+off <= len(t) {
			t = t[n+off:]
		} else {
			t = nil
		}
		n++
		return c
	}
	for _, w := range want {
		if read(1) == w {
			return false
		}
	}
	return true
}

var versionRE = regexp.MustCompile(`FBXVersion: (\d+)`)

func textVersion(text string) (int, error) {
	m := versionRE.FindStringSubmatch(text)
	if m == nil {
		return 0, errors.New("the FBX file names no FBXVersion")
	}
	return int(parseIntJS(m[1])), nil
}

// --- binary ---

type reader struct {
	b   []byte
	off int
}

func (r *reader) need(n int) error {
	if n < 0 || r.off+n > len(r.b) {
		return errTruncated
	}
	return nil
}

func (r *reader) u8() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v, nil
}

// u64 is getUint64: 2^32 * high + low as a number.
func (r *reader) u64() (float64, error) {
	lo, err := r.u32()
	if err != nil {
		return 0, err
	}
	hi, err := r.u32()
	if err != nil {
		return 0, err
	}
	return 4294967296*float64(hi) + float64(lo), nil
}

func (r *reader) bytes(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v, nil
}

// str is getString: the bytes up to the first NUL, decoded as UTF-8.
func (r *reader) str(n int) (string, error) {
	b, err := r.bytes(n)
	if err != nil {
		return "", err
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return decodeUTF8(b), nil
}

func parseBinary(data []byte) (*obj, error) {
	r := &reader{b: data, off: 23}
	version, err := r.u32()
	if err != nil {
		return nil, err
	}
	if version < 6400 {
		return nil, fmt.Errorf("FBX version %d is not supported: the web reads binary FBX from version 6400 on", version)
	}
	tree := newObj()
	for !endOfContent(r) {
		n, err := parseNode(r, version)
		if err != nil {
			return nil, err
		}
		if n != nil {
			name, _ := n.str("name")
			tree.set(name, n)
		}
	}
	return tree, nil
}

// endOfContent is the loader's check, with JavaScript's 32-bit "&".
func endOfContent(r *reader) bool {
	size := len(r.b)
	if size%16 == 0 {
		return int(int32(r.off+160+16)&-16) >= size
	}
	return r.off+160+16 >= size
}

func parseNode(r *reader, version uint32) (*obj, error) {
	n := newObj()
	var end, count float64
	var err error
	if version >= 7500 {
		if end, err = r.u64(); err != nil {
			return nil, err
		}
		if count, err = r.u64(); err != nil {
			return nil, err
		}
		if _, err = r.u64(); err != nil {
			return nil, err
		}
	} else {
		var e, c uint32
		if e, err = r.u32(); err != nil {
			return nil, err
		}
		if c, err = r.u32(); err != nil {
			return nil, err
		}
		if _, err = r.u32(); err != nil {
			return nil, err
		}
		end, count = float64(e), float64(c)
	}
	nameLen, err := r.u8()
	if err != nil {
		return nil, err
	}
	name, err := r.str(int(nameLen))
	if err != nil {
		return nil, err
	}
	if end == 0 {
		return nil, nil
	}
	if count > float64(len(r.b)) {
		return nil, errTruncated
	}
	props := make([]any, 0, int(count))
	for i := 0; i < int(count); i++ {
		p, err := parseProperty(r)
		if err != nil {
			return nil, err
		}
		props = append(props, p)
	}
	var id, attrName, attrType any = "", "", ""
	if len(props) > 0 {
		id = props[0]
	}
	if len(props) > 1 {
		attrName = props[1]
	}
	if len(props) > 2 {
		attrType = props[2]
	}
	n.set("singleProperty", count == 1 && float64(r.off) == end)
	for end > float64(r.off) {
		c, err := parseNode(r, version)
		if err != nil {
			return nil, err
		}
		if c != nil {
			parseSubNode(name, n, c)
		}
	}
	n.set("propertyList", props)
	if f, ok := id.(float64); ok {
		n.set("id", f)
	}
	if s, ok := attrName.(string); !ok || s != "" {
		n.set("attrName", attrName)
	}
	if s, ok := attrType.(string); !ok || s != "" {
		n.set("attrType", attrType)
	}
	if name != "" {
		n.set("name", name)
	}
	return n, nil
}

// parseSubNode puts child c of the node named parent (n) where the web's
// parser does.
func parseSubNode(parent string, n, c *obj) {
	cname, _ := c.str("name")
	props, _ := c.vals["propertyList"].([]any)
	single, _ := c.vals["singleProperty"].(bool)
	switch {
	case single:
		if a, ok := props[0].([]any); ok {
			n.set(cname, c)
			c.set("a", a)
		} else {
			n.set(cname, props[0])
		}
	case parent == "Connections" && cname == "C":
		var conn []any
		for i, p := range props {
			if i != 0 {
				conn = append(conn, p)
			}
		}
		list, _ := n.vals["connections"].([]any)
		n.set("connections", append(list, conn))
	case cname == "Properties70":
		for _, k := range c.keys {
			n.set(k, c.vals[k])
		}
	case parent == "Properties70" && cname == "P":
		if len(props) < 4 {
			return
		}
		key, _ := props[0].(string)
		typ, _ := props[1].(string)
		if strings.HasPrefix(key, "Lcl ") {
			key = strings.Replace(key, "Lcl ", "Lcl_", 1)
		}
		if strings.HasPrefix(typ, "Lcl ") {
			typ = strings.Replace(typ, "Lcl ", "Lcl_", 1)
		}
		var value any
		if typ == "Color" || typ == "ColorRGB" || typ == "Vector" || typ == "Vector3D" || strings.HasPrefix(typ, "Lcl_") {
			value = []any{at(props, 4), at(props, 5), at(props, 6)}
		} else {
			value = at(props, 4)
		}
		p := newObj()
		p.set("type", typ)
		p.set("type2", props[2])
		p.set("flag", props[3])
		p.set("value", value)
		n.set(key, p)
	default:
		id, hasID := c.vals["id"].(float64)
		existing, ok := n.vals[cname]
		if !ok {
			if hasID {
				m := newObj()
				m.set(numKey(id), c)
				n.set(cname, m)
			} else {
				n.set(cname, c)
			}
			return
		}
		if cname == "PoseNode" {
			if list, ok := existing.([]any); ok {
				n.set(cname, append(list, c))
			} else {
				n.set(cname, []any{existing, c})
			}
			return
		}
		m, ok := existing.(*obj)
		if !ok {
			return
		}
		key := "undefined"
		if hasID {
			key = numKey(id)
		}
		if !m.has(key) {
			m.set(key, c)
		}
	}
}

func at(list []any, i int) any {
	if i < len(list) {
		return list[i]
	}
	return nil
}

func parseProperty(r *reader) (any, error) {
	t, err := r.str(1)
	if err != nil {
		return nil, err
	}
	scalar := func(n int) ([]byte, error) { return r.bytes(n) }
	switch t {
	case "C":
		b, err := r.u8()
		return b&1 == 1, err
	case "D":
		b, err := scalar(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
	case "F":
		b, err := scalar(4)
		if err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), nil
	case "I":
		b, err := scalar(4)
		if err != nil {
			return nil, err
		}
		return float64(int32(binary.LittleEndian.Uint32(b))), nil
	case "L":
		b, err := scalar(8)
		if err != nil {
			return nil, err
		}
		return float64(int64(binary.LittleEndian.Uint64(b))), nil
	case "Y":
		b, err := scalar(2)
		if err != nil {
			return nil, err
		}
		return float64(int16(binary.LittleEndian.Uint16(b))), nil
	case "R":
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		return r.bytes(int(n))
	case "S":
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		return r.str(int(n))
	case "b", "c", "d", "f", "i", "l":
		count, err := r.u32()
		if err != nil {
			return nil, err
		}
		enc, err := r.u32()
		if err != nil {
			return nil, err
		}
		clen, err := r.u32()
		if err != nil {
			return nil, err
		}
		size := map[string]int{"b": 1, "c": 1, "d": 8, "f": 4, "i": 4, "l": 8}[t]
		var raw []byte
		if enc == 0 {
			if raw, err = r.bytes(int(count) * size); err != nil {
				return nil, err
			}
		} else {
			comp, err := r.bytes(int(clen))
			if err != nil {
				return nil, err
			}
			zr, err := zlib.NewReader(bytes.NewReader(comp))
			if err != nil {
				return nil, fmt.Errorf("an FBX array does not inflate: %w", err)
			}
			raw, err = io.ReadAll(io.LimitReader(zr, int64(count)*int64(size)+1))
			if err != nil {
				return nil, fmt.Errorf("an FBX array does not inflate: %w", err)
			}
			if len(raw) < int(count)*size {
				return nil, errTruncated
			}
		}
		out := make([]any, count)
		for i := range out {
			b := raw[i*size:]
			switch t {
			case "b", "c":
				out[i] = b[0]&1 == 1
			case "d":
				out[i] = math.Float64frombits(binary.LittleEndian.Uint64(b))
			case "f":
				out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
			case "i":
				out[i] = float64(int32(binary.LittleEndian.Uint32(b)))
			case "l":
				out[i] = float64(int64(binary.LittleEndian.Uint64(b)))
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown FBX property type %q", t)
}

// --- text ---

type textParser struct {
	indent   int
	all      *obj
	stack    []*obj
	prop     *obj
	propName string
}

var (
	commentRE  = regexp.MustCompile(`^[\s\t]*;`)
	blankRE    = regexp.MustCompile(`^[\s\t]*$`)
	continueRE = regexp.MustCompile(`^[^\s\t}]`)
	lineRE     = regexp.MustCompile(`[\r\n]+`)
	nsRE       = regexp.MustCompile(`^(\w+)::`)
	firstWS    = regexp.MustCompile(`\s`)
)

func parseText(text string) *obj {
	p := &textParser{all: newObj()}
	lines := lineRE.Split(text, -1)
	for i := 0; i < len(lines); i++ {
		t := lines[i]
		if commentRE.MatchString(t) || blankRE.MatchString(t) {
			continue
		}
		re := lineREs(p.indent)
		begin := re.begin.FindStringSubmatch(t)
		prop := re.prop.FindStringSubmatch(t)
		end := re.end != nil && re.end.MatchString(t)
		switch {
		case begin != nil:
			p.nodeBegin(begin)
		case prop != nil:
			next := ""
			if i+1 < len(lines) {
				next = lines[i+1]
			}
			p.nodeProperty(prop, next)
		case end:
			p.pop()
		case continueRE.MatchString(t):
			p.continued(t)
		}
	}
	return p.all
}

type indentREs struct{ begin, prop, end *regexp.Regexp }

var (
	indentMu    sync.Mutex
	indentCache = map[int]indentREs{}
)

// lineREs are the parser's three line patterns at an indent level (tabs).
func lineREs(indent int) indentREs {
	indentMu.Lock()
	defer indentMu.Unlock()
	if r, ok := indentCache[indent]; ok {
		return r
	}
	tabs := strings.Repeat(`\t`, max(indent, 0))
	r := indentREs{
		begin: regexp.MustCompile(`^` + tabs + `(\w+):(.*)\{`),
		prop:  regexp.MustCompile(`^` + tabs + `(\w+):[\s\t\r\n](.*)`),
	}
	if indent-1 >= 0 {
		r.end = regexp.MustCompile(`^` + strings.Repeat(`\t`, indent-1) + `\}`)
	}
	indentCache[indent] = r
	return r
}

func (p *textParser) current() *obj {
	if p.indent-1 >= 0 && p.indent-1 < len(p.stack) {
		return p.stack[p.indent-1]
	}
	return nil
}

func (p *textParser) prev() *obj {
	if p.indent-2 >= 0 && p.indent-2 < len(p.stack) {
		return p.stack[p.indent-2]
	}
	return nil
}

func (p *textParser) push(n *obj) { p.stack = append(p.stack, n); p.indent++ }

func (p *textParser) pop() {
	if len(p.stack) > 0 {
		p.stack = p.stack[:len(p.stack)-1]
	}
	p.indent--
}

func unquote(s string) string {
	s = strings.TrimPrefix(s, `"`)
	return strings.TrimSuffix(s, `"`)
}

func (p *textParser) nodeBegin(m []string) {
	name := unquote(strings.TrimSpace(m[1]))
	var attrs []string
	for _, a := range strings.Split(m[2], ",") {
		attrs = append(attrs, unquote(strings.TrimSpace(a)))
	}
	r := newObj()
	r.set("name", name)
	id, attrName, attrType := nodeAttr(attrs)
	a := p.current()
	idNum, isNum := id.(float64)
	switch {
	case p.indent == 0:
		p.all.set(name, r)
	case a == nil:
	case a.has(name):
		if name == "PoseNode" {
			if list, ok := a.vals["PoseNode"].([]any); ok {
				a.set("PoseNode", append(list, r))
			}
		} else if old, ok := a.vals[name].(*obj); ok && old.has("id") {
			// three.js: (a[n] = {}), (a[n][a[n].id] = a[n]): the old node is
			// lost and the new object holds itself under "undefined".
			m := newObj()
			m.set("undefined", m)
			a.set(name, m)
		}
		if s, ok := id.(string); !ok || s != "" {
			if m, ok := a.vals[name].(*obj); ok {
				key := ""
				if isNum {
					key = numKey(idNum)
				} else {
					key, _ = id.(string)
				}
				m.set(key, r)
			}
		}
	case isNum:
		m := newObj()
		m.set(numKey(idNum), r)
		a.set(name, m)
	case name != "Properties70":
		if name == "PoseNode" {
			a.set(name, []any{r})
		} else {
			a.set(name, r)
		}
	}
	if isNum {
		r.set("id", idNum)
	}
	if attrName != "" {
		r.set("attrName", attrName)
	}
	if attrType != "" {
		r.set("attrType", attrType)
	}
	p.push(r)
}

// nodeAttr is parseNodeAttr: id (a number when it parses, else the text),
// name without its "Class::" prefix, type.
func nodeAttr(attrs []string) (any, string, string) {
	var id any = attrs[0]
	if attrs[0] != "" {
		if f := parseIntJS(attrs[0]); !math.IsNaN(f) {
			id = f
		}
	}
	name, typ := "", ""
	if len(attrs) > 1 {
		name = nsRE.ReplaceAllString(attrs[1], "")
		if len(attrs) > 2 {
			typ = attrs[2]
		}
	}
	return id, name, typ
}

func (p *textParser) nodeProperty(m []string, next string) {
	key := strings.TrimSpace(unquote(m[1]))
	val := strings.TrimSpace(unquote(m[2]))
	if key == "Content" && val == "," {
		val = strings.TrimSpace(strings.TrimSuffix(strings.ReplaceAll(next, `"`, ""), ","))
	}
	s := p.current()
	if s == nil {
		return
	}
	if name, _ := s.str("name"); name == "Properties70" {
		p.special(val)
		return
	}
	var v any = val
	if key == "C" {
		parts := strings.Split(val, ",")
		rest := parts[1:]
		conn := []any{parseIntJS(at2(rest, 0)), parseIntJS(at2(rest, 1))}
		if len(parts) > 3 {
			for _, x := range parts[3:] {
				conn = append(conn, strings.TrimPrefix(strings.TrimSpace(x), `"`))
			}
		}
		key, v = "connections", conn
		if !s.has(key) {
			s.set(key, []any{})
		}
	}
	if key == "Node" {
		s.set("id", v)
	}
	if list, ok := s.vals[key].([]any); ok {
		s.set(key, append(list, v))
	} else if key != "a" {
		s.set(key, v)
	} else {
		s.set("a", v)
	}
	p.prop, p.propName = s, key
	if key == "a" && !strings.HasSuffix(val, ",") {
		s.set("a", floats(val))
	}
}

func at2(list []string, i int) string {
	if i < len(list) {
		return list[i]
	}
	return ""
}

func (p *textParser) continued(t string) {
	e := p.current()
	if e == nil {
		return
	}
	s, _ := e.vals["a"].(string)
	s += t
	if !strings.HasSuffix(t, ",") {
		e.set("a", floats(s))
	} else {
		e.set("a", s)
	}
}

// special is parseNodeSpecialProperty: a P line of Properties70, stored on
// the node that holds the Properties70.
func (p *textParser) special(val string) {
	parts := strings.Split(val, `",`)
	for i, x := range parts {
		x = strings.TrimPrefix(strings.TrimSpace(x), `"`)
		if loc := firstWS.FindStringIndex(x); loc != nil {
			parts[i] = x[:loc[0]] + "_" + x[loc[1]:]
		} else {
			parts[i] = x
		}
	}
	key, typ, type2, flag := at2(parts, 0), at2(parts, 1), at2(parts, 2), at2(parts, 3)
	var value any = at2(parts, 4)
	if len(parts) <= 4 {
		value = nil
	}
	switch typ {
	case "int", "enum", "bool", "ULongLong", "double", "Number", "FieldOfView":
		value = parseFloatJS(at2(parts, 4))
	case "Color", "ColorRGB", "Vector3D", "Lcl_Translation", "Lcl_Rotation", "Lcl_Scaling":
		value = floats(at2(parts, 4))
	}
	prev := p.prev()
	if prev == nil {
		return
	}
	o := newObj()
	o.set("type", typ)
	o.set("type2", type2)
	o.set("flag", flag)
	o.set("value", value)
	prev.set(key, o)
	p.prop, p.propName = prev, key
}

// floats is the parser's split(",").map(parseFloat).
func floats(s string) []any {
	var out []any
	for _, x := range strings.Split(s, ",") {
		out = append(out, parseFloatJS(x))
	}
	return out
}
