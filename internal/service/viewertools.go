package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/imaging"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// The viewer tools (plan 2.6.4): open_viewer, show_in_viewer,
// control_viewer, get_viewer_state, close_viewer. Each is a call over the
// viewer process's socket; the window holds the state.

const (
	// viewerCallTimeout bounds one call to the window (the socket client
	// gives up after 30 s on its own).
	viewerCallTimeout = 28 * time.Second
	// captureWait is how long a screenshot waits for a full-quality frame
	// before it takes the current one (plan 2.6.4: 10 s).
	captureWait = 10 * time.Second
	// ViewerShotMin and ViewerShotMax bound get_viewer_state size.
	ViewerShotMin = 256
	ViewerShotMax = 1600
)

const hintOpenViewer = "Call open_viewer (only when the user asks to see something)."

const userMayChange = "The user can change the view at any time: read get_viewer_state before relying on what is shown."

type viewerFront struct {
	Open     bool               `yaml:"open"`
	PID      int                `yaml:"pid,omitempty"`
	Revision int64              `yaml:"revision"`
	Window   *viewer.WindowInfo `yaml:"window,omitempty"`
}

type shownFront struct {
	Kind       string   `yaml:"kind"`
	TaskID     string   `yaml:"task_id,omitempty"`
	Stage      string   `yaml:"stage,omitempty"`
	Version    string   `yaml:"version,omitempty"`
	Mode       string   `yaml:"mode,omitempty"`
	Modes      []string `yaml:"available_modes,omitempty,flow"`
	InputSetID string   `yaml:"input_set_id,omitempty"`
}

type shotFront struct {
	Width   int    `yaml:"width"`
	Height  int    `yaml:"height"`
	Quality string `yaml:"quality"`
}

type viewerResultFront struct {
	Viewer      viewerFront         `yaml:"viewer"`
	Started     *bool               `yaml:"started,omitempty"`
	Shown       *shownFront         `yaml:"shown,omitempty"`
	InputSetID  string              `yaml:"input_set_id,omitempty"`
	State       *viewer.State       `yaml:"state,omitempty"`
	UserActions []viewer.UserAction `yaml:"user_actions,omitempty"`
	// UserActionsTruncated: older user actions after since_revision were
	// dropped (the window keeps the last 50).
	UserActionsTruncated bool       `yaml:"user_actions_truncated,omitempty"`
	Screenshot           *shotFront `yaml:"screenshot,omitempty"`
}

// viewerCall calls the window, turning a closed or silent window and a
// refused call into tool errors.
func (s *Service) viewerCall(ctx context.Context, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, viewerCallTimeout)
	defer cancel()
	err := viewer.Call(ctx, s.paths, method, params, result)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, viewer.ErrNotRunning):
		return fail(render.CodeUnavailable, "No viewer window is open.", hintOpenViewer)
	case errors.Is(err, context.Canceled):
		return err
	}
	return fail(render.CodeUnavailable, "The viewer window did not answer: "+err.Error()+".",
		"Call get_viewer_state again; if the window stays silent, close_viewer stops a window that does not answer, then open_viewer starts a new one.")
}

// refused turns a refusal in a reply into the tool error.
func refused(e *viewer.Error) error {
	if e == nil {
		return nil
	}
	return failFields(e.Code, e.Message, e.Hint, e.Fields)
}

func frontOf(rep viewer.StateReply) viewerFront {
	w := rep.State.Window
	return viewerFront{Open: true, PID: rep.PID, Revision: rep.State.Revision, Window: &w}
}

func shownOf(st viewer.State) *shownFront {
	sh := &shownFront{Kind: st.Content.Kind, TaskID: st.Content.TaskID, Stage: st.Stage, Version: st.Version, Mode: st.Mode, Modes: st.Modes, InputSetID: st.Content.InputSetID}
	return sh
}

