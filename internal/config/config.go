// Package config reads config.toml. Its only table is [permission]: one
// allow or deny per operation that changes something on Hyper3D or spends
// credits. Reads are always allowed and have no key.
//
// The file is read again on every call that needs it, so an edit applies
// without a restart. A missing file means every key is "deny".
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

// Permissions is the [permission] table. Every field holds Allow or Deny.
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

// Default is the configuration without a file: every key denied.
func Default() Config {
	p := Permissions{}
	for _, k := range Keys {
		*p.field(k.Name) = Deny
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

// Allowed reports whether key is set to allow. Unknown keys are denied.
func (p Permissions) Allowed(key string) bool {
	v, ok := p.Get(key)
	return ok && v == Allow
}

// ErrMalformed marks a config file that exists but cannot be used. Load
// returns the defaults together with it, so callers keep working with every
// write denied and can still report the problem.
var ErrMalformed = errors.New("config.toml is malformed")

// Load reads path. A missing file is the defaults and no error. A file that
// cannot be parsed, has an unknown key, or a value other than allow or deny
// returns the defaults and an error wrapping ErrMalformed that names the
// problem.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return Default(), fmt.Errorf("%w: %s: unknown key: %s", ErrMalformed, path, strings.TrimSpace(strict.String()))
		}
		return Default(), fmt.Errorf("%w: %s: %v", ErrMalformed, path, err)
	}
	for _, k := range Keys {
		v := strings.TrimSpace(*parsed.Permission.field(k.Name))
		switch v {
		case "":
			// Not set: keep the default.
		case Allow, Deny:
			*cfg.Permission.field(k.Name) = v
		default:
			return Default(), fmt.Errorf("%w: %s: [permission] %s = %q; use \"allow\" or \"deny\"", ErrMalformed, path, k.Name, v)
		}
	}
	return cfg, nil
}
