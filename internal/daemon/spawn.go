package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sairaph/mcp-wizard/daemon/lock"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// maxLog is the size at which daemon.log is moved to daemon.log.1 before a
// new daemon starts.
const maxLog = 1 << 20

// EnsureRunning starts `<exe> daemon` as a background process unless one
// holds the lock. It is mcp-wizard's lock.EnsureRunning with an own spawn:
// on Windows the daemon gets no console window and does not belong to the
// starting process's console, so it never shows a window and survives that
// console closing (see spawn_windows.go). Losing the start race to another
// process counts as running.
func EnsureRunning(exe string, paths domain.Paths) bool {
	opts := LockOptions(paths)
	if lock.IsRunning(opts.LockFile) {
		return true
	}
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return false
	}
	rotateLog(opts.LogFile)
	out, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	defer out.Close()
	start := func(breakaway bool) error {
		cmd := exec.Command(exe, "daemon")
		cmd.Dir = filepath.Dir(exe)
		cmd.Stdout, cmd.Stderr = out, out // stdin is the null device
		cmd.SysProcAttr = detachAttr(breakaway)
		if err := cmd.Start(); err != nil {
			return err
		}
		// The daemon outlives this process by design; release the handle.
		return cmd.Process.Release()
	}
	// First try to leave a job object the host may have put us in (a host
	// that kills its job on exit would take the daemon with it); a job that
	// does not allow it refuses, and the daemon starts inside it instead.
	if err := start(true); err != nil {
		if err := start(false); err != nil {
			fmt.Fprintf(out, "%s could not start the daemon from %s: %v\n", time.Now().UTC().Format(time.RFC3339), exe, err)
			return false
		}
	}
	for i := 0; i < 20; i++ {
		if lock.IsRunning(opts.LockFile) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return lock.IsRunning(opts.LockFile)
}

// rotateLog keeps one previous log when the current one has grown past
// maxLog. It runs before the new daemon opens the file.
func rotateLog(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLog {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}
