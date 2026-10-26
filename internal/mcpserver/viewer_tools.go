package mcpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// The viewer tools (plan 2.6.4). They change only the local window, never
// the account.
var (
	viewerTool     = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(true)}
	viewerReadTool = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	viewerClose    = &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
)

type taskRefArg struct {
	TaskID  string  `json:"task_id" jsonschema:"the task id (an id prefix of 8 or more characters works too)"`
	Stage   *string `json:"stage,omitempty" jsonschema:"which stage: current (the task's own, default), geometry_draft, geometry (confirmed) or material"`
	Version *string `json:"version,omitempty" jsonschema:"geometry_draft only: a geometry version from get_task versions (v1, v2, ...); default the task's"`
}

type inputImageArg struct {
	Path   string   `json:"path" jsonschema:"absolute path of a png, jpeg or webp image"`
	View   *string  `json:"view,omitempty" jsonschema:"the side the image shows the object from (default ?, unknown)"`
	Weight *float64 `json:"weight,omitempty" jsonschema:"relative weight above 0, at most 1 (normalised to sum 1); give one on every image or on none (none: the web's table)"`
}

type inputSetArg struct {
	ID     *string         `json:"id,omitempty" jsonschema:"a stored input set (from an earlier show_in_viewer); or give images"`
	Images []inputImageArg `json:"images,omitempty" jsonschema:"1-5 images for a new input set"`
}

type texturesArg struct {
	TaskID string   `json:"task_id" jsonschema:"a task at the material stage (an id prefix of 8 or more characters works too)"`
	Maps   []string `json:"maps,omitempty" jsonschema:"the maps to show (default every map the task has)"`
}

type contentArg struct {
	Task       *taskRefArg  `json:"task,omitempty" jsonschema:"a task stage in 3D; its server images and input images (with their view tags) show in the strip below"`
	Compare    []taskRefArg `json:"compare,omitempty" jsonschema:"two task stages side by side with linked cameras"`
	InputSet   *inputSetArg `json:"input_set,omitempty" jsonschema:"input images with view tags and weights, arranged around a top-down compass; the user can retag, reorder and reweight them; saved as an input set for generate_model input_set"`
	TaskInputs *string      `json:"task_inputs,omitempty" jsonschema:"a task id: an editable copy of its input images with their stored view tags (a new input set)"`
	Textures   *texturesArg `json:"textures,omitempty" jsonschema:"a task's texture maps, flat, with zoom and pan"`
	ModelFile  *string      `json:"model_file,omitempty" jsonschema:"absolute path of a local glb, gltf, obj or stl file, in 3D"`
	Images     []string     `json:"images,omitempty" jsonschema:"absolute paths of png, jpeg or webp images to show flat"`
}

func (c *contentArg) content() viewer.Content {
	var out viewer.Content
	ref := func(r taskRefArg) viewer.TaskRef {
		return viewer.TaskRef{TaskID: r.TaskID, Stage: optString(r.Stage), Version: optString(r.Version)}
	}
	if c.Task != nil {
		r := ref(*c.Task)
		out.Task = &r
	}
	for _, r := range c.Compare {
		out.Compare = append(out.Compare, ref(r))
	}
	if c.InputSet != nil {
		set := &viewer.InputSetRef{ID: optString(c.InputSet.ID)}
		for _, im := range c.InputSet.Images {
			set.Images = append(set.Images, viewer.InputImage{Path: im.Path, View: optString(im.View), Weight: im.Weight})
		}
		out.InputSet = set
	}
	out.TaskInputs = optString(c.TaskInputs)
	if c.Textures != nil {
		out.Textures = &viewer.TexturesRef{TaskID: c.Textures.TaskID, Maps: c.Textures.Maps}
	}
	out.ModelFile = optString(c.ModelFile)
	out.Images = c.Images
	return out
}

type openViewerArgs struct {
	Show *contentArg `json:"show,omitempty" jsonschema:"what to show at once (the same object as show_in_viewer content); default nothing, or what the open window shows"`
	Mode *string     `json:"mode,omitempty" jsonschema:"render mode for the 3D view (the modes of get_preview's renders, as the stage offers them)"`
	View *string     `json:"view,omitempty" jsonschema:"named camera: front, back, left, right, top, three_quarter or web"`
}

