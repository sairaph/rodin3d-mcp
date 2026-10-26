package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

// Graphics libraries loaded into the window process (Mesa's GL under
// WSLg, for one) can print the same warning on every frame to the
// process's stderr, which is viewer.log; at 60 frames a second that log
// would grow without end while the window is open. guardStderr (Unix)
// routes file descriptor 2 through collapse, which writes a line once and
// then only how often it repeated.

// repeatReport is how often a line that keeps repeating is counted in the log.
const repeatReport = 10 * time.Minute

// collapse copies lines from r to w, writing a run of identical lines as
// the first one plus a count when the run ends or every repeatReport.
func collapse(r io.Reader, w io.Writer) {
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	tick := time.NewTicker(repeatReport)
	defer tick.Stop()
	last, repeats := "", 0
	report := func() {
		if repeats > 0 {
			fmt.Fprintf(w, "(the line above repeated %d more times)\n", repeats)
			repeats = 0
		}
	}
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				report()
				return
			}
			if l == last {
				repeats++
				continue
			}
			report()
			last = l
			fmt.Fprintln(w, l)
		case <-tick.C:
			report()
		}
	}
}

// stderrPipe is the routed stderr while guardStderr is in effect.
var stderrPipe struct {
	restore func() // points fd 2 back at the original stderr
	w       *os.File
	done    chan struct{} // closed when collapse has written everything
}

// pipeStderr makes fd 2 the write end of a pipe whose lines collapse
// copies to the original stderr; dup2 does the swap (per OS). On any error
// stderr stays as it was.
func pipeStderr(dup2 func(oldfd, newfd int) error, dup func(fd int) (int, error)) {
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	orig, err := dup(2)
	if err != nil {
		r.Close()
		w.Close()
		return
	}
	if err := dup2(int(w.Fd()), 2); err != nil {
		r.Close()
		w.Close()
		return
	}
	out := os.NewFile(uintptr(orig), "stderr")
	// A crash ends the process before the pipe is read; its report goes to
	// the log directly.
	_ = debug.SetCrashOutput(out, debug.CrashOptions{})
	done := make(chan struct{})
	stderrPipe.restore = func() { _ = dup2(orig, 2) }
	stderrPipe.w, stderrPipe.done = w, done
	go func() {
		collapse(r, out)
		close(done)
	}()
}

// flushStderr ends the routing before the process exits, so the last lines
// (and the count of a repeating line) reach the log: fd 2 points back at
// the original stderr, the pipe's write end closes, and collapse drains
// what is left (at most 2 s). Run and its stop watchdog may both call it:
// it runs once, and a second caller waits until the first has finished.
func flushStderr() { stderrFlush.Do(endStderrPipe) }

var stderrFlush sync.Once

func endStderrPipe() {
	p := &stderrPipe
	if p.restore == nil {
		return
	}
	p.restore()
	p.restore = nil
	p.w.Close()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}
