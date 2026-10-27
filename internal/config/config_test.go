package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// allAllowed fails unless every key of p is allowed.
func allAllowed(t *testing.T, p Permissions, from string) {
	t.Helper()
	for _, k := range Keys {
		if !p.Allowed(k.Name) {
			t.Errorf("%s denied %s", k.Name, from)
		}
	}
	if d := p.Denied(); len(d) != 0 {
		t.Errorf("Denied() = %v %s", d, from)
	}
}

func TestMissingFileAllowsEverything(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Keys {
		if v, ok := cfg.Permission.Get(k.Name); !ok || v != Allow {
			t.Errorf("%s = %q, want allow", k.Name, v)
		}
	}
	allAllowed(t, cfg.Permission, "without a file")
	if cfg.Permission.Allowed("no_such_key") {
		t.Error("a name that is no permission key is allowed")
	}
}

func TestEmptyFileAndZeroValueAllow(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "empty table": "[permission]\n"} {
		cfg, err := Load(write(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		allAllowed(t, cfg.Permission, "from an "+name+" file")
	}
	allAllowed(t, Permissions{}, "in the zero value")
}

// Only "deny" denies; a missing key keeps the default, allow.
func TestDenyAndMissingKeys(t *testing.T) {
	cfg, err := Load(write(t, "[permission]\ngenerate = \"allow\"\nexport = \"deny\"\nrig = \" deny \"\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Permission
	if !p.Allowed("generate") || p.Allowed("export") || p.Allowed("rig") || !p.Allowed("fork") {
		t.Errorf("unexpected permissions %+v", p)
	}
	if d := strings.Join(p.Denied(), ","); d != "export,rig" {
		t.Errorf("Denied() = %s, want export,rig", d)
	}
}

// A value other than allow or deny counts as allow; the other keys keep
// their values and the error names every such key.
func TestBadValuesAllow(t *testing.T) {
	cfg, err := Load(write(t, "[permission]\ngenerate = \"yes\"\nexport = \"deny\"\nfork = \"Deny\"\n"))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	for _, want := range []string{`generate = "yes"`, `fork = "Deny"`, `only "deny" denies, so each counts as "allow"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %s", err, want)
		}
	}
	p := cfg.Permission
	if v, _ := p.Get("generate"); v != Allow || !p.Allowed("fork") || p.Allowed("export") {
		t.Errorf("unexpected permissions %+v", p)
	}
}

// A file that cannot be parsed allows everything and says why.
func TestUnparsableFileAllowsEverything(t *testing.T) {
	cfg, err := Load(write(t, "[permission\ngenerate = allow"))
	if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "every operation is allowed until it is fixed") {
		t.Fatalf("err = %v, want ErrMalformed saying everything is allowed", err)
	}
	allAllowed(t, cfg.Permission, "from an unparsable file")
}

// An unknown key or a wrong-typed value counts as allow for itself only: the
// valid keys of the same file still apply, and the error names each bad line.
func TestBadLinesKeepTheOtherKeys(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		denied string
		says   []string
	}{
		"unknown key": {
			body:   "[permission]\nexport = \"deny\"\nrigg = \"deny\"\n",
			denied: "export",
			says:   []string{`unknown key "rigg"`},
		},
		"wrong type": {
			body:   "[permission]\ngenerate = false\nfork = \"deny\"\n",
			denied: "fork",
			says:   []string{"generate = false (not a string)"},
		},
		"other table and not a table": {
			body:   "spend = 1\n[sync]\ninterval = 5\n[permission]\nrig = \"deny\"\nanimate = 3\n",
			denied: "rig",
			says:   []string{`unknown key "spend" outside the table`, `unknown key "sync" outside the table`, "animate = 3 (not a string)"},
		},
		"permission not a table": {
			body: "permission = \"deny\"\n",
			says: []string{"permission is not a table"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(write(t, tc.body))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			for _, want := range append(tc.says, "the other keys apply") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %s", err, want)
				}
			}
			if d := strings.Join(cfg.Permission.Denied(), ","); d != tc.denied {
				t.Errorf("Denied() = %q, want %q", d, tc.denied)
			}
		})
	}
}

func TestUnreadableFileAllowsEverything(t *testing.T) {
	dir := t.TempDir() // a directory where the file should be cannot be read
	cfg, err := Load(dir)
	if err == nil || errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "every operation is allowed until it can be read") {
		t.Fatalf("err = %v, want a read error saying everything is allowed", err)
	}
	allAllowed(t, cfg.Permission, "from an unreadable file")
}

// TestSaveRoundTrip writes each key denied alone, then every key allowed,
// and reads the file back: Load must return exactly what Save was given.
func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cases := []Permissions{Default().Permission}
	for _, k := range Keys {
		p := Default().Permission
		if err := p.Set(k.Name, Deny); err != nil {
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
	// The zero value is written as every key allowed.
	if err := Save(path, Permissions{}); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.Permission != Default().Permission {
		t.Fatalf("zero value saved as %+v (%v), want every key allowed", got.Permission, err)
	}
	if !strings.Contains(string(Encode(Permissions{})), "Only \"deny\" blocks an operation") {
		t.Error("the file does not say that only deny blocks")
	}
	if err := (&Permissions{}).Set("no_such_key", Allow); err == nil {
		t.Error("Set accepted an unknown key")
	}
	if err := (&Permissions{}).Set("generate", "yes"); err == nil {
		t.Error("Set accepted a value other than allow or deny")
	}
}