type showArgs struct {
	Content          contentArg `json:"content" jsonschema:"what to show: exactly one of task, compare, input_set, task_inputs, textures, model_file, images"`
	Mode             *string    `json:"mode,omitempty" jsonschema:"render mode for the 3D view; default the current one when the content has it, else the stage's first web mode"`
	View             *string    `json:"view,omitempty" jsonschema:"named camera; default the task's last camera in this viewer, else three_quarter"`
	ExpectedRevision *int64     `json:"expected_revision,omitempty" jsonschema:"apply only if the viewer is still at this revision (the user may have changed it); a stale revision answers conflict with the current state"`
}

type orbitArg struct {
	AzimuthDeg   *float64 `json:"azimuth_deg,omitempty" jsonschema:"degrees round the model: 0 looks at the front, 90 the right side, -90 the left, 180 the back"`
	ElevationDeg *float64 `json:"elevation_deg,omitempty" jsonschema:"degrees above the horizon, -90 to 90 (90 looks straight down)"`
}

type panArg struct {
	X float64 `json:"x" jsonschema:"move the look-at point right (model sizes)"`
	Y float64 `json:"y" jsonschema:"move the look-at point up (model sizes)"`
}

type selectArg struct {
	ImageIndex *int    `json:"image_index,omitempty" jsonschema:"an image: in the inputs layout an input_set image, elsewhere a strip image (shown full size; -1 goes back to 3D)"`
	TextureMap *string `json:"texture_map,omitempty" jsonschema:"a texture of the textures layout (state textures)"`
	Pane       *int    `json:"pane,omitempty" jsonschema:"the pane of a comparison (0 or 1) the single layout shows"`
}

type setTagArg struct {
	ImageIndex int      `json:"image_index" jsonschema:"the input_set image to retag"`
	View       string   `json:"view" jsonschema:"its new view tag"`
	Weight     *float64 `json:"weight,omitempty" jsonschema:"its new weight above 0, at most 1; images of the set without a weight get the even share 1/N"`
}

type reorderArg struct {
	From int `json:"from" jsonschema:"index of the image to move"`
	To   int `json:"to" jsonschema:"its new index"`
}

type highlightArg struct {
	ImageIndex *int    `json:"image_index,omitempty" jsonschema:"an image to mark for the user (strip or input_set index)"`
	View       *string `json:"view,omitempty" jsonschema:"a view tag to mark: its images and its side of the compass"`
}

type controlArgs struct {
	Layout           *string       `json:"layout,omitempty" jsonschema:"single (3D), compare, inputs or textures, as the content offers them (state layouts)"`
	Stage            *string       `json:"stage,omitempty" jsonschema:"a stage of the shown task with a mesh (state stages)"`
	Version          *string       `json:"version,omitempty" jsonschema:"a geometry draft version (state versions)"`
	Mode             *string       `json:"mode,omitempty" jsonschema:"render mode (state available_modes)"`
	View             *string       `json:"view,omitempty" jsonschema:"named camera"`
	ResetCamera      *bool         `json:"reset_camera,omitempty" jsonschema:"back to the three_quarter view at zoom 1"`
	Orbit            *orbitArg     `json:"orbit,omitempty" jsonschema:"set the camera direction"`
	Zoom             *float64      `json:"zoom,omitempty" jsonschema:"0.1-10; 1 fits the model, 2 is twice as close"`
	Pan              *panArg       `json:"pan,omitempty" jsonschema:"move the look-at point from the model centre"`
	Turntable        *bool         `json:"turntable,omitempty" jsonschema:"turn the model slowly (frames stay quick while it turns)"`
	WireframeOverlay *bool         `json:"wireframe_overlay,omitempty" jsonschema:"draw the triangle edges over any mode"`
	Background       *string       `json:"background,omitempty" jsonschema:"the mode's own background, or a flat light or dark one"`
	Select           *selectArg    `json:"select,omitempty" jsonschema:"pick an image, a texture or a pane"`
	SetTag           *setTagArg    `json:"set_tag,omitempty" jsonschema:"retag an image of the shown input set"`
	Reorder          *reorderArg   `json:"reorder,omitempty" jsonschema:"move an image of the shown input set"`
	Highlight        *highlightArg `json:"highlight,omitempty" jsonschema:"mark an image or a view tag for the user (it pulses for 4 s, then stays outlined)"`
	ClearHighlight   *bool         `json:"clear_highlight,omitempty" jsonschema:"remove the highlight"`
	Message          *string       `json:"message,omitempty" jsonschema:"short text for the banner above the view; empty removes it"`
	ExpectedRevision *int64        `json:"expected_revision,omitempty" jsonschema:"apply only if the viewer is still at this revision; a stale revision answers conflict with the current state"`
}

