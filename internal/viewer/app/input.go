package app

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// frameInput is one tick of mouse and keys, real or injected (tests).
type frameInput struct {
	x, y  int
	down  [3]bool // left, right, middle
	wheel float64
	keys  []ebiten.Key // pressed this tick
	shift bool
}

// inputState follows a press from button down to button up.
type inputState struct {
	x, y         int
	down         [3]bool
	button       int    // the button of the press
	pressed      string // what the press started on: a widget id, pane:N, tex, card:N
	pressX       int
	pressY       int
	lastX, lastY int
	moved        bool
	dragging     bool // a camera drag
	pan          bool
	lastClick    time.Time
	lastClickX   int
	lastClickY   int
	wheelPending bool
	wheelAt      time.Time
}

func (a *App) readInput() frameInput {
	if f, ok := a.injected(); ok {
		return f
	}
	var f frameInput
	f.x, f.y = ebiten.CursorPosition()
	f.down = [3]bool{ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft), ebiten.IsMouseButtonPressed(ebiten.MouseButtonRight), ebiten.IsMouseButtonPressed(ebiten.MouseButtonMiddle)}
	_, f.wheel = ebiten.Wheel()
	f.keys = inpututil.AppendJustPressedKeys(nil)
	f.shift = ebiten.IsKeyPressed(ebiten.KeyShift)
	return f
}

// handleInput turns this tick's input into view changes and user actions.
func (a *App) handleInput(now time.Time) {
	a.ui.layout(a)
	f := a.readInput()
	prev := a.in.down
	if f.x != a.in.x || f.y != a.in.y {
		if h := a.ui.hitWidget(f.x, f.y); h != a.ui.hover {
			a.ui.hover = h
			a.dirty()
		}
	}
	a.in.x, a.in.y = f.x, f.y
	a.in.down = f.down
	for b := 0; b < 3; b++ {
		if f.down[b] && !prev[b] && a.in.pressed == "" {
			a.onPress(f, b, now)
		}
	}
	if a.in.pressed != "" && f.down[a.in.button] {
		a.onMove(f, now)
	}
	if a.in.pressed != "" && !f.down[a.in.button] && prev[a.in.button] {
		a.onRelease(f, now)
	}
	if f.wheel != 0 {
		a.onWheel(f, now)
	}
	for _, k := range f.keys {
		a.onKey(k, now)
	}
}

func (a *App) onPress(f frameInput, button int, now time.Time) {
	target := a.ui.hitWidget(f.x, f.y)
	if a.popup >= 0 && !strings.HasPrefix(target, "tag:") {
		a.popup = -1
		a.dirty()
		return
	}
	if target == "" {
		target = a.ui.hitRegion(f.x, f.y)
	}
	a.in.pressed, a.in.button = target, button
	a.in.pressX, a.in.pressY, a.in.lastX, a.in.lastY = f.x, f.y, f.x, f.y
	a.in.moved, a.in.dragging = false, false
	a.in.pan = button == 1 || button == 2 || f.shift
}

func (a *App) onMove(f frameInput, now time.Time) {
	dx, dy := f.x-a.in.lastX, f.y-a.in.lastY
	a.in.lastX, a.in.lastY = f.x, f.y
	if !a.in.moved && (abs(f.x-a.in.pressX) > 4 || abs(f.y-a.in.pressY) > 4) {
		a.in.moved = true
	}
	if !a.in.moved || (dx == 0 && dy == 0) {
		return
	}
	switch {
	case strings.HasPrefix(a.in.pressed, "pane:"):
		r := a.ui.paneRect(paneIndex(a.in.pressed))
		h := float64(max(r.Dy(), 1))
		a.in.dragging = true
		if a.in.pan {
			k := 2.071 / a.cam.Zoom / h // view height at the look-at point, in framing sizes, per pixel
			a.cam.Pan[0] = clamp(a.cam.Pan[0]-float64(dx)*k, -10, 10)
			a.cam.Pan[1] = clamp(a.cam.Pan[1]+float64(dy)*k, -10, 10)
		} else {
			a.cam.Az = wrapAz(a.cam.Az - 360*float64(dx)/h)
			a.cam.El = clamp(a.cam.El+360*float64(dy)/h, -89.9, 89.9)
		}
		a.viewName = "custom"
		a.lastChange = now
		a.dirty()
	case a.in.pressed == "tex":
		a.texPan[0] += float64(dx)
		a.texPan[1] += float64(dy)
		a.dirty()
	case strings.HasPrefix(a.in.pressed, "card:"):
		a.ui.dropTarget = a.ui.cardAt(f.x, f.y)
		a.dirty()
	}
}

