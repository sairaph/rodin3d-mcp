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
