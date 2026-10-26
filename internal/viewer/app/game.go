package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// App is the window and the state it shows. Every field is owned by the
// game loop (Update and Draw); other goroutines reach it through cmds and
// loaded only.
type App struct {
	ctx     context.Context
	svc     *service.Service
	version string
	cmds    chan func()
	loaded  chan func()
	rend    *renderer
	saver   *setSaver
	// pendingSave is the input set write queued by the call running on the
	// loop now (doSaving waits for it).
	pendingSave <-chan error

	// The shared state (viewer.State is built from it by snapshot).
	rev        int64
	actions    []viewer.UserAction
	droppedRev int64 // revision of the newest user action no longer kept
	kind       string
	info       viewer.ContentInfo
	panes      []*pane
	layout     string
	mode       string
	cam        camState
	viewName   string
	turntable  bool
	overlay    bool
	background string
	message    string
	selected   viewer.SelectedState
	highlight  *viewer.Highlight
	strip      []*thumb
	inputs     *inputModel
	textures   []*thumb

	// Window-side view state.
	contentGen  uint64
	flat        int // strip picture shown flat in the single layout; -1 none
	texSel      int
	texZoom     float64
	texPan      [2]float64
	popup       int // input card whose tag picker is open; -1 none
	highlightAt time.Time
	cameras     map[string]camState
	lastChange  time.Time
	captures    []*captureReq
	closing     bool
	closeAt     time.Time
	raiseStep   int

	// Screen.
	sw, sh       int
	scale        float64
	off          *ebiten.Image
	gen, drawn   uint64 // composition generation wanted / on screen
	drawnW       int
	drawnH       int
	ui           *uiState
	in           inputState
	lastTick     time.Time
	loadingPulse time.Time
	test         testHooks // input injection (viewertest builds)
}

// captureReq is a screenshot waiting for a full-quality frame; since, when
// set, asks for the user's actions after that revision as well.
type captureReq struct {
	start, deadline time.Time
	since           *int64
	done            chan viewer.CaptureReply
}

func newApp(ctx context.Context, svc *service.Service, version string) *App {
	a := &App{ctx: ctx, svc: svc, version: version, cmds: make(chan func(), 16), loaded: make(chan func(), 256), rend: newRenderer(),
		kind: viewer.KindNone, info: viewer.ContentInfo{Kind: viewer.KindNone}, background: "default", flat: -1, popup: -1, texZoom: 1,
		cam: defaultCam(), viewName: "three_quarter", scale: 1, lastTick: time.Now()}
	a.loadCameras()
	a.ui = newUI()
	a.saver = newSetSaver(svc.SaveInputSet, func(id string, err error) {
		log.Printf("save input set %s: %v", id, err)
		a.post(func() {
			a.message = "Could not save input set " + id + ": " + service.Classify(err).Message
			a.dirty()
		})
	})
	return a
}

// dirty asks for the screen to be composed again.
func (a *App) dirty() { a.gen++ }

// Update runs once per tick (60 per second).
func (a *App) Update() error {
	now := time.Now()
	dt := now.Sub(a.lastTick).Seconds()
	a.lastTick = now
	if a.raiseStep > 0 {
		a.raiseStep--
		if a.raiseStep == 0 {
			ebiten.SetWindowFloating(false)
		}
	}
	for {
		select {
		case fn := <-a.cmds:
			fn()
			continue
		case fn := <-a.loaded:
			fn()
			continue
		case d := <-a.rend.out:
			a.rendered(d)
			continue
		default:
		}
		break
	}
	if ebiten.IsWindowBeingClosed() && !a.closing {
		a.closing = true
	}
	if a.closing {
		if a.closeAt.IsZero() {
			// Let the close call's reply leave before the process ends.
			a.closeAt = now.Add(200 * time.Millisecond)
			a.rememberCamera()
		}
		if now.After(a.closeAt) {
			for _, c := range a.captures {
				close(c.done)
			}
			return ebiten.Termination
		}
	}
	select {
	case <-a.ctx.Done():
		a.closing = true
	default:
	}
	if a.sw == 0 {
		return nil
	}
	a.handleInput(now)
	if a.turntable && len(a.visiblePanes()) > 0 {
		a.cam.Az = wrapAz(a.cam.Az + 30*dt)
		a.viewName = "custom"
		a.lastChange = now
		a.dirty()
	}
	a.flushPendingCamera(now)
	a.scheduleRenders(now)
	a.serveCaptures(now)
	if a.animating(now) {
		a.dirty()
	}
	return nil
}

