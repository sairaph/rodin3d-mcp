// Package viewer is the control channel of the viewer window (plan 2.11):
// the RPC methods of the `rodin3d-mcp viewer` process and their JSON
// shapes, the view state the window reports, and the caller's side (start
// the process, dial its socket, call). The window itself is
// internal/viewer/app; this package draws nothing and links no window code.
package viewer

import (
	"errors"
	"fmt"
	"strings"
)

// RPC methods of the viewer process (daemon/socket JSON-RPC).
const (
	MethodPing    = "ping"    // -> PingReply
	MethodShow    = "show"    // ShowParams -> StateReply
	MethodControl = "control" // Control -> StateReply
	MethodState   = "state"   // StateParams -> StateReply
	MethodCapture = "capture" // CaptureParams -> CaptureReply
	MethodRaise   = "raise"   // -> StateReply: restore and bring the window to the front
	MethodClose   = "close"   // -> StateReply, then the process ends
	// MethodInject feeds mouse and key events into the window's game loop;
	// served only by builds with the viewertest tag (live tests).
	MethodInject = "inject_input" // InjectParams -> InjectReply
)

// Content kinds (show_in_viewer content).
const (
	KindTask       = "task"
	KindCompare    = "compare"
	KindInputSet   = "input_set"
	KindTaskInputs = "task_inputs"
	KindTextures   = "textures"
	KindModelFile  = "model_file"
	KindImages     = "images"
	KindNone       = "none" // an open window that shows nothing yet
)

// Layouts.
const (
	LayoutSingle   = "single"
	LayoutCompare  = "compare"
	LayoutInputs   = "inputs"
	LayoutTextures = "textures"
)

// Layouts lists every layout.
var Layouts = []string{LayoutSingle, LayoutCompare, LayoutInputs, LayoutTextures}

// Backgrounds of the 3D view: the mode's own, or a flat light or dark one.
var Backgrounds = []string{"default", "light", "dark"}

// Views are the named cameras of control_viewer view (render3d's, without
// grid).
var Views = []string{"front", "back", "left", "right", "top", "three_quarter", "web"}

// ViewTags are the image view tags of the web (F front, FL front-left, ...,
// U up, D down, ? unknown), in the web's order.
var ViewTags = []string{"F", "FL", "FR", "B", "BL", "BR", "L", "R", "U", "D", "?"}

// TextureMaps are the material maps the textures layout shows.
var TextureMaps = []string{"diffuse", "pbr", "normal", "shaded"}

// Quality of the frame on screen.
const (
	QualityFull        = "full"        // every pane shows its full frame for the current view
	QualityInteractive = "interactive" // a quick frame (drag, turntable) or content still loading
)

// TaskRef names a task and the stage of it to show.
type TaskRef struct {
	TaskID  string `json:"task_id" yaml:"task_id"`
	Stage   string `json:"stage,omitempty" yaml:"stage,omitempty"`     // current (default) | geometry_draft | geometry | material
	Version string `json:"version,omitempty" yaml:"version,omitempty"` // geometry_draft only: v1, v2, ... (default the task's)
}

// InputImage is one image of an input set.
type InputImage struct {
	Path   string   `json:"path" yaml:"path"`
	View   string   `json:"view,omitempty" yaml:"view,omitempty"`     // a ViewTags value; empty is ?
	Weight *float64 `json:"weight,omitempty" yaml:"weight,omitempty"` // 0-1; nil: the default table
}

// InputSetRef is a new input set (images) or a stored one (id).
type InputSetRef struct {
	ID     string       `json:"id,omitempty" yaml:"id,omitempty"`
	Images []InputImage `json:"images,omitempty" yaml:"images,omitempty"`
}

// TexturesRef names a task's texture maps.
type TexturesRef struct {
	TaskID string   `json:"task_id" yaml:"task_id"`
	Maps   []string `json:"maps,omitempty" yaml:"maps,omitempty"`
}

// Content is what to show: exactly one field is set.
type Content struct {
	Task       *TaskRef     `json:"task,omitempty" yaml:"task,omitempty"`
	Compare    []TaskRef    `json:"compare,omitempty" yaml:"compare,omitempty"`
	InputSet   *InputSetRef `json:"input_set,omitempty" yaml:"input_set,omitempty"`
	TaskInputs string       `json:"task_inputs,omitempty" yaml:"task_inputs,omitempty"`
	Textures   *TexturesRef `json:"textures,omitempty" yaml:"textures,omitempty"`
	ModelFile  string       `json:"model_file,omitempty" yaml:"model_file,omitempty"`
	Images     []string     `json:"images,omitempty" yaml:"images,omitempty"`
}

