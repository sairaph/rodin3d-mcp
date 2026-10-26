package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/daemon/lock"
	"github.com/sairaph/mcp-wizard/daemon/socket"

	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// SocketName is the socket's name in the viewer folder (viewer.sock).
const SocketName = "viewer"

// SocketPath is the viewer process's socket.
func SocketPath(p domain.Paths) string { return filepath.Join(p.Viewer, SocketName+".sock") }

// LockPath is the lock the viewer process holds while it runs (the
// socket server's lock: one window at a time).
func LockPath(p domain.Paths) string { return filepath.Join(p.Viewer, "lock") }

// Running reports whether a viewer process holds its lock.
func Running(p domain.Paths) bool { return lock.IsRunning(LockPath(p)) }

// ErrNotRunning means no viewer window is open.
var ErrNotRunning = errors.New("no viewer window is open")

// Call sends one RPC to the viewer process and decodes its result. It
// returns ErrNotRunning when nothing listens on the viewer socket. It dials
// without probing the lock: a probe takes the lock for a moment, and a
// window starting in that moment would find it taken and quit.
func Call(ctx context.Context, p domain.Paths, method string, params, result any) error {
	c, err := socket.Dial(SocketPath(p))
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrNotRunning, err)
	}
	defer c.Close()
	return c.Call(ctx, method, params, result)
}

// DisplayAvailable reports whether this system can show a window, and why
// not. Windows and macOS always can; elsewhere the viewer needs an X server
// or XWayland (DISPLAY), as Ebitengine has no Wayland-only backend.
func DisplayAvailable() (bool, string) {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true, ""
	}
	if os.Getenv("DISPLAY") != "" {
		return true, ""
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return false, "this session runs Wayland without XWayland (DISPLAY is not set); the viewer window needs an X server or XWayland"
	}
	return false, "no display: DISPLAY is not set (a session without a desktop, such as SSH without X forwarding)"
}

// StartTimeout bounds how long Start waits for a new window to answer.
const StartTimeout = 20 * time.Second

// How long a window takes to end. A closing window saves the input sets
// still queued for at most SaveWait (the store's busy timeout is 5 s); a
// window told to stop by a signal whose loop does not close within
// StopGrace exits anyway, and one whose loop did close is given SaveWait
// and 2 s more. So a window ends within ExitWait of a close call or a stop
// signal.
const (
	SaveWait  = 10 * time.Second
	StopGrace = 5 * time.Second
	ExitWait  = StopGrace + SaveWait + 2*time.Second
)

// Start runs `<exe> viewer` as a background process unless a window is
// already open, and waits until its socket answers. started reports a new
// process. A process that exits before answering is reported with the
// lines it wrote to viewer.log.
func Start(ctx context.Context, exe string, p domain.Paths) (started bool, err error) {
	if Running(p) {
		return false, nil
	}
	if exe == "" {
		return false, errors.New("the path of this program is not known, so the viewer cannot be started")
	}
	if err := os.MkdirAll(p.Viewer, 0o700); err != nil {
		return false, err
	}
	rotate(p.ViewerLog)
	var offset int64
	if fi, err := os.Stat(p.ViewerLog); err == nil {
		offset = fi.Size()
	}
	out, err := os.OpenFile(p.ViewerLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer out.Close()
	exited := make(chan error, 1)
	var child *os.Process
	start := func(breakaway bool) error {
		cmd := exec.Command(exe, "viewer")
		cmd.Dir = filepath.Dir(exe)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.SysProcAttr = daemon.DetachAttr(breakaway)
		if err := cmd.Start(); err != nil {
			return err
		}
		child = cmd.Process
		go func() { exited <- cmd.Wait() }()
		return nil
	}
	// As the daemon: first try to leave a job object a host may kill on exit.
	if err := start(true); err != nil {
		if err := start(false); err != nil {
			return false, fmt.Errorf("start the viewer: %w", err)
		}
	}
	deadline := time.Now().Add(StartTimeout)
	for {
		var pong PingReply
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := Call(cctx, p, MethodPing, nil, &pong)
		cancel()
		if err == nil {
			return true, nil
		}
		select {
		case werr := <-exited:
			reason := logSince(p.ViewerLog, offset)
			if reason == "" && werr != nil {
				reason = werr.Error()
			}
			if reason == "" {
				reason = "it exited without a message"
			}
			return false, &StartError{Reason: reason}
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			// A window that never answers would hold the viewer lock for
			// good; the process this call started is stopped.
			_ = child.Kill()
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
			}
			return false, &StartError{Reason: fmt.Sprintf("the window did not answer within %s and was stopped; see %s", StartTimeout, p.ViewerLog)}
		}
	}
}

// Stop ends a viewer process that does not answer: it signals the process
// id in the viewer's PID file, and only while the viewer lock is held, so a
// process id reused by another program is never hit (Windows: terminated;
// elsewhere: interrupted, and the window exits within ExitWait, within
// StopGrace when its loop is stuck). ErrNotRunning means no window holds
// the lock.
func Stop(p domain.Paths) (pid int, err error) {
	if b, rerr := os.ReadFile(p.ViewerPID); rerr == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if err := lock.Stop(p.ViewerPID, LockPath(p)); err != nil {
		if errors.Is(err, lock.ErrNotRunning) {
			return pid, ErrNotRunning
		}
		return pid, err
	}
	return pid, nil
}

// StartError is a viewer process that could not open its window.
type StartError struct{ Reason string }

func (e *StartError) Error() string { return "the viewer window could not open: " + e.Reason }

// logSince returns the last lines written to path after offset (at most
// 600 bytes), trimmed.
func logSince(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return ""
	}
	// The window's own start line says nothing about the failure.
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(l, "] started (pid ") && !strings.Contains(l, " started (pid ") {
			keep = append(keep, l)
		}
	}
	s := strings.Join(keep, "\n")
	if len(s) > 600 {
		s = "..." + s[len(s)-600:]
	}
	return s
}

// maxLog is the size at which viewer.log is moved to viewer.log.1 before
// a new window starts.
const maxLog = 1 << 20

func rotate(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLog {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
}