// animating reports whether something on screen moves by itself.
func (a *App) animating(now time.Time) bool {
	if a.highlight != nil && now.Sub(a.highlightAt) < 4*time.Second {
		return true
	}
	if now.Sub(a.loadingPulse) > 300*time.Millisecond {
		for _, p := range a.panes {
			if p.status == "loading" || p.mapsWanted {
				a.loadingPulse = now
				return true
			}
		}
		if a.pendingImages() > 0 {
			a.loadingPulse = now
			return true
		}
	}
	return false
}

// visiblePanes are the panes the layout shows.
func (a *App) visiblePanes() []*pane {
	switch a.layout {
	case viewer.LayoutCompare:
		return a.panes
	case viewer.LayoutSingle:
		if a.flat >= 0 {
			return nil
		}
		if len(a.panes) == 0 {
			return nil
		}
		i := a.selected.Pane
		if i < 0 || i >= len(a.panes) {
			i = 0
		}
		return a.panes[i : i+1]
	}
	return nil
}

func modeNeedsMaps(mode string) bool { return render3d.Mode(mode).NeedsMaps() }

// fullKey is the full-quality frame a pane should show now.
func (a *App) fullKey(p *pane) renderKey {
	r := a.ui.paneRect(p.index)
	return renderKey{mode: a.mode, cam: a.cam.r3d(), w: max(r.Dx(), 16), h: max(r.Dy(), 16), bg: a.background, overlay: a.overlay, gen: p.gen, maps: p.maps != nil}
}

// interacting reports whether quick frames are wanted.
func (a *App) interacting(now time.Time) bool {
	return a.turntable || a.in.dragging || now.Sub(a.lastChange) < settleDelay
}

func (a *App) wantKey(p *pane, now time.Time) renderKey {
	k := a.fullKey(p)
	if a.interacting(now) {
		k.interactive = true
		k.w, k.h = max(k.w/p.div, 16), max(k.h/p.div, 16)
	}
	return k
}

// scheduleRenders submits the frame each visible pane needs.
func (a *App) scheduleRenders(now time.Time) {
	for _, p := range a.visiblePanes() {
		if p.status != "ready" || p.mesh == nil {
			continue
		}
		if modeNeedsMaps(a.mode) && p.maps == nil {
			continue
		}
		want := a.wantKey(p, now)
		if p.hasShown && p.shown == want {
			continue
		}
		if p.hasShown && !p.shown.interactive && want.interactive && p.shown == a.fullKey(p) {
			continue // the full frame of this view is already up
		}
		if p.hasQueued && p.queued == want {
			continue
		}
		if p.renderErr != "" && p.queued == want {
			continue
		}
		mesh := p.mesh
		if want.interactive && p.simple != nil {
			mesh = p.simple
		}
		p.queued, p.hasQueued = want, true
		a.rend.submit(&renderJob{p: p, key: want, mesh: mesh, maps: p.maps, frame: p.frame, draft: p.draft})
	}
}

// rendered takes a finished frame.
func (a *App) rendered(d renderDone) {
	p := d.job.p
	if !containsPane(a.panes, p) || d.job.key.gen != p.gen {
		return
	}
	if p.hasQueued && p.queued == d.job.key {
		p.hasQueued = false
	}
	k := d.job.key
	if k.interactive {
		switch {
		case d.took > quickSlow && p.div < maxQuickDivisor:
			p.div++
		case d.took < quickFast && p.div > 2:
			p.div--
		}
	}
	if d.err != nil {
		var ie *render3d.InputError
		if asInputError(d.err, &ie) {
			p.renderErr = "mode " + k.mode + " " + ie.Reason
		} else {
			p.renderErr = "the render failed: " + d.err.Error()
		}
		a.dirty()
		return
	}
	full := a.fullKey(p)
	if k.interactive && p.hasShown && !p.shown.interactive && p.shown == full {
		return // a late quick frame after the full one
	}
	if !k.interactive && k != full {
		return // a full frame of a view that has moved on
	}
	p.renderErr = ""
	b := d.img.Bounds()
	if p.img == nil || p.img.Bounds().Dx() != b.Dx() || p.img.Bounds().Dy() != b.Dy() {
		if p.img != nil {
			p.img.Deallocate()
		}
		p.img = ebiten.NewImage(b.Dx(), b.Dy())
	}
	p.img.WritePixels(d.img.Pix)
	p.shown, p.hasShown = k, true
	a.dirty()
}