// describe says in one sentence what the window shows.
func describe(st viewer.State) string {
	var b strings.Builder
	switch st.Content.Kind {
	case viewer.KindNone, "":
		return "The viewer window is open and shows nothing yet; show_in_viewer puts a task, images or a model in it."
	case viewer.KindTask:
		fmt.Fprintf(&b, "The viewer shows %q (task %s), stage %s", clip(st.Content.Prompt, 60), short(st.Content.TaskID), st.Stage)
		if st.Version != "" && st.Stage == "geometry_draft" {
			fmt.Fprintf(&b, " version %s", st.Version)
		}
	case viewer.KindCompare:
		b.WriteString("The viewer compares")
		for i, p := range st.Panes {
			if i > 0 {
				b.WriteString(" and")
			}
			fmt.Fprintf(&b, " %q (%s, %s)", clip(p.Prompt, 40), short(p.TaskID), p.Stage)
		}
	case viewer.KindInputSet, viewer.KindTaskInputs:
		n := 0
		if st.InputSet != nil {
			n = len(st.InputSet.Images)
		}
		fmt.Fprintf(&b, "The viewer shows input set %s (%d images with their view tags)", st.Content.InputSetID, n)
	case viewer.KindTextures:
		fmt.Fprintf(&b, "The viewer shows the texture maps of %q (task %s)", clip(st.Content.Prompt, 60), short(st.Content.TaskID))
	case viewer.KindModelFile:
		fmt.Fprintf(&b, "The viewer shows the model file %s", st.Content.File)
	case viewer.KindImages:
		fmt.Fprintf(&b, "The viewer shows %d images", len(st.Content.Files))
	}
	fmt.Fprintf(&b, "; layout %s", st.Layout)
	if st.Mode != "" && (st.Layout == viewer.LayoutSingle || st.Layout == viewer.LayoutCompare) {
		fmt.Fprintf(&b, ", mode %s, view %s", st.Mode, st.View)
	}
	b.WriteString(".")
	for _, p := range st.Panes {
		switch p.Status {
		case "loading":
			fmt.Fprintf(&b, " Pane %d is still loading its mesh.", p.Index)
		case "error":
			fmt.Fprintf(&b, " Pane %d could not be drawn: %s", p.Index, p.Error)
		default:
			// Ready, but this mode cannot be drawn (a texture map that did not
			// load, a render error); the other modes can.
			if p.Error != "" {
				fmt.Fprintf(&b, " Pane %d cannot draw %s", p.Index, strings.TrimSuffix(p.Error, ".")+".")
			}
		}
		if p.Note != "" {
			fmt.Fprintf(&b, " Pane %d: %s.", p.Index, p.Note)
		}
	}
	if st.Message != "" {
		fmt.Fprintf(&b, " Banner: %q.", st.Message)
	}
	return b.String()
}

// capture takes a screenshot, waiting up to captureWait for a full-quality
// frame; size > 0 shrinks it to that longer edge. since, when set, asks
// for the user's actions after that revision, read with the screenshot.
func (s *Service) capture(ctx context.Context, size int, since *int64) (viewer.CaptureReply, []byte, error) {
	var shot viewer.CaptureReply
	if err := s.viewerCall(ctx, viewer.MethodCapture, viewer.CaptureParams{WaitMS: int(captureWait / time.Millisecond), SinceRevision: since}, &shot); err != nil {
		return shot, nil, err
	}
	if err := refused(shot.Error); err != nil {
		return shot, nil, err
	}
	png := shot.PNG
	if size > 0 && (shot.Width > size || shot.Height > size) {
		out, w, h, err := imaging.ToPNG(shot.PNG, size)
		if err != nil {
			return shot, nil, wrapf(err, "scale the screenshot")
		}
		png, shot.Width, shot.Height = out, w, h
	}
	return shot, png, nil
}

func shotNote(shot viewer.CaptureReply) string {
	if shot.Quality == viewer.QualityFull {
		return fmt.Sprintf(" The screenshot (%dx%d) is the window as the user sees it.", shot.Width, shot.Height)
	}
	return fmt.Sprintf(" The screenshot (%dx%d) is a quick frame: the content was still loading or moving after %d s; call get_viewer_state for the full-quality picture.", shot.Width, shot.Height, shot.Waited/1000)
}

