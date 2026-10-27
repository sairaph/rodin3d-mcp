package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMissingFileDeniesEverything(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Keys {
		if v, ok := cfg.Permission.Get(k.Name); !ok || v != Deny {
			t.Errorf("%s = %q, want deny", k.Name, v)
		}
		if cfg.Permission.Allowed(k.Name) {
			t.Errorf("%s allowed without a file", k.Name)
		}
	}
	if cfg.Permission.Allowed("no_such_key") {
		t.Error("an unknown key is allowed")
	}
}

func TestAllowAndDefaults(t *testing.T) {
	cfg, err := Load(write(t, "[permission]\ngenerate = \"allow\"\nexport = \"deny\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Permission.Allowed("generate") || cfg.Permission.Allowed("export") || cfg.Permission.Allowed("fork") {
		t.Errorf("unexpected permissions %+v", cfg.Permission)
	}
}

// TestSaveRoundTrip writes each key allowed alone, then every key denied, and
// reads the file back: Load must return exactly what Save was given.
func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cases := []Permissions{Default().Permission}
	for _, k := range Keys {
		p := Default().Permission
		if err := p.Set(k.Name, Allow); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, p)
	}
	for _, want := range cases {
		if err := Save(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatalf("load what Save wrote: %v\n%s", err, Encode(want))
		}
		if got.Permission != want {
			t.Fatalf("got %+v, want %+v", got.Permission, want)
		}
	}
	if err := (&Permissions{}).Set("no_such_key", Allow); err == nil {
		t.Error("Set accepted an unknown key")
	}
	if err := (&Permissions{}).Set("generate", "yes"); err == nil {
		t.Error("Set accepted a value other than allow or deny")
	}
}

func TestMalformedFilesFallBackToDeny(t *testing.T) {
	for name, body := range map[string]string{
		"syntax":      "[permission\ngenerate = allow",
		"unknown key": "[permission]\nspend_everything = \"allow\"\n",
		"bad value":   "[permission]\ngenerate = \"yes\"\n",
		"other table": "[sync]\ninterval = 5\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(write(t, body))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			for _, k := range Keys {
				if cfg.Permission.Allowed(k.Name) {
					t.Errorf("%s allowed from a malformed file", k.Name)
				}
			}
		})
	}
}