// Kind returns the kind of c, or an error unless exactly one field is set.
func (c Content) Kind() (string, error) {
	var kinds []string
	if c.Task != nil {
		kinds = append(kinds, KindTask)
	}
	if c.Compare != nil {
		kinds = append(kinds, KindCompare)
	}
	if c.InputSet != nil {
		kinds = append(kinds, KindInputSet)
	}
	if c.TaskInputs != "" {
		kinds = append(kinds, KindTaskInputs)
	}
	if c.Textures != nil {
		kinds = append(kinds, KindTextures)
	}
	if c.ModelFile != "" {
		kinds = append(kinds, KindModelFile)
	}
	if c.Images != nil {
		kinds = append(kinds, KindImages)
	}
	switch len(kinds) {
	case 1:
		return kinds[0], nil
	case 0:
		return "", errors.New("content names nothing to show: give one of task, compare, input_set, task_inputs, textures, model_file, images")
	}
	return "", fmt.Errorf("content gives %s at once; give exactly one", strings.Join(kinds, " and "))
}

// ShowParams is the show method's input.
type ShowParams struct {
	Content          Content `json:"content"`
	Mode             string  `json:"mode,omitempty"`
	View             string  `json:"view,omitempty"`
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
}

// Orbit sets the camera direction: azimuth round the model (0 looks at the
// front, 90 at the right side, -90 the left, 180 the back) and elevation
// above the horizon (90 looks straight down).
type Orbit struct {
	AzimuthDeg   *float64 `json:"azimuth_deg,omitempty"`
	ElevationDeg *float64 `json:"elevation_deg,omitempty"`
}

// Pan moves the look-at point from the model centre along the view's right
// (x) and up (y), in units of the model's framing size.
type Pan struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Select picks an image, a texture map or a pane.
type Select struct {
	ImageIndex *int    `json:"image_index,omitempty"`
	TextureMap *string `json:"texture_map,omitempty"`
	Pane       *int    `json:"pane,omitempty"`
}

// SetTag retags an image of the shown input set.
type SetTag struct {
	ImageIndex int      `json:"image_index"`
	View       string   `json:"view"`
	Weight     *float64 `json:"weight,omitempty"`
}

// Reorder moves an image of the shown input set.
type Reorder struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Highlight marks an image or a view tag for the user.
type Highlight struct {
	ImageIndex *int    `json:"image_index,omitempty" yaml:"image_index,omitempty"`
	View       *string `json:"view,omitempty" yaml:"view,omitempty"`
}

// Control is the control method's input: any set field is applied, in the
// order of the fields below; nothing is applied when one of them is refused.
type Control struct {
	Layout           *string    `json:"layout,omitempty"`
	Stage            *string    `json:"stage,omitempty"`
	Version          *string    `json:"version,omitempty"`
	Mode             *string    `json:"mode,omitempty"`
	View             *string    `json:"view,omitempty"`
	ResetCamera      bool       `json:"reset_camera,omitempty"`
	Orbit            *Orbit     `json:"orbit,omitempty"`
	Zoom             *float64   `json:"zoom,omitempty"`
	Pan              *Pan       `json:"pan,omitempty"`
	Turntable        *bool      `json:"turntable,omitempty"`
	WireframeOverlay *bool      `json:"wireframe_overlay,omitempty"`
	Background       *string    `json:"background,omitempty"`
	Select           *Select    `json:"select,omitempty"`
	SetTag           *SetTag    `json:"set_tag,omitempty"`
	Reorder          *Reorder   `json:"reorder,omitempty"`
	Highlight        *Highlight `json:"highlight,omitempty"`
	ClearHighlight   bool       `json:"clear_highlight,omitempty"`
	Message          *string    `json:"message,omitempty"`
	ExpectedRevision *int64     `json:"expected_revision,omitempty"`
}

// Empty reports whether c changes nothing.
func (c Control) Empty() bool {
	return c.Layout == nil && c.Stage == nil && c.Version == nil && c.Mode == nil && c.View == nil && !c.ResetCamera &&
		c.Orbit == nil && c.Zoom == nil && c.Pan == nil && c.Turntable == nil && c.WireframeOverlay == nil &&
		c.Background == nil && c.Select == nil && c.SetTag == nil && c.Reorder == nil && c.Highlight == nil &&
		!c.ClearHighlight && c.Message == nil
}

// StateParams is the state method's input.
type StateParams struct {
	SinceRevision int64 `json:"since_revision,omitempty"`
}

// CaptureParams is the capture method's input: wait up to WaitMS for a
// full-quality frame, then take the current one. With SinceRevision the
// reply also lists the user's actions after that revision, read at the
// moment of the screenshot.
type CaptureParams struct {
	WaitMS        int    `json:"wait_ms,omitempty"`
	SinceRevision *int64 `json:"since_revision,omitempty"`
}