// ViewerOpenInput is open_viewer's input.
type ViewerOpenInput struct {
	Content *viewer.Content
	Mode    string
	View    string
}

// OpenViewer is open_viewer: start the window (or raise the open one), show
// the content when given, and return a screenshot.
func (s *Service) OpenViewer(ctx context.Context, in ViewerOpenInput) (Result, error) {
	if in.Content != nil {
		if _, err := in.Content.Kind(); err != nil {
			return Result{}, fail(render.CodeInvalidInput, capital(err.Error())+".", "Call open_viewer with show holding exactly one kind of content.")
		}
	}
	if ok, why := viewer.DisplayAvailable(); !ok {
		return Result{}, fail(render.CodeUnavailable, "The viewer window cannot open on this computer: "+why+".",
			"Use get_preview for pictures of a task; the viewer needs a desktop session.")
	}
	started, err := s.startViewer(ctx)
	if err != nil {
		return Result{}, err
	}
	replaced := false
	if !started {
		// A window of another build of this program (opened before an
		// update) may speak another protocol: it is closed and a new one
		// started.
		var pong viewer.PingReply
		if err := s.viewerCall(ctx, viewer.MethodPing, nil, &pong); err != nil {
			return Result{}, err
		}
		if pong.Version != s.opts.Version {
			if _, err := s.CloseViewer(ctx); err != nil {
				return Result{}, err
			}
			if started, err = s.startViewer(ctx); err != nil {
				return Result{}, err
			}
			replaced = started
		}
	}
	var rep viewer.StateReply
	if !started {
		if err := s.viewerCall(ctx, viewer.MethodRaise, nil, &rep); err != nil {
			return Result{}, err
		}
	}
	switch {
	case in.Content != nil:
		if err := s.viewerCall(ctx, viewer.MethodShow, viewer.ShowParams{Content: *in.Content, Mode: in.Mode, View: in.View}, &rep); err != nil {
			return Result{}, err
		}
	case in.Mode != "" || in.View != "":
		c := viewer.Control{}
		if in.Mode != "" {
			c.Mode = &in.Mode
		}
		if in.View != "" {
			c.View = &in.View
		}
		if err := s.viewerCall(ctx, viewer.MethodControl, c, &rep); err != nil {
			return Result{}, err
		}
	}
	if err := refused(rep.Error); err != nil {
		return Result{}, err
	}
	shot, png, err := s.capture(ctx, 0, nil)
	if err != nil {
		return Result{}, err
	}
	st := shot.State
	front := viewerResultFront{Viewer: frontOf(viewer.StateReply{State: st, PID: shot.PID}), Started: &started, State: &st,
		Screenshot: &shotFront{Width: shot.Width, Height: shot.Height, Quality: shot.Quality}}
	if in.Content != nil {
		front.Shown = shownOf(st)
		front.InputSetID = st.Content.InputSetID
	}
	lead := "Opened the viewer window. "
	switch {
	case replaced:
		lead = "The open viewer window came from another version of this program; it was closed and a new one opened. "
	case !started:
		lead = "The viewer window was already open; it was brought to the front. "
	}
	body := lead + describe(st) + shotNote(shot) + " " + userMayChange +
		"\n\nNext: show_in_viewer for other content, control_viewer to change the mode, stage, camera or layout or to put a message on the banner, close_viewer when the user is done."
	return Result{Front: front, Body: body, Images: []Image{{PNG: png, What: "viewer screenshot"}}}, nil
}

// startViewer starts the window unless one is open (started reports a new
// one); a window that cannot open is unavailable with its reason.
func (s *Service) startViewer(ctx context.Context) (bool, error) {
	started, err := viewer.Start(ctx, s.opts.Executable, s.paths)
	var se *viewer.StartError
	if errors.As(err, &se) {
		return false, fail(render.CodeUnavailable, capital(se.Error())+".", "Use get_preview for pictures of a task; "+s.paths.ViewerLog+" has the window's log.")
	}
	if err != nil {
		return false, wrapf(err, "start the viewer")
	}
	return started, nil
}

