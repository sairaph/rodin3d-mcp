//go:build !windows

package install

// systemUserPath is nil: install.sh changes startup files, not a persisted
// user PATH.
func systemUserPath() userPathStore { return nil }
