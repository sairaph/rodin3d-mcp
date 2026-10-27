package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// What the terminal app and the installer read besides the tool results.

// CancelLogin ends the loopback listener of a sign-in this process started;
// its Done channel then reports auth.ErrLoginReplaced. The pending sign-in
// record stays, so a final address pasted later still completes it.
func (s *Service) CancelLogin() { s.login.StopListener() }

// OpenBrowser opens address in the browser of this computer (a no-op error
// when the service was made without a browser opener).
func (s *Service) OpenBrowser(address string) error {
	if s.opts.OpenBrowser == nil {
		return errors.New("no browser opener")
	}
	return s.opts.OpenBrowser(address)
}

// PreviewFile is the PNG a get_preview result saved ("" for another result).
func PreviewFile(res Result) string {
	if f, ok := res.Front.(previewResultFront); ok {
		return f.Preview.File
	}
	return ""
}

// BrowserOpened reports whether the login result res opened the browser.
func BrowserOpened(res Result) bool {
	f, ok := res.Front.(loginStartFront)
	return ok && f.BrowserOpened
}

// TaskRow is one task of the index as the app's table shows it: a list_tasks
// row (listRow has the same fields in the same order).
type TaskRow struct {
	Prompt  string
	ID      string
	Stage   string
	State   string
	Tier    string
	Views   string
	Created string
}

// SyncStatus is the syncer's state for a status line.
type SyncStatus struct {
	Running  bool
	Phase    string
	LastSync time.Time
	Error    string
	Tasks    int
}

// Sync reads the syncer's state from the store.
func (s *Service) Sync() (SyncStatus, error) {
	st, err := s.storeOrFail()
	if err != nil {
		return SyncStatus{}, err
	}
	state, err := st.State()
	if err != nil {
		return SyncStatus{}, err
	}
	out := SyncStatus{Running: s.DaemonRunning(), Phase: state.Phase, LastSync: state.LastSync, Error: state.LastError}
	if tasks, err := st.Tasks(); err == nil {
		out.Tasks = len(tasks)
	}
	return out, nil
}

// TaskRows is the task index of the signed-in account, newest first, the
// rows list_tasks shows without its filters and pages. It does not wait for
// the first history sync: synced is false until that sync has finished, and
// the rows are then empty.
func (s *Service) TaskRows(ctx context.Context) (rows []TaskRow, synced bool, err error) {
	st, err := s.storeOrFail()
	if err != nil {
		return nil, false, err
	}
	creds, err := s.signedIn(ctx)
	if err != nil {
		return nil, false, err
	}
	s.EnsureDaemon(ctx)
	state, err := st.State()
	if err != nil {
		return nil, false, err
	}
	if !syncedFor(state, creds.UserUUID) {
		return nil, false, nil
	}
	tasks, err := st.Tasks()
	if err != nil {
		return nil, false, err
	}
	rows = make([]TaskRow, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, taskRow(t))
	}
	return rows, true, nil
}

func taskRow(t store.Task) TaskRow {
	ts := stage.Of(t.Step, t.UserState, t.PendingJobs)
	return TaskRow{
		Prompt: clip(t.Prompt, 120), ID: t.UUID, Stage: ts.Stage, State: ts.State,
		Tier: strings.TrimSpace(t.Mode + " " + t.Tier), Views: strings.Join(t.Views, ","), Created: t.Created,
	}
}
