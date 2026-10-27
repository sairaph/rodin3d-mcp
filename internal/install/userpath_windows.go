//go:build windows

package install

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// registryUserPath is the user PATH install.ps1 writes: the raw value Path
// under HKEY_CURRENT_USER\Environment, set with its own registry type and
// %VAR% left unexpanded. key is a field so a test can use a scratch key.
type registryUserPath struct{ key string }

func systemUserPath() userPathStore { return registryUserPath{key: environmentKey} }

const environmentKey = `Environment`

func (r registryUserPath) read() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, r.key, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

// write keeps the value's type (REG_EXPAND_SZ or REG_SZ), deletes the value
// when nothing is left, and tells running programs the environment changed,
// as SetEnvironmentVariable does.
func (r registryUserPath) write(value string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, r.key, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	_, typ, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	switch {
	case value == "":
		err = k.DeleteValue("Path")
	case typ == registry.EXPAND_SZ:
		err = k.SetExpandStringValue("Path", value)
	default:
		err = k.SetStringValue("Path", value)
	}
	if err != nil {
		return err
	}
	if r.key == environmentKey {
		broadcastEnvironmentChange()
	}
	return nil
}

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvironmentChange sends WM_SETTINGCHANGE "Environment" to the
// top-level windows, so terminals opened from Explorer get the new PATH. A
// window that does not answer is skipped (SMTO_ABORTIFHUNG, 5 s each).
func broadcastEnvironmentChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	name, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = procSendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(name)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}