// ShowInViewer is show_in_viewer: put new content in the open window.
func (s *Service) ShowInViewer(ctx context.Context, p viewer.ShowParams) (Result, error) {
	if _, err := p.Content.Kind(); err != nil {
		return Result{}, fail(render.CodeInvalidInput, capital(err.Error())+".", "Call show_in_viewer with content holding exactly one kind.")
	}
	var rep viewer.StateReply
	if err := s.viewerCall(ctx, viewer.MethodShow, p, &rep); err != nil {
		return Result{}, err
	}
	if err := refused(rep.Error); err != nil {
		return Result{}, err
	}
	shot, png, err := s.capture(ctx, 0, nil)
	if err != nil {
		return Result{}, err
	}
	st := shot.State
	front := viewerResultFront{Viewer: frontOf(viewer.StateReply{State: st, PID: shot.PID}), Shown: shownOf(st), InputSetID: st.Content.InputSetID, State: &st,
		Screenshot: &shotFront{Width: shot.Width, Height: shot.Height, Quality: shot.Quality}}
	body := describe(st) + shotNote(shot) + " " + userMayChange
	if st.Content.InputSetID != "" {
		body += fmt.Sprintf("\n\nThe images are saved as input set %s: the user can retag, reorder and reweight them in the window; generate_model takes input_set %s.", st.Content.InputSetID, st.Content.InputSetID)
	}
	body += "\n\nNext: control_viewer to change what is shown; get_viewer_state to see it with the user's changes."
	return Result{Front: front, Body: body, Images: []Image{{PNG: png, What: "viewer screenshot"}}}, nil
}

// ControlViewer is control_viewer: change the view of the open window.
func (s *Service) ControlViewer(ctx context.Context, c viewer.Control) (Result, error) {
	if c.Empty() {
		return Result{}, fail(render.CodeInvalidInput, "control_viewer got nothing to change.", "Pass at least one of its fields (mode, stage, view, orbit, zoom, layout, message, ...).")
	}
	var rep viewer.StateReply
	if err := s.viewerCall(ctx, viewer.MethodControl, c, &rep); err != nil {
		return Result{}, err
	}
	if err := refused(rep.Error); err != nil {
		return Result{}, err
	}
	st := rep.State
	front := viewerResultFront{Viewer: frontOf(rep), State: &st}
	body := "Applied. " + describe(st) + " " + userMayChange + "\n\nNext: get_viewer_state for a screenshot."
	return Result{Front: front, Body: body}, nil
}

// ViewerStateInput is get_viewer_state's input.
type ViewerStateInput struct {
	Screenshot    bool
	Size          int
	SinceRevision int64
}

// ViewerState is get_viewer_state: the shared state, the user's actions
// since a revision and a screenshot of the window. With a screenshot, the
// state and the actions are read at the moment the screenshot is taken, so
// every action up to the reply's revision is listed.
func (s *Service) ViewerState(ctx context.Context, in ViewerStateInput) (Result, error) {
	if in.Size != 0 && (in.Size < ViewerShotMin || in.Size > ViewerShotMax) {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("size %d is outside %d-%d.", in.Size, ViewerShotMin, ViewerShotMax), "Call get_viewer_state with size between 256 and 1600, or without it for the window's size.")
	}
	var front viewerResultFront
	var st viewer.State
	var actions []viewer.UserAction
	var truncated bool
	var images []Image
	note := ""
	if in.Screenshot {
		since := in.SinceRevision
		shot, png, err := s.capture(ctx, in.Size, &since)
		if err != nil {
			return Result{}, err
		}
		st, actions, truncated = shot.State, shot.Actions, shot.ActionsTruncated
		front.Viewer = frontOf(viewer.StateReply{State: shot.State, PID: shot.PID})
		front.Screenshot = &shotFront{Width: shot.Width, Height: shot.Height, Quality: shot.Quality}
		images = []Image{{PNG: png, What: "viewer screenshot"}}
		note = shotNote(shot)
	} else {
		var rep viewer.StateReply
		if err := s.viewerCall(ctx, viewer.MethodState, viewer.StateParams{SinceRevision: in.SinceRevision}, &rep); err != nil {
			return Result{}, err
		}
		st, actions, truncated = rep.State, rep.Actions, rep.ActionsTruncated
		front.Viewer = frontOf(rep)
	}
	front.State, front.UserActions, front.UserActionsTruncated = &st, actions, truncated
	body := describe(st) + note
	switch n := len(actions); {
	case n == 0 && in.SinceRevision > 0:
		body += fmt.Sprintf(" The user changed nothing since revision %d.", in.SinceRevision)
	case n > 0:
		body += fmt.Sprintf(" The user made %d change(s) since revision %d (user_actions).", n, in.SinceRevision)
	}
	if truncated {
		body += " Older changes after that revision were dropped (the window keeps the last 50): read the state itself for what is shown now."
	}
	body += fmt.Sprintf("\n\nNext: pass since_revision %d next time to see only newer user changes; control_viewer with expected_revision %d changes the view unless the user changed it first.", st.Revision, st.Revision)
	return Result{Front: front, Body: body, Images: images}, nil
}

