package fbx

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// obj is a JavaScript object as the web's FBX parser builds it: keys keep
// their insertion order, and iteration lists the array-index keys first in
// ascending order, as for...in does (FBX object ids are such keys when they
// are small enough).
type obj struct {
	keys []string
	vals map[string]any
}

func newObj() *obj { return &obj{vals: map[string]any{}} }

func (o *obj) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *obj) has(k string) bool { _, ok := o.vals[k]; return ok }

func (o *obj) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// child returns o[k] as an object.
func (o *obj) child(k string) *obj {
	v, _ := o.vals[k].(*obj)
	return v
}

// str returns o[k] as a string ("" when absent or not a string).
func (o *obj) str(k string) (string, bool) {
	v, ok := o.vals[k].(string)
	return v, ok
}

// arrayIndex reports whether k is an array index key (0 .. 2^32-2 in
// canonical form).
func arrayIndex(k string) (uint64, bool) {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	if err != nil || n > 4294967294 {
		return 0, false
	}
	return n, true
}

// order returns the keys in for...in order.
func (o *obj) order() []string {
	var idx, rest []string
	for _, k := range o.keys {
		if _, ok := arrayIndex(k); ok {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, _ := arrayIndex(idx[i])
		b, _ := arrayIndex(idx[j])
		return a < b
	})
	return append(idx, rest...)
}

// numKey is the property key of a number (Number.prototype.toString for
// the integral values FBX ids are).
func numKey(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// parseIntJS is JavaScript parseInt(s) for the decimal ids of the parser:
// leading white space, an optional sign, then digits; NaN when none.
func parseIntJS(s string) float64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\v' || s[i] == '\f') {
		i++
	}
	sign := 1.0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		if s[i] == '-' {
			sign = -1
		}
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s[i:j], 64)
	if err != nil {
		return math.NaN()
	}
	return sign * f
}

// parseFloatJS is JavaScript parseFloat(s): the longest leading decimal
// number, NaN when none.
func parseFloatJS(s string) float64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\v' || s[i] == '\f') {
		i++
	}
	s = s[i:]
	for _, p := range []string{"Infinity", "+Infinity"} {
		if len(s) >= len(p) && s[:len(p)] == p {
			return math.Inf(1)
		}
	}
	if len(s) >= 9 && s[:9] == "-Infinity" {
		return math.Inf(-1)
	}
	end := 0
	j := 0
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	digits := false
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j, digits = j+1, true
	}
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j, digits = j+1, true
		}
	}
	if !digits {
		return math.NaN()
	}
	end = j
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		e := k
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > e {
			end = k
		}
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		// ParseFloat reports range errors with ±Inf or 0, as JavaScript gives.
		return f
	}
	return f
}

// num reads a JavaScript number value (float64 or an integer kind).
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// isJSSpace is JavaScript's \s (and the set trim removes).
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// trimJS is String.prototype.trim.
func trimJS(s string) string { return strings.TrimFunc(s, isJSSpace) }

var mixamoPrefix = regexp.MustCompile(`(?i)^mixamorig:?`)
