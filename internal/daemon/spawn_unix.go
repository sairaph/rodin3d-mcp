//go:build !windows

package daemon

import "syscall"

// DetachAttr puts the daemon in its own session, so it survives the terminal
// that started it closing and does not receive that terminal's signals. Job
// objects are a Windows notion; breakaway has no meaning here.
func DetachAttr(bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