// How long close_viewer waits for the window to end: the window's own
// bound in each case and a few seconds for the process to go.
//   - closeWait, after a close call the window took: its loop ends 200 ms
//     after the reply, then the process saves queued input sets for at most
//     viewer.SaveWait and (Unix) flushes its log for at most 2 s.
//   - stopWait, after stopping a window that took the close call but did not
//     end: its loop is stuck, so it exits within viewer.StopGrace of the
//     signal (Windows: terminated at once).
//   - stopBusyWait, after stopping a window that did not take the close call:
//     its loop may still close and save, so viewer.ExitWait.
//
// So close_viewer answers within about 23 s in every case (2 s busy + 20 s,
// or 15 s + 8 s).
const (
	closeWait    = viewer.SaveWait + 5*time.Second
	stopWait     = viewer.StopGrace + 3*time.Second
	stopBusyWait = viewer.ExitWait + 3*time.Second
)

// CloseViewer is close_viewer: close the window (a closed window is not an
// error). A window that does not answer, or does not end after the close
// call, has its process stopped (the process id the window wrote, only
// while it holds the viewer lock).
func (s *Service) CloseViewer(ctx context.Context) (Result, error) {
	front := viewerResultFront{Viewer: viewerFront{Open: false}}
	if !viewer.Running(s.paths) {
		return Result{Front: front, Body: "No viewer window was open."}, nil
	}
	var rep viewer.StateReply
	err := s.viewerCall(ctx, viewer.MethodClose, nil, &rep)
	if errors.Is(err, context.Canceled) {
		return Result{}, err
	}
	afterStop := stopBusyWait
	if err == nil {
		front.Viewer.Revision = rep.State.Revision
		if s.waitViewerGone(closeWait) {
			return Result{Front: front, Body: "Closed the viewer window. Its last camera per task is kept for the next time a task is shown."}, nil
		}
		afterStop = stopWait
	} else if !viewer.Running(s.paths) {
		return Result{Front: front, Body: "No viewer window was open."}, nil
	}
	pid, serr := viewer.Stop(s.paths)
	if errors.Is(serr, viewer.ErrNotRunning) {
		return Result{Front: front, Body: "Closed the viewer window."}, nil
	}
	if serr != nil {
		return Result{}, fail(render.CodeUnavailable, "The viewer window does not answer and could not be stopped: "+serr.Error()+".",
			"Close the window on the screen; "+s.paths.ViewerLog+" has the window's log.")
	}
	if !s.waitViewerGone(afterStop) {
		return Result{}, fail(render.CodeUnavailable, fmt.Sprintf("The viewer window (process %d) does not answer and is still running after it was told to stop.", pid),
			"Close the window on the screen; "+s.paths.ViewerLog+" has the window's log.")
	}
	return Result{Front: front, Body: fmt.Sprintf("The viewer window did not answer, so its process (%d) was stopped; its last cameras of this session may not be kept.", pid)}, nil
}

// waitViewerGone waits up to d for the viewer lock to be free.
func (s *Service) waitViewerGone(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for viewer.Running(s.paths) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}
