//go:build windows

package daemon

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// DetachAttr starts the daemon in its own process group with
// CREATE_NO_WINDOW: it runs with a console of its own that has no window, so
// a host without a console (a GUI client) never sees a console window appear,
// and closing the starting terminal does not send the daemon CTRL_CLOSE. The
// same flags as sana-mcp's daemon. breakaway adds CREATE_BREAKAWAY_FROM_JOB.
func DetachAttr(breakaway bool) *syscall.SysProcAttr {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}
