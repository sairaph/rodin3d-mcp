//go:build windows

package app

// guardStderr does nothing on Windows: the DirectX path prints nothing per
// frame.
func guardStderr() {}