func containsPane(ps []*pane, p *pane) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// serveCaptures answers screenshots: at once when the screen shows full
// frames of the current state, else at their deadline.
func (a *App) serveCaptures(now time.Time) {
	if len(a.captures) == 0 || a.off == nil {
		return
	}
	ready := a.quality() == viewer.QualityFull && a.drawn == a.gen && a.drawnW == a.sw && a.drawnH == a.sh
	var keep []*captureReq
	for _, c := range a.captures {
		if !ready && now.Before(c.deadline) {
			keep = append(keep, c)
			continue
		}
		b := a.off.Bounds()
		pix := make([]byte, 4*b.Dx()*b.Dy())
		a.off.ReadPixels(pix)
		st := a.snapshot()
		q := viewer.QualityFull
		if !ready {
			q = viewer.QualityInteractive
		}
		st.Quality = q
		rep := viewer.CaptureReply{Width: b.Dx(), Height: b.Dy(), Quality: q, Waited: int(now.Sub(c.start) / time.Millisecond), State: st, PID: os.Getpid()}
		if c.since != nil {
			rep.Actions, rep.ActionsTruncated = a.actionsSince(*c.since)
		}
		go func(c *captureReq) {
			img := &image.RGBA{Pix: pix, Stride: 4 * b.Dx(), Rect: image.Rect(0, 0, b.Dx(), b.Dy())}
			opaque(img)
			var buf bytes.Buffer
			_ = png.Encode(&buf, img)
			rep.PNG = buf.Bytes()
			c.done <- rep
		}(c)
	}
	a.captures = keep
}

// opaque sets every alpha to 255 (the window is opaque; the offscreen's
// alpha is a by-product of drawing).
func opaque(img *image.RGBA) {
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
}

// Draw composes the frame into the offscreen and shows it.
func (a *App) Draw(screen *ebiten.Image) {
	if a.sw == 0 {
		return
	}
	if a.off == nil || a.off.Bounds().Dx() != a.sw || a.off.Bounds().Dy() != a.sh {
		if a.off != nil {
			a.off.Deallocate()
		}
		a.off = ebiten.NewImage(a.sw, a.sh)
		a.dirty()
	}
	if a.drawn != a.gen || a.drawnW != a.sw || a.drawnH != a.sh {
		a.ui.layout(a)
		a.off.Fill(colBg)
		a.ui.draw(a, a.off)
		a.drawn, a.drawnW, a.drawnH = a.gen, a.sw, a.sh
	}
	screen.DrawImage(a.off, nil)
}

// LayoutF makes the screen as many pixels as the window has on the
// display (crisp at any scale).
func (a *App) LayoutF(ow, oh float64) (float64, float64) {
	// Monitor is nil when the backend cannot name one; the last scale stays.
	s := a.scale
	if m := ebiten.Monitor(); m != nil {
		s = m.DeviceScaleFactor()
	}
	if !(s > 0) {
		s = 1
	}
	w, h := int(math.Ceil(ow*s)), int(math.Ceil(oh*s))
	if w != a.sw || h != a.sh || s != a.scale {
		a.sw, a.sh, a.scale = w, h, s
		a.ui.setScale(s)
		a.ui.layout(a)
		a.lastChange = time.Now()
		a.dirty()
	}
	return float64(w), float64(h)
}

// Layout is required by ebiten.Game; LayoutF is used instead.
func (a *App) Layout(ow, oh int) (int, int) {
	w, h := a.LayoutF(float64(ow), float64(oh))
	return int(w), int(h)
}