type viewerStateArgs struct {
	Screenshot    *bool  `json:"screenshot,omitempty" jsonschema:"attach a screenshot of the window as the user sees it (default true)"`
	Size          *int   `json:"size,omitempty" jsonschema:"longer edge of the screenshot, 256-1600 (default the window's size, shrunk only to fit the reply)"`
	SinceRevision *int64 `json:"since_revision,omitempty" jsonschema:"list the user's changes after this revision (default 0: all kept, up to 50)"`
}

// contentSchema adds the enums of a content object.
func contentSchema(c *jsonschema.Schema) {
	nonNullDeep(c)
	for _, ref := range []*jsonschema.Schema{property(c, "task"), property(c, "compare").Items} {
		nonNullDeep(ref)
		withEnum(ref, "stage", "current", "geometry_draft", "geometry", "material")
	}
	cmp := property(c, "compare")
	cmp.MinItems, cmp.MaxItems = jsonschema.Ptr(2), jsonschema.Ptr(2)
	set := property(c, "input_set")
	nonNullDeep(set)
	imgs := property(set, "images")
	imgs.MinItems, imgs.MaxItems = jsonschema.Ptr(1), jsonschema.Ptr(service.MaxImages)
	nonNullDeep(imgs.Items)
	withEnum(imgs.Items, "view", viewer.ViewTags...)
	withOpenMinRange(imgs.Items, 0, 1, "weight")
	tex := property(c, "textures")
	nonNullDeep(tex)
	withEnum(tex, "maps", viewer.TextureMaps...)
	property(c, "images").MinItems = jsonschema.Ptr(1)
}

func (s *Server) registerViewerTools() {
	modes := service.RenderModes()

	open := nonNullDeep(inputSchema[openViewerArgs](nil))
	contentSchema(property(open, "show"))
	withEnum(open, "mode", modes...)
	withEnum(open, "view", viewer.Views...)
	addTool(s, "open_viewer", "Open the viewer window",
		"Open the 3D viewer window on this computer for the user (a native window of this program, not a browser), or bring the open "+
			"one to the front; show puts content in it at once. Open it only when the user asks to see something. The user can orbit, zoom, "+
			"switch modes and stages and retag input images in it at any time, so read get_viewer_state before relying on what is shown. "+
			"Returns the viewer's state and a screenshot after the first full-quality frame (up to 10 s; then the current frame). "+
			"Answers unavailable on a computer without a display.",
		viewerTool, open, s.openViewer)

	show := nonNullDeep(inputSchema[showArgs](nil))
	contentSchema(property(show, "content"))
	withEnum(show, "mode", modes...)
	withEnum(show, "view", viewer.Views...)
	addTool(s, "show_in_viewer", "Show in the viewer",
		"Show content in the open viewer window: a task stage in 3D (every render mode of get_preview, stage tabs, versions, the stage's "+
			"server images and the input images with their view tags in a strip), two task stages compared side by side, input images "+
			"around a compass of their view tags (an input set the user can retag; generate_model takes its id), a task's texture maps, a "+
			"local model file, or images. Returns what is shown with its available modes, and a screenshot. Answers unavailable when no "+
			"window is open (call open_viewer), conflict when the stage or mode does not exist or expected_revision is stale.",
		viewerTool, show, s.showInViewer)

	ctl := nonNullDeep(inputSchema[controlArgs](nil))
	withEnum(ctl, "layout", viewer.Layouts...)
	withEnum(ctl, "stage", "geometry_draft", "geometry", "material")
	withEnum(ctl, "mode", modes...)
	withEnum(ctl, "view", viewer.Views...)
	withEnum(ctl, "background", viewer.Backgrounds...)
	withRange(ctl, 0.1, 10, "zoom")
	withRange(property(ctl, "orbit"), -90, 90, "elevation_deg")
	withEnum(property(ctl, "set_tag"), "view", viewer.ViewTags...)
	withOpenMinRange(property(ctl, "set_tag"), 0, 1, "weight")
	withEnum(property(ctl, "highlight"), "view", viewer.ViewTags...)
	addTool(s, "control_viewer", "Change the viewer",
		"Change what the open viewer shows: layout, stage, version, render mode, named view, orbit, zoom, pan, turntable, wireframe "+
			"overlay, background, selection, the view tags, weights and order of a shown input set, a highlight, and a message banner for "+
			"the user. Every field is checked before any is applied. Returns the new state (no screenshot: get_viewer_state takes one). "+
			"Use expected_revision from the last state so a change the user just made is not overwritten (conflict otherwise).",
		viewerTool, ctl, s.controlViewer)

	st := nonNullDeep(inputSchema[viewerStateArgs](map[string]string{"screenshot": "true"}))
	withRange(st, service.ViewerShotMin, service.ViewerShotMax, "size")
	withMin(st, 0, "since_revision")
	addTool(s, "get_viewer_state", "Viewer state and screenshot",
		"Read the open viewer: what it shows (content, stage, mode, view, camera, layout, panes, selection, input set with tags and "+
			"weights), the revision, the changes the user made in the window since since_revision, and a screenshot of the window exactly "+
			"as the user sees it (quality full, or interactive when nothing full-quality came within 10 s).",
		viewerReadTool, st, s.viewerState)

	addTool(s, "close_viewer", "Close the viewer window",
		"Close the viewer window (when the user is done with it). Its last camera per task is kept. Closing a closed viewer is not an error.",
		viewerClose, inputSchema[noInput](nil), s.closeViewer)
}

