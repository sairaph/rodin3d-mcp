//go:build windows

package install

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestRegistryUserPath runs the user PATH edit against a scratch key,
// HKEY_CURRENT_USER\Software\rodin3d-mcp-uninstall-test, never Environment,
// and deletes the key afterwards. It writes the registry, so it runs only
// with RODIN_REGISTRY_TEST=1.
func TestRegistryUserPath(t *testing.T) {
	if os.Getenv("RODIN_REGISTRY_TEST") != "1" {
		t.Skip("set RODIN_REGISTRY_TEST=1 to write a scratch registry key")
	}
	const scratch = `Software\rodin3d-mcp-uninstall-test`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, scratch, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		k.Close()
		if err := registry.DeleteKey(registry.CURRENT_USER, scratch); err != nil {
			t.Errorf("delete scratch key: %v", err)
		}
	}()
	store := registryUserPath{key: scratch}
	dir := `C:\Users\u\AppData\Local\rodin3d-mcp\bin`

	// REG_EXPAND_SZ stays REG_EXPAND_SZ, other entries stay unexpanded.
	if err := k.SetExpandStringValue("Path", dir+`;%USERPROFILE%\tools`); err != nil {
		t.Fatal(err)
	}
	if err := removeUserPathEntry(store, dir); err != nil {
		t.Fatal(err)
	}
	if v, typ, err := k.GetStringValue("Path"); err != nil || v != `%USERPROFILE%\tools` || typ != registry.EXPAND_SZ {
		t.Fatalf("got %q type %d err %v", v, typ, err)
	}

	// REG_SZ stays REG_SZ.
	if err := k.SetStringValue("Path", `C:\a;`+dir); err != nil {
		t.Fatal(err)
	}
	if err := removeUserPathEntry(store, dir); err != nil {
		t.Fatal(err)
	}
	if v, typ, err := k.GetStringValue("Path"); err != nil || v != `C:\a` || typ != registry.SZ {
		t.Fatalf("got %q type %d err %v", v, typ, err)
	}

	// Our entry alone (install.ps1 on an empty user PATH): the value goes.
	if err := k.SetStringValue("Path", dir); err != nil {
		t.Fatal(err)
	}
	if err := removeUserPathEntry(store, dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.GetStringValue("Path"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("value still there: %v", err)
	}

	// No value at all: nothing to do.
	if err := removeUserPathEntry(store, dir); err != nil {
		t.Fatal(err)
	}
}