func (a *App) onRelease(f frameInput, now time.Time) {
	p := a.in.pressed
	a.in.pressed = ""
	defer func() { a.in.dragging = false; a.ui.dropTarget = -1; a.dirty() }()
	switch {
	case strings.HasPrefix(p, "pane:"):
		i := paneIndex(p)
		if a.in.moved {
			what := "orbit"
			if a.in.pan {
				what = "pan"
			}
			a.lastChange = now
			a.userAction("camera", what+": "+a.cam.String())
			return
		}
		if now.Sub(a.in.lastClick) < 400*time.Millisecond && abs(f.x-a.in.lastClickX) < 6 && abs(f.y-a.in.lastClickY) < 6 {
			a.cam.Zoom, a.cam.Pan = 1, [2]float64{}
			a.lastChange = now
			a.userAction("frame", a.cam.String())
			a.in.lastClick = time.Time{}
			return
		}
		a.in.lastClick, a.in.lastClickX, a.in.lastClickY = now, f.x, f.y
		if a.layout == viewer.LayoutCompare && a.selected.Pane != i {
			a.selected.Pane = i
			a.userAction("select", fmt.Sprintf("pane %d", i))
		}
	case p == "tex":
		if a.in.moved {
			a.userAction("texture_pan", fmt.Sprintf("%.0f %.0f px", a.texPan[0], a.texPan[1]))
		}
	case strings.HasPrefix(p, "card:"):
		from := cardIndex(p)
		to := a.ui.cardAt(f.x, f.y)
		if a.in.moved {
			if to >= 0 && to != from && a.inputs != nil && a.inputs.editable {
				a.userReorder(from, to)
			}
			return
		}
		a.userSelectImage(from)
	default:
		if !a.in.moved && p != "" && a.ui.hitWidget(f.x, f.y) == p {
			a.ui.click(p)
		}
	}
}

func (a *App) onWheel(f frameInput, now time.Time) {
	switch r := a.ui.hitRegion(f.x, f.y); {
	case strings.HasPrefix(r, "pane:"):
		a.cam.Zoom = clamp(a.cam.Zoom*math.Pow(1.12, f.wheel), 0.1, 10)
		a.viewName = "custom"
		a.lastChange = now
		a.in.wheelPending, a.in.wheelAt = true, now
		a.dirty()
	case r == "tex":
		old := a.texZoom
		a.texZoom = clamp(a.texZoom*math.Pow(1.15, f.wheel), 0.1, 40)
		// Keep the point under the cursor in place.
		vr := a.ui.texView
		cx, cy := float64(f.x-vr.Min.X-vr.Dx()/2)-a.texPan[0], float64(f.y-vr.Min.Y-vr.Dy()/2)-a.texPan[1]
		k := a.texZoom / old
		a.texPan[0] -= cx * (k - 1)
		a.texPan[1] -= cy * (k - 1)
		a.dirty()
	}
}

// flushPendingCamera records a wheel zoom as one action once the wheel
// rests.
func (a *App) flushPendingCamera(now time.Time) {
	if a.in.wheelPending && now.Sub(a.in.wheelAt) > 400*time.Millisecond {
		a.in.wheelPending = false
		a.userAction("camera", "zoom: "+a.cam.String())
	}
}

func (a *App) onKey(k ebiten.Key, now time.Time) {
	has3D := len(a.visiblePanes()) > 0
	switch k {
	case ebiten.KeyEscape:
		switch {
		case a.popup >= 0:
			a.popup = -1
			a.dirty()
		case a.flat >= 0:
			a.userSelectImage(-1)
		}
	case ebiten.KeyR:
		if has3D {
			a.userReset()
		}
	case ebiten.KeyT:
		if has3D {
			a.userTurntable()
		}
	case ebiten.KeyW:
		if has3D {
			a.userOverlay()
		}
	case ebiten.KeyF:
		if has3D {
			a.cam.Zoom, a.cam.Pan = 1, [2]float64{}
			a.lastChange = now
			a.userAction("frame", a.cam.String())
		}
	}
}

// User actions: what the window's controls do.

func (a *App) userMode(m string) {
	if a.mode == m {
		return
	}
	a.mode = m
	a.userAction("mode", m)
}

func (a *App) userStage(name string) {
	if len(a.panes) != 1 || a.panes[0].vt == nil || a.panes[0].stage.Name == name {
		return
	}
	st, err := pickStage(a.panes[0].vt, name, "")
	if err != nil {
		a.message = errorOf(err).Message
		a.dirty()
		return
	}
	a.setStage(st)
	a.userAction("stage", name)
}

