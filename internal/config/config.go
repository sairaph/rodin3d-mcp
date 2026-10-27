// Package config reads config.toml. Its only table is [permission]: one
// allow or deny per operation that changes something on Hyper3D or spends
// credits. Reads are always allowed and have no key.
//
// The file is read again on every call that needs it, so an edit applies
// without a restart. Every operation is allowed unless its key is set to
// "deny" (D5): a missing file, a missing key, a file that cannot be used and
// a value other than "deny" all mean allow.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// Values of a permission key.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Key is one permission key: its name in [permission] and the tools it covers.
type Key struct {
	Name  string
	Tools string
}

// Keys lists every permission key in file order.
var Keys = []Key{
	{"generate", "generate_model"},
	{"redo", "redo_geometry, redo_material, select_candidate"},
	{"confirm_geometry", "confirm_geometry"},
	{"generate_texture", "generate_texture"},
	{"confirm_material", "confirm_material"},
	{"export", "export_model"},
	{"fork", "fork_task"},
	{"agentic", "generate_agentic"},
	{"split_parts", "split_parts"},
	{"rig", "rig_model"},
	{"animate", "animate_model"},
	{"texture_model", "texture_model"},
}

// Permissions is the [permission] table. Every field holds Allow or Deny; a
// field holding anything else (the zero value included) counts as Allow.
type Permissions struct {
	Generate        string `toml:"generate" yaml:"generate"`
	Redo            string `toml:"redo" yaml:"redo"`
	ConfirmGeometry string `toml:"confirm_geometry" yaml:"confirm_geometry"`
	GenerateTexture string `toml:"generate_texture" yaml:"generate_texture"`
	ConfirmMaterial string `toml:"confirm_material" yaml:"confirm_material"`
	Export          string `toml:"export" yaml:"export"`
	Fork            string `toml:"fork" yaml:"fork"`
	Agentic         string `toml:"agentic" yaml:"agentic"`
	SplitParts      string `toml:"split_parts" yaml:"split_parts"`
	Rig             string `toml:"rig" yaml:"rig"`
	Animate         string `toml:"animate" yaml:"animate"`
	TextureModel    string `toml:"texture_model" yaml:"texture_model"`
}

// Config is the whole file.
type Config struct {
	Permission Permissions `toml:"permission"`
}

// Default is the configuration without a file: every key allowed.
func Default() Config {
	p := Permissions{}
	for _, k := range Keys {
		*p.field(k.Name) = Allow
	}
	return Config{Permission: p}
}

// field returns the field of key, or nil for an unknown key.
func (p *Permissions) field(key string) *string {
	switch key {
	case "generate":
		return &p.Generate
	case "redo":
		return &p.Redo
	case "confirm_geometry":
		return &p.ConfirmGeometry
	case "generate_texture":
		return &p.GenerateTexture
	case "confirm_material":
		return &p.ConfirmMaterial
	case "export":
		return &p.Export
	case "fork":
		return &p.Fork
	case "agentic":
		return &p.Agentic
	case "split_parts":
		return &p.SplitParts
	case "rig":
		return &p.Rig
	case "animate":
		return &p.Animate
	case "texture_model":
		return &p.TextureModel
	}
	return nil
}

// Get returns the value of key; ok is false for a key that does not exist.
func (p Permissions) Get(key string) (value string, ok bool) {
	f := p.field(key)
	if f == nil {
		return "", false
	}
	return *f, true
}

// Allowed reports whether key is allowed: every value but Deny allows. A
// name that is not a permission key is not allowed.
func (p Permissions) Allowed(key string) bool {
	v, ok := p.Get(key)
	return ok && v != Deny
}

// Denied lists the keys set to deny, in Keys order.
func (p Permissions) Denied() []string {
	var out []string
	for _, k := range Keys {
		if !p.Allowed(k.Name) {
			out = append(out, k.Name)
		}
	}
	return out
}

// Set changes key to value (Allow or Deny).
func (p *Permissions) Set(key, value string) error {
	f := p.field(key)
	if f == nil {
		return fmt.Errorf("no permission key %q", key)
	}
	if value != Allow && value != Deny {
		return fmt.Errorf("[permission] %s = %q; use \"allow\" or \"deny\"", key, value)
	}
	*f = value
	return nil
}

// Encode is the file Save writes: every key in Keys order, each with the
// tools it covers as a comment.
func Encode(p Permissions) []byte {
	var b bytes.Buffer
	b.WriteString("# rodin3d-mcp write permissions: \"allow\" or \"deny\" for each operation that\n")
	b.WriteString("# spends credits or changes a task on Hyper3D. Reads need no permission.\n")
	b.WriteString("# Only \"deny\" blocks an operation: a missing key or any other value allows it.\n")
	b.WriteString("# Every call reads this file again, so an edit applies at once.\n\n")
	b.WriteString("[permission]\n")
	width := 0
	for _, k := range Keys {
		width = max(width, len(k.Name))
	}
	for _, k := range Keys {
		v := Allow
		if !p.Allowed(k.Name) {
			v = Deny
		}
		fmt.Fprintf(&b, "%-*s = %-7s # %s\n", width, k.Name, `"`+v+`"`, k.Tools)
	}
	return b.Bytes()
}

// Save writes p to path as the whole file (Encode), atomically and readable
// only by the user. Anything else the file held is replaced.
func Save(path string, p Permissions) error {
	return domain.WriteFileAtomic(path, Encode(p), 0o600)
}

// ErrMalformed marks a config file that has a problem: it cannot be parsed,
// has an unknown key, or a value other than allow or deny. Load returns the
// configuration in force together with it, so callers keep working and can
// still report the problem.
var ErrMalformed = errors.New("config.toml is malformed")

// Load reads path and returns the configuration in force; an error only
// reports a problem and never denies anything. A missing file is the
// defaults (every key allowed) and no error, and a missing key is allowed.
// A file that cannot be read or parsed, or has an unknown key, is the
// defaults and an error naming the problem (wrapping ErrMalformed unless the
// read failed). A value other than allow or deny counts as allow, the other
// keys keep their values, and the error wrapping ErrMalformed names each
// such key.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w; every operation is allowed until it can be read", path, err)
	}
	var parsed Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return cfg, fmt.Errorf("%w: %s: unknown key: %s; every operation is allowed until it is fixed", ErrMalformed, path, strings.TrimSpace(strict.String()))
		}
		return cfg, fmt.Errorf("%w: %s: %v; every operation is allowed until it is fixed", ErrMalformed, path, err)
	}
	var bad []string
	for _, k := range Keys {
		v := strings.TrimSpace(*parsed.Permission.field(k.Name))
		switch v {
		case "":
			// Not set: keep the default.
		case Allow, Deny:
			*cfg.Permission.field(k.Name) = v
		default:
			// Only "deny" denies: the key keeps the default, allow.
			bad = append(bad, fmt.Sprintf("%s = %q", k.Name, v))
		}
	}
	if len(bad) > 0 {
		return cfg, fmt.Errorf("%w: %s: [permission] %s: only \"deny\" denies, so each counts as \"allow\"; use \"allow\" or \"deny\"", ErrMalformed, path, strings.Join(bad, ", "))
	}
	return cfg, nil
}
