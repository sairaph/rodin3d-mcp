package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// inputSchema infers the schema of T and applies property defaults (JSON
// literals).
func inputSchema[T any](defaults map[string]string) *jsonschema.Schema {
	s, err := jsonschema.For[T](&jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	for name, value := range defaults {
		prop, ok := s.Properties[name]
		if !ok {
			panic(fmt.Sprintf("inputSchema: no property %q", name))
		}
		prop.Default = json.RawMessage(value)
	}
	return s
}

func property(s *jsonschema.Schema, name string) *jsonschema.Schema {
	prop, ok := s.Properties[name]
	if !ok {
		panic(fmt.Sprintf("schema: no property %q", name))
	}
	return prop
}

// withEnum restricts the named property (a string, or the items of an
// array) to values.
func withEnum(s *jsonschema.Schema, name string, values ...string) *jsonschema.Schema {
	prop := property(s, name)
	dropNullAlt(prop)
	target := prop
	if prop.Type == "array" && prop.Items != nil {
		target = prop.Items
	}
	target.Enum = make([]any, len(values))
	for i, v := range values {
		target.Enum[i] = v
	}
	return s
}

// withRange sets an inclusive minimum and maximum on numeric properties.
func withRange(s *jsonschema.Schema, lo, hi float64, names ...string) *jsonschema.Schema {
	for _, name := range names {
		prop := property(s, name)
		prop.Minimum, prop.Maximum = jsonschema.Ptr(lo), jsonschema.Ptr(hi)
	}
	return s
}

// withOpenMinRange sets an exclusive minimum and an inclusive maximum on
// numeric properties: (lo, hi].
func withOpenMinRange(s *jsonschema.Schema, lo, hi float64, names ...string) *jsonschema.Schema {
	for _, name := range names {
		prop := property(s, name)
		prop.ExclusiveMinimum, prop.Maximum = jsonschema.Ptr(lo), jsonschema.Ptr(hi)
	}
	return s
}

// withMin sets an inclusive minimum on a numeric property.
func withMin(s *jsonschema.Schema, lo float64, name string) *jsonschema.Schema {
	property(s, name).Minimum = jsonschema.Ptr(lo)
	return s
}

// dropNullAlt removes the "null" alternative inferred for pointer and slice
// fields: the property is either absent or a real value.
func dropNullAlt(prop *jsonschema.Schema) {
	if len(prop.Types) == 2 && prop.Types[0] == "null" {
		prop.Type, prop.Types = prop.Types[1], nil
	}
}

// nonNull drops the null alternative of every property of s.
func nonNull(s *jsonschema.Schema) *jsonschema.Schema {
	for _, p := range s.Properties {
		dropNullAlt(p)
	}
	return s
}