func (s *Server) openViewer(ctx context.Context, _ *mcp.CallToolRequest, in openViewerArgs) (*mcp.CallToolResult, any, error) {
	o := service.ViewerOpenInput{Mode: optString(in.Mode), View: optString(in.View)}
	if in.Show != nil {
		c := in.Show.content()
		o.Content = &c
	}
	return imageReply(s.svc.OpenViewer(ctx, o)), nil, nil
}

func (s *Server) showInViewer(ctx context.Context, _ *mcp.CallToolRequest, in showArgs) (*mcp.CallToolResult, any, error) {
	return imageReply(s.svc.ShowInViewer(ctx, viewer.ShowParams{Content: in.Content.content(), Mode: optString(in.Mode), View: optString(in.View), ExpectedRevision: in.ExpectedRevision})), nil, nil
}

func (s *Server) controlViewer(ctx context.Context, _ *mcp.CallToolRequest, in controlArgs) (*mcp.CallToolResult, any, error) {
	c := viewer.Control{
		Layout: in.Layout, Stage: in.Stage, Version: in.Version, Mode: in.Mode, View: in.View, ResetCamera: optBool(in.ResetCamera, false),
		Zoom: in.Zoom, Turntable: in.Turntable, WireframeOverlay: in.WireframeOverlay, Background: in.Background,
		ClearHighlight: optBool(in.ClearHighlight, false), Message: in.Message, ExpectedRevision: in.ExpectedRevision,
	}
	if in.Orbit != nil {
		c.Orbit = &viewer.Orbit{AzimuthDeg: in.Orbit.AzimuthDeg, ElevationDeg: in.Orbit.ElevationDeg}
	}
	if in.Pan != nil {
		c.Pan = &viewer.Pan{X: in.Pan.X, Y: in.Pan.Y}
	}
	if in.Select != nil {
		c.Select = &viewer.Select{ImageIndex: in.Select.ImageIndex, TextureMap: in.Select.TextureMap, Pane: in.Select.Pane}
	}
	if in.SetTag != nil {
		c.SetTag = &viewer.SetTag{ImageIndex: in.SetTag.ImageIndex, View: in.SetTag.View, Weight: in.SetTag.Weight}
	}
	if in.Reorder != nil {
		c.Reorder = &viewer.Reorder{From: in.Reorder.From, To: in.Reorder.To}
	}
	if in.Highlight != nil {
		c.Highlight = &viewer.Highlight{ImageIndex: in.Highlight.ImageIndex, View: in.Highlight.View}
	}
	return reply(s.svc.ControlViewer(ctx, c)), nil, nil
}

func (s *Server) viewerState(ctx context.Context, _ *mcp.CallToolRequest, in viewerStateArgs) (*mcp.CallToolResult, any, error) {
	var since int64
	if in.SinceRevision != nil {
		since = *in.SinceRevision
	}
	return imageReply(s.svc.ViewerState(ctx, service.ViewerStateInput{Screenshot: optBool(in.Screenshot, true), Size: optInt(in.Size, 0), SinceRevision: since})), nil, nil
}

func (s *Server) closeViewer(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	return reply(s.svc.CloseViewer(ctx)), nil, nil
}