// ContentInfo says what the window shows.
type ContentInfo struct {
	Kind       string   `json:"kind" yaml:"kind"`
	TaskID     string   `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Prompt     string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Tasks      []string `json:"tasks,omitempty" yaml:"tasks,omitempty,flow"` // compare: both task ids
	File       string   `json:"file,omitempty" yaml:"file,omitempty"`
	Files      []string `json:"files,omitempty" yaml:"files,omitempty"`
	InputSetID string   `json:"input_set_id,omitempty" yaml:"input_set_id,omitempty"`
}

// CameraState is the camera of the 3D panes (linked in compare).
type CameraState struct {
	AzimuthDeg   float64    `json:"azimuth_deg" yaml:"azimuth_deg"`
	ElevationDeg float64    `json:"elevation_deg" yaml:"elevation_deg"`
	Zoom         float64    `json:"zoom" yaml:"zoom"`
	Pan          [2]float64 `json:"pan" yaml:"pan,flow"`
	Distance     float64    `json:"distance" yaml:"distance"`   // from the look-at point, model units
	Target       [3]float64 `json:"target" yaml:"target,flow"`  // look-at point, model units
	FOV          float64    `json:"fov_deg" yaml:"fov_deg"`     // vertical
	Turntable    bool       `json:"turntable" yaml:"turntable"` // turning by itself
}

// PaneState is one 3D pane.
type PaneState struct {
	Index     int      `json:"index" yaml:"index"`
	Kind      string   `json:"kind" yaml:"kind"` // task | model_file
	TaskID    string   `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Prompt    string   `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	File      string   `json:"file,omitempty" yaml:"file,omitempty"`
	Stage     string   `json:"stage,omitempty" yaml:"stage,omitempty"`
	Version   string   `json:"version,omitempty" yaml:"version,omitempty"`
	Modes     []string `json:"available_modes" yaml:"available_modes,flow"`
	Status    string   `json:"status" yaml:"status"` // loading | ready | error
	Error     string   `json:"error,omitempty" yaml:"error,omitempty"`
	Note      string   `json:"note,omitempty" yaml:"note,omitempty"` // a change the load made to what was asked (a mode the model lacks)
	Triangles int      `json:"triangles,omitempty" yaml:"triangles,omitempty"`
	// TurntableFrame is what a pane of a Gen-1.5 geometry draft shows: the
	// server's turntable frame nearest the camera's azimuth.
	TurntableFrame *TurntableFrame `json:"turntable_frame,omitempty" yaml:"turntable_frame,omitempty"`
}

// TurntableFrame is the server frame a turntable pane draws: frame 0-15 of
// the 16 round the model, and the azimuth it was taken from (0 front, 90
// right). The frames are pictures: the camera's elevation and pan, the
// wireframe overlay and the background are not drawn on them.
type TurntableFrame struct {
	Frame      int     `json:"frame" yaml:"frame"`
	AzimuthDeg float64 `json:"azimuth_deg" yaml:"azimuth_deg"`
}

// SelectedState is the current selection.
type SelectedState struct {
	ImageIndex *int   `json:"image_index,omitempty" yaml:"image_index,omitempty"`
	TextureMap string `json:"texture_map,omitempty" yaml:"texture_map,omitempty"`
	Pane       int    `json:"pane" yaml:"pane"`
}

// InputSetImage is one image of the shown input set.
type InputSetImage struct {
	Index  int      `json:"index" yaml:"index"`
	File   string   `json:"file" yaml:"file"`
	View   string   `json:"view" yaml:"view"`
	Weight *float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
}

// InputSetState is the shown input set (or a task's inputs, read-only).
type InputSetState struct {
	ID       string          `json:"id,omitempty" yaml:"id,omitempty"`
	Editable bool            `json:"editable" yaml:"editable"`
	FromTask string          `json:"from_task,omitempty" yaml:"from_task,omitempty"`
	Images   []InputSetImage `json:"images" yaml:"images"`
}

// StripImage is one picture of the image strip (server images of the
// shown stage and the task's input images).
type StripImage struct {
	Index int    `json:"index" yaml:"index"`
	Kind  string `json:"kind" yaml:"kind"` // poster | geometry_image | material_image | input
	Name  string `json:"name" yaml:"name"`
	View  string `json:"view,omitempty" yaml:"view,omitempty"` // inputs: the view tag
}

// WindowInfo is the window's size in pixels (the screenshot's size) and the
// screen's scale (pixels per point).
type WindowInfo struct {
	W     int     `json:"w" yaml:"w"`
	H     int     `json:"h" yaml:"h"`
	Scale float64 `json:"scale" yaml:"scale"`
}

// State is everything the window shows, as the user and the agent share it.
// Every change bumps Revision.
type State struct {
	Revision   int64          `json:"revision" yaml:"revision"`
	Content    ContentInfo    `json:"content" yaml:"content"`
	Layout     string         `json:"layout" yaml:"layout"`
	Layouts    []string       `json:"layouts" yaml:"layouts,flow"` // the layouts the content offers
	Stage      string         `json:"stage,omitempty" yaml:"stage,omitempty"`
	Stages     []string       `json:"stages,omitempty" yaml:"stages,omitempty,flow"`
	Version    string         `json:"version,omitempty" yaml:"version,omitempty"`
	Versions   []string       `json:"versions,omitempty" yaml:"versions,omitempty,flow"`
	Mode       string         `json:"mode,omitempty" yaml:"mode,omitempty"`
	Modes      []string       `json:"available_modes,omitempty" yaml:"available_modes,omitempty,flow"`
	View       string         `json:"view,omitempty" yaml:"view,omitempty"` // a named view, or custom after an orbit
	Camera     CameraState    `json:"camera" yaml:"camera"`
	Overlay    bool           `json:"wireframe_overlay" yaml:"wireframe_overlay"`
	Background string         `json:"background" yaml:"background"`
	Panes      []PaneState    `json:"panes" yaml:"panes"`
	Selected   SelectedState  `json:"selected" yaml:"selected"`
	InputSet   *InputSetState `json:"input_set,omitempty" yaml:"input_set,omitempty"`
	Strip      []StripImage   `json:"strip,omitempty" yaml:"strip,omitempty"`
	Textures   []string       `json:"textures,omitempty" yaml:"textures,omitempty,flow"`
	Highlight  *Highlight     `json:"highlight,omitempty" yaml:"highlight,omitempty"`
	Message    string         `json:"message,omitempty" yaml:"message,omitempty"`
	Quality    string         `json:"quality" yaml:"quality"`
	Window     WindowInfo     `json:"window" yaml:"window"`
}

// UserAction is one change the user made in the window.
type UserAction struct {
	Revision int64  `json:"revision" yaml:"revision"`
	At       string `json:"at" yaml:"at"`
	Action   string `json:"action" yaml:"action"`
	Detail   string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// Error is a refused call, in the shape of the tools' errors.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// StateReply answers show, control, state, raise and close: the state after
// the call, the user's actions since the asked revision (state only) and the
// window's process id; Error when the call was refused (State is then the
// unchanged state).
type StateReply struct {
	State   State        `json:"state"`
	Actions []UserAction `json:"user_actions,omitempty"`
	// ActionsTruncated: the window keeps the last 50 actions, and older
	// ones after the asked revision were dropped.
	ActionsTruncated bool   `json:"actions_truncated,omitempty"`
	PID              int    `json:"pid"`
	Error            *Error `json:"error,omitempty"`
}

// CaptureReply is a screenshot: the composed window image as PNG, with the
// state (and, when asked, the user's actions) of the same moment; Error
// when no screenshot was taken (the window closed meanwhile).
type CaptureReply struct {
	PNG              []byte       `json:"png"`
	Width            int          `json:"width"`
	Height           int          `json:"height"`
	Quality          string       `json:"quality"`
	Waited           int          `json:"waited_ms"`
	State            State        `json:"state"`
	Actions          []UserAction `json:"user_actions,omitempty"`
	ActionsTruncated bool         `json:"actions_truncated,omitempty"`
	PID              int          `json:"pid"`
	Error            *Error       `json:"error,omitempty"`
}

// PingReply identifies the window process.
type PingReply struct {
	PID      int    `json:"pid"`
	Version  string `json:"version"`
	Revision int64  `json:"revision"`
}

// InjectEvent is one input event for the test-only inject_input method.
// Type move, down, up, click (down then up), drag (down, moves, up over
// Frames ticks), wheel, key or wait (Frames ticks of no input). X, Y are
// window pixels; Target, when set, names a widget whose centre is used
// (inject_input's reply lists them). Button left (default), right or middle.
type InjectEvent struct {
	Type   string  `json:"type"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	ToX    float64 `json:"to_x,omitempty"`
	ToY    float64 `json:"to_y,omitempty"`
	Target string  `json:"target,omitempty"`
	To     string  `json:"to,omitempty"` // drag end widget
	Button string  `json:"button,omitempty"`
	DY     float64 `json:"dy,omitempty"`
	Key    string  `json:"key,omitempty"` // escape, shift (held through the next event), ...
	Frames int     `json:"frames,omitempty"`
}

// InjectParams is the inject_input method's input.
type InjectParams struct {
	Events []InjectEvent `json:"events"`
}

// InjectReply lists the widgets on screen (id -> x, y, w, h) after the
// events ran, and the state.
type InjectReply struct {
	Widgets map[string][4]int `json:"widgets"`
	State   State             `json:"state"`
	Error   *Error            `json:"error,omitempty"`
}