func (a *App) userVersion(v string) {
	if len(a.panes) != 1 || a.panes[0].vt == nil || a.panes[0].stage.Version == v {
		return
	}
	st, err := pickStage(a.panes[0].vt, "geometry_draft", v)
	if err != nil {
		a.message = errorOf(err).Message
		a.dirty()
		return
	}
	a.setStage(st)
	a.userAction("version", v)
}

func (a *App) userView(v string) {
	c, ok := namedCam(v)
	if !ok {
		return
	}
	a.cam, a.viewName = c, v
	a.lastChange = time.Now()
	a.userAction("view", v)
}

func (a *App) userLayout(l string) {
	if a.layout == l {
		return
	}
	a.layout = l
	a.flat, a.popup = -1, -1
	a.lastChange = time.Now()
	a.userAction("layout", l)
}

func (a *App) userTurntable() {
	a.turntable = !a.turntable
	a.userAction("turntable", onOff(a.turntable))
}

func (a *App) userOverlay() {
	a.overlay = !a.overlay
	a.userAction("wireframe_overlay", onOff(a.overlay))
}

func (a *App) userBackground() {
	i := 0
	for j, b := range viewer.Backgrounds {
		if b == a.background {
			i = j
		}
	}
	a.background = viewer.Backgrounds[(i+1)%len(viewer.Backgrounds)]
	a.userAction("background", a.background)
}

func (a *App) userReset() {
	a.cam, a.viewName = defaultCam(), "three_quarter"
	a.lastChange = time.Now()
	a.userAction("reset_camera", a.cam.String())
}

func (a *App) userSelectImage(i int) {
	cur := -1
	if a.selected.ImageIndex != nil {
		cur = *a.selected.ImageIndex
	}
	if cur == i && (a.layout == viewer.LayoutInputs || a.flat == i) {
		return
	}
	a.selectImage(i)
	if i < 0 {
		a.userAction("select", "none")
		return
	}
	a.userAction("select", "image "+strconv.Itoa(i))
}

func (a *App) userSelectTexture(i int) {
	if i == a.texSel || i < 0 || i >= len(a.textures) {
		return
	}
	a.texSel, a.texZoom, a.texPan = i, 1, [2]float64{}
	a.selected.TextureMap = a.textures[i].name
	a.userAction("select", "texture "+a.textures[i].name)
}

func (a *App) userPane(i int) {
	if a.selected.Pane == i {
		return
	}
	a.selected.Pane = i
	a.userAction("select", fmt.Sprintf("pane %d", i))
}

func (a *App) userSetTag(i int, tag string) {
	a.popup = -1
	if a.inputs == nil || !a.inputs.editable || i < 0 || i >= len(a.inputs.items) {
		return
	}
	it := a.inputs.items[i]
	if it.view == tag {
		a.dirty()
		return
	}
	old := it.view
	it.view = tag
	a.saveInputs()
	a.userAction("retag", fmt.Sprintf("image %d: %s -> %s", i, old, tag))
}

func (a *App) userWeight(i int, delta float64) {
	if a.inputs == nil || !a.inputs.editable || i < 0 || i >= len(a.inputs.items) {
		return
	}
	it := a.inputs.items[i]
	w := 1 / float64(len(a.inputs.items))
	if it.weight != nil {
		w = *it.weight
	}
	w = clamp(math.Round((w+delta)*20)/20, 0, 1)
	it.weight = &w
	a.saveInputs()
	a.userAction("weight", fmt.Sprintf("image %d: %.2f", i, w))
}

func (a *App) userReorder(from, to int) {
	a.reorder(from, to)
	if a.selected.ImageIndex != nil && *a.selected.ImageIndex == from {
		a.selected.ImageIndex = &to
	}
	a.userAction("reorder", fmt.Sprintf("image %d -> %d", from, to))
}

func (a *App) userDismiss() {
	if a.message == "" {
		return
	}
	a.message = ""
	a.userAction("dismiss_message", "")
}

// loadFlat loads a strip picture at full size for the flat view.
func (a *App) loadFlat(t *thumb) {
	if t.full != nil || t.file == "" || t.fullLoading {
		return
	}
	t.fullLoading = true
	cgen := a.contentGen
	file := t.file
	go func() {
		img, _, _, err := decodeFile(file, 4096)
		a.post(func() {
			if a.contentGen != cgen {
				return
			}
			t.fullLoading = false
			if err == nil {
				t.full = ebiten.NewImageFromImage(img)
			}
			a.dirty()
		})
	}()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

func paneIndex(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "pane:"))
	return n
}

func cardIndex(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "card:"))
	return n
}

func asInputError(err error, t **render3d.InputError) bool { return errors.As(err, t) }
