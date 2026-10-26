package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// Colours of the window (dark theme).
var (
	colBg      = color.RGBA{0x1b, 0x1e, 0x23, 0xff}
	colBar     = color.RGBA{0x25, 0x29, 0x30, 0xff}
	colBtn     = color.RGBA{0x34, 0x39, 0x43, 0xff}
	colHover   = color.RGBA{0x41, 0x47, 0x53, 0xff}
	colActive  = color.RGBA{0x3d, 0x6c, 0xf0, 0xff}
	colText    = color.RGBA{0xe8, 0xe9, 0xeb, 0xff}
	colDim     = color.RGBA{0x99, 0xa0, 0xaa, 0xff}
	colOff     = color.RGBA{0x5c, 0x62, 0x6c, 0xff}
	colPane    = color.RGBA{0x10, 0x12, 0x16, 0xff}
	colWarn    = color.RGBA{0xff, 0xcf, 0x4a, 0xff}
	colWarnBg  = color.RGBA{0x4a, 0x3d, 0x12, 0xff}
	colErr     = color.RGBA{0xff, 0x70, 0x70, 0xff}
	colGlow    = color.RGBA{0xff, 0xd0, 0x40, 0xff}
	colBadge   = color.RGBA{0x2b, 0x8a, 0x5f, 0xff}
	colUnknown = color.RGBA{0x6b, 0x6f, 0x78, 0xff}
)

// widget is a clickable control of this frame.
type widget struct {
	id       string
	r        image.Rectangle
	label    string
	active   bool
	disabled bool
	small    bool
	onClick  func()
}

// uiState is the layout of the current frame: widgets and regions in
// screen pixels, rebuilt from the state each tick.
type uiState struct {
	u          float64
	src, bold  *text.GoTextFaceSource
	face       *text.GoTextFace
	faceSmall  *text.GoTextFace
	faceBold   *text.GoTextFace
	faceBig    *text.GoTextFace
	widgets    []widget
	hover      string
	dropTarget int

	toolbar  image.Rectangle
	main     image.Rectangle
	stripR   image.Rectangle
	status   image.Rectangle
	banner   image.Rectangle
	panes    map[int]image.Rectangle
	stripC   []image.Rectangle
	cards    []image.Rectangle
	compass  image.Rectangle
	texView  image.Rectangle
	texTabs  image.Rectangle
	flatView image.Rectangle
}

func newUI() *uiState {
	s := &uiState{dropTarget: -1, panes: map[int]image.Rectangle{}}
	s.src, _ = text.NewGoTextFaceSource(bytes.NewReader(goregular.TTF))
	s.bold, _ = text.NewGoTextFaceSource(bytes.NewReader(gobold.TTF))
	s.setScale(1)
	return s
}

func (s *uiState) setScale(u float64) {
	s.u = u
	s.face = &text.GoTextFace{Source: s.src, Size: 13 * u}
	s.faceSmall = &text.GoTextFace{Source: s.src, Size: 11 * u}
	s.faceBold = &text.GoTextFace{Source: s.bold, Size: 13 * u}
	s.faceBig = &text.GoTextFace{Source: s.bold, Size: 17 * u}
}

func (s *uiState) px(v float64) int { return int(math.Round(v * s.u)) }

func (s *uiState) paneRect(i int) image.Rectangle { return s.panes[i] }

// hitWidget returns the id of the widget at x, y ("" for none); the tag
// picker lies on top of everything.
func (s *uiState) hitWidget(x, y int) string {
	p := image.Pt(x, y)
	for i := len(s.widgets) - 1; i >= 0; i-- {
		w := s.widgets[i]
		if !w.disabled && p.In(w.r) {
			return w.id
		}
	}
	return ""
}

// hitRegion names the area at x, y that is not a widget: pane:N, tex,
// card:N, or "".
func (s *uiState) hitRegion(x, y int) string {
	p := image.Pt(x, y)
	for i, r := range s.panes {
		if p.In(r) {
			return "pane:" + strconv.Itoa(i)
		}
	}
	if p.In(s.texView) {
		return "tex"
	}
	if i := s.cardAt(x, y); i >= 0 {
		return "card:" + strconv.Itoa(i)
	}
	return ""
}

func (s *uiState) cardAt(x, y int) int {
	p := image.Pt(x, y)
	for i, r := range s.cards {
		if p.In(r) {
			return i
		}
	}
	return -1
}

func (s *uiState) click(id string) {
	for _, w := range s.widgets {
		if w.id == id && !w.disabled && w.onClick != nil {
			w.onClick()
			return
		}
	}
}

func (s *uiState) textW(str string, f *text.GoTextFace) int {
	return int(math.Ceil(text.Advance(str, f)))
}

func (s *uiState) lineH(f *text.GoTextFace) int {
	m := f.Metrics()
	return int(math.Ceil(m.HAscent + m.HDescent))
}

// layout places every widget and region for the state of a.
func (s *uiState) layout(a *App) {
	s.widgets = s.widgets[:0]
	s.panes = map[int]image.Rectangle{}
	s.stripC, s.cards = nil, nil
	s.texView, s.texTabs, s.compass, s.flatView, s.banner = image.Rectangle{}, image.Rectangle{}, image.Rectangle{}, image.Rectangle{}, image.Rectangle{}
	W, H := a.sw, a.sh
	pad, gap := s.px(6), s.px(4)
	btnH := s.px(24)

	// Toolbar: groups of buttons that wrap onto more rows.
	x, y := pad, pad
	place := func(id, label string, active, disabled, small bool, onClick func()) {
		f := s.face
		if small {
			f = s.faceSmall
		}
		w := s.textW(label, f) + 2*s.px(9)
		if x+w > W-pad && x > pad {
			x, y = pad, y+btnH+gap
		}
		s.widgets = append(s.widgets, widget{id: id, r: image.Rect(x, y, x+w, y+btnH), label: label, active: active, disabled: disabled, small: small, onClick: onClick})
		x += w + gap
	}
	group := func() {
		if x > pad {
			x += s.px(10)
		}
	}
	if layouts := a.layouts(); len(layouts) > 1 {
		for _, l := range layouts {
			l := l
			place("layout:"+l, layoutLabel(l), a.layout == l, false, false, func() { a.userLayout(l) })
		}
		group()
	}
	if a.kind == viewer.KindTask && len(a.panes) == 1 && a.panes[0].vt != nil && a.layout == viewer.LayoutSingle {
		p := a.panes[0]
		for _, st := range meshStages(p.vt) {
			st := st
			place("stage:"+st, stageLabel(st), p.stage.Name == st, false, false, func() { a.userStage(st) })
		}
		if p.stage.Name == "geometry_draft" && len(p.vt.Versions) > 1 {
			for _, v := range p.vt.Versions {
				v := v
				place("version:"+v, v, p.stage.Version == v, false, true, func() { a.userVersion(v) })
			}
		}
		group()
	}
	threeD := len(a.panes) > 0 && (a.layout == viewer.LayoutSingle || a.layout == viewer.LayoutCompare) && a.flat < 0
	if threeD {
		for _, m := range a.modesOf(a.panes) {
			m := m
			place("mode:"+m, modeLabel(m), a.mode == m, false, false, func() { a.userMode(m) })
		}
		group()
		for _, v := range viewer.Views {
			v := v
			place("view:"+v, viewLabel(v), a.viewName == v, false, true, func() { a.userView(v) })
		}
		group()
		place("toggle:turntable", "Turntable", a.turntable, false, true, a.userTurntable)
		place("toggle:wireframe", "Wire", a.overlay, false, true, a.userOverlay)
		place("toggle:background", "Bg: "+a.background, a.background != "default", false, true, a.userBackground)
		place("reset", "Reset", false, false, true, a.userReset)
	}
	if a.layout == viewer.LayoutTextures {
		for i, t := range a.textures {
			i := i
			place("texture:"+t.name, t.name, a.texSel == i, false, false, func() { a.userSelectTexture(i) })
		}
		group()
		place("texfit", "Fit", false, false, true, func() {
			a.texZoom, a.texPan = 1, [2]float64{}
			a.userAction("texture_fit", "")
		})
	}
	if a.flat >= 0 && a.flat < len(a.strip) {
		place("back3d", "Back to 3D", false, false, false, func() { a.userSelectImage(-1) })
	}
	s.toolbar = image.Rect(0, 0, W, y+btnH+pad)
	if a.kind == viewer.KindNone {
		s.toolbar = image.Rect(0, 0, W, 0)
	}

	// Bottom: status line; image strip above it in the 3D layouts.
	statusH := s.px(22)
	s.status = image.Rect(0, H-statusH, W, H)
	bottom := s.status.Min.Y
	if (a.layout == viewer.LayoutSingle || a.layout == viewer.LayoutCompare) && len(a.strip) > 0 {
		stripH := s.px(118)
		s.stripR = image.Rect(0, bottom-stripH, W, bottom)
		bottom = s.stripR.Min.Y
		cellH := stripH - 2*pad - s.lineH(s.faceSmall) - gap
		cx := pad
		for i, t := range a.strip {
			w := cellH
			if t.w > 0 && t.h > 0 {
				w = int(float64(cellH) * float64(t.w) / float64(t.h))
			}
			w = min(max(w, cellH*2/3), cellH*2)
			r := image.Rect(cx, s.stripR.Min.Y+pad, cx+w, s.stripR.Min.Y+pad+cellH)
			s.stripC = append(s.stripC, r)
			i := i
			s.widgets = append(s.widgets, widget{id: "strip:" + strconv.Itoa(i), r: r.Union(image.Rect(r.Min.X, r.Max.Y, r.Max.X, s.stripR.Max.Y-pad)), onClick: func() {
				if a.flat == i {
					a.userSelectImage(-1)
				} else {
					a.userSelectImage(i)
				}
			}})
			cx += w + s.px(10)
		}
	} else {
		s.stripR = image.Rectangle{}
	}
	top := s.toolbar.Max.Y
	if a.message != "" {
		bh := s.px(30)
		s.banner = image.Rect(0, top, W, top+bh)
		top += bh
		cw := s.px(26)
		s.widgets = append(s.widgets, widget{id: "banner:close", r: image.Rect(W-pad-cw, s.banner.Min.Y+s.px(3), W-pad, s.banner.Max.Y-s.px(3)), label: "x", onClick: a.userDismiss})
	}
	s.main = image.Rect(0, top, W, bottom)

	switch a.layout {
	case viewer.LayoutSingle:
		if a.flat >= 0 {
			s.flatView = s.main.Inset(pad)
			break
		}
		if vp := a.visiblePanes(); len(vp) == 1 {
			s.panes[vp[0].index] = s.main
		}
	case viewer.LayoutCompare:
		half := s.main.Dx() / 2
		for i := range a.panes {
			r := image.Rect(s.main.Min.X+i*half, s.main.Min.Y, s.main.Min.X+(i+1)*half, s.main.Max.Y)
			if i == 0 {
				r.Max.X -= s.px(2)
			} else {
				r.Min.X += s.px(2)
			}
			s.panes[i] = r
		}
	case viewer.LayoutInputs:
		s.layoutInputs(a)
	case viewer.LayoutTextures:
		s.texView = s.main.Inset(pad)
	}
	if a.popup >= 0 && a.popup < len(s.cards) {
		s.layoutPopup(a)
	}
}

// layoutInputs places the input cards and the compass.
func (s *uiState) layoutInputs(a *App) {
	if a.inputs == nil {
		return
	}
	pad := s.px(8)
	n := len(a.inputs.items)
	headH := s.lineH(s.face) + 2*pad
	area := image.Rect(s.main.Min.X+pad, s.main.Min.Y+headH, s.main.Max.X-pad, s.main.Max.Y-pad)
	compassW := min(s.px(300), area.Dx()/3)
	s.compass = image.Rect(area.Max.X-compassW, area.Min.Y, area.Max.X, area.Min.Y+compassW)
	cardsW := area.Dx() - compassW - pad
	cw := min(s.px(230), (cardsW-pad*(n-1))/max(n, 1))
	ch := min(cw+s.px(76), area.Dy())
	for i := 0; i < n; i++ {
		x := area.Min.X + i*(cw+pad)
		r := image.Rect(x, area.Min.Y, x+cw, area.Min.Y+ch)
		s.cards = append(s.cards, r)
		if !a.inputs.editable {
			continue
		}
		i := i
		bs := s.px(34)
		badge := image.Rect(r.Min.X+s.px(6), r.Min.Y+s.px(6), r.Min.X+s.px(6)+bs+s.px(10), r.Min.Y+s.px(6)+bs)
		s.widgets = append(s.widgets, widget{id: "badge:" + strconv.Itoa(i), r: badge, onClick: func() {
			if a.popup == i {
				a.popup = -1
			} else {
				a.popup = i
			}
			a.dirty()
		}})
		by := r.Max.Y - s.px(30)
		bw := s.px(26)
		s.widgets = append(s.widgets,
			widget{id: "weight-:" + strconv.Itoa(i), r: image.Rect(r.Min.X+s.px(6), by, r.Min.X+s.px(6)+bw, by+s.px(24)), label: "-", onClick: func() { a.userWeight(i, -0.05) }},
			widget{id: "weight+:" + strconv.Itoa(i), r: image.Rect(r.Max.X-s.px(6)-bw, by, r.Max.X-s.px(6), by+s.px(24)), label: "+", onClick: func() { a.userWeight(i, 0.05) }})
	}
}

// layoutPopup places the tag picker of the open card.
func (s *uiState) layoutPopup(a *App) {
	r := s.cards[a.popup]
	bw, bh, g := s.px(40), s.px(28), s.px(4)
	cols := 4
	x0, y0 := r.Min.X+s.px(6), r.Min.Y+s.px(46)
	if x0+cols*(bw+g) > a.sw {
		x0 = a.sw - cols*(bw+g) - s.px(6)
	}
	i := a.popup
	for k, tag := range viewer.ViewTags {
		tag := tag
		x, y := x0+(k%cols)*(bw+g), y0+(k/cols)*(bh+g)
		s.widgets = append(s.widgets, widget{id: "tag:" + tag, r: image.Rect(x, y, x+bw, y+bh), label: tag, active: a.inputs.items[i].view == tag, onClick: func() { a.userSetTag(i, tag) }})
	}
}

// --- drawing ---

func (s *uiState) fill(dst *ebiten.Image, r image.Rectangle, c color.Color) {
	if r.Empty() {
		return
	}
	vector.FillRect(dst, float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()), c, false)
}

func (s *uiState) stroke(dst *ebiten.Image, r image.Rectangle, width float64, c color.Color) {
	if r.Empty() {
		return
	}
	vector.StrokeRect(dst, float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()), float32(width*s.u), c, true)
}

// text draws str with its top-left at x, y.
func (s *uiState) text(dst *ebiten.Image, str string, f *text.GoTextFace, x, y int, c color.Color) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(float64(x), float64(y))
	op.ColorScale.ScaleWithColor(c)
	text.Draw(dst, str, f, op)
}

// textIn draws str centred in r.
func (s *uiState) textIn(dst *ebiten.Image, str string, f *text.GoTextFace, r image.Rectangle, c color.Color) {
	w, h := s.textW(str, f), s.lineH(f)
	s.text(dst, str, f, r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2, c)
}

// fit returns the rectangle an image of w x h takes when fitted into r.
func fitRect(r image.Rectangle, w, h int) image.Rectangle {
	if w <= 0 || h <= 0 || r.Empty() {
		return r
	}
	k := math.Min(float64(r.Dx())/float64(w), float64(r.Dy())/float64(h))
	fw, fh := int(float64(w)*k), int(float64(h)*k)
	x, y := r.Min.X+(r.Dx()-fw)/2, r.Min.Y+(r.Dy()-fh)/2
	return image.Rect(x, y, x+fw, y+fh)
}

// image draws img scaled into dst rectangle r.
func (s *uiState) image(dst, img *ebiten.Image, r image.Rectangle, alpha float32) {
	b := img.Bounds()
	if b.Empty() || r.Empty() {
		return
	}
	op := &ebiten.DrawImageOptions{}
	sx, sy := float64(r.Dx())/float64(b.Dx()), float64(r.Dy())/float64(b.Dy())
	op.GeoM.Scale(sx, sy)
	op.GeoM.Translate(float64(r.Min.X), float64(r.Min.Y))
	if sx != 1 || sy != 1 {
		op.Filter = ebiten.FilterLinear
	}
	if alpha < 1 {
		op.ColorScale.ScaleAlpha(alpha)
	}
	dst.DrawImage(img, op)
}

func (s *uiState) draw(a *App, dst *ebiten.Image) {
	s.fill(dst, s.toolbar, colBar)
	switch a.layout {
	case viewer.LayoutSingle, viewer.LayoutCompare:
		if a.flat >= 0 && a.flat < len(a.strip) {
			s.drawFlat(a, dst)
		} else {
			for _, p := range a.visiblePanes() {
				s.drawPane(a, dst, p)
			}
		}
	case viewer.LayoutInputs:
		s.drawInputs(a, dst)
	case viewer.LayoutTextures:
		s.drawTextures(a, dst)
	}
	if a.kind == viewer.KindNone {
		s.textIn(dst, "Nothing to show yet: the agent puts tasks, images and models here.", s.face, s.main, colDim)
	}
	s.drawStrip(a, dst)
	if !s.banner.Empty() {
		s.fill(dst, s.banner, colWarnBg)
		s.text(dst, clipText(a.message, 220), s.faceBold, s.px(10), s.banner.Min.Y+(s.banner.Dy()-s.lineH(s.faceBold))/2, colWarn)
	}
	for _, w := range s.widgets {
		s.drawWidget(dst, w)
	}
	s.drawStatus(a, dst)
}

func (s *uiState) drawWidget(dst *ebiten.Image, w widget) {
	if strings.HasPrefix(w.id, "strip:") || strings.HasPrefix(w.id, "badge:") {
		return // drawn with their pictures
	}
	bg := colBtn
	switch {
	case w.active:
		bg = colActive
	case w.id == s.hover:
		bg = colHover
	}
	if w.id == "banner:close" {
		bg = colWarnBg
		if w.id == s.hover {
			bg = colHover
		}
	}
	s.fill(dst, w.r, bg)
	f := s.face
	if w.small {
		f = s.faceSmall
	}
	c := colText
	if w.disabled {
		c = colOff
	}
	s.textIn(dst, w.label, f, w.r, c)
}

func (s *uiState) drawPane(a *App, dst *ebiten.Image, p *pane) {
	r := s.panes[p.index]
	s.fill(dst, r, colPane)
	if p.img != nil {
		s.image(dst, p.img, r, 1)
	} else if poster := a.posterThumb(); poster != nil && p.kind == viewer.KindTask && len(a.panes) == 1 {
		s.image(dst, poster.img, fitRect(r.Inset(s.px(40)), poster.w, poster.h), 0.45)
	}
	msg, col := "", colText
	switch {
	case p.status == "loading":
		dots := strings.Repeat(".", int(time.Now().UnixMilli()/300%4))
		msg = "Loading the " + p.what + dots
	case p.status == "error":
		msg, col = "Cannot show this: "+p.err, colErr
	case p.renderErr != "":
		msg, col = "Cannot draw "+p.renderErr, colErr
	case p.mapsWanted && modeNeedsMaps(a.mode):
		dots := strings.Repeat(".", int(time.Now().UnixMilli()/300%4))
		msg = "Loading the textures" + dots
	}
	if msg != "" {
		box := image.Rect(r.Min.X, r.Max.Y-s.px(34), r.Max.X, r.Max.Y)
		s.fill(dst, box, color.RGBA{0, 0, 0, 0xb0})
		s.textIn(dst, clipText(msg, 160), s.face, box, col)
	}
	if a.layout == viewer.LayoutCompare {
		label := fmt.Sprintf("%d  %s  %s", p.index, clipText(p.label(), 50), stageLabel(p.stage.Name))
		s.fill(dst, image.Rect(r.Min.X, r.Min.Y, r.Min.X+s.textW(label, s.faceSmall)+s.px(16), r.Min.Y+s.px(22)), color.RGBA{0, 0, 0, 0x90})
		s.text(dst, label, s.faceSmall, r.Min.X+s.px(8), r.Min.Y+s.px(4), colText)
		if a.selected.Pane == p.index {
			s.stroke(dst, r, 2, colActive)
		}
	}
}

func (p *pane) label() string {
	if p.vt != nil {
		return p.vt.Prompt
	}
	return filepath.Base(p.file)
}

// posterThumb is the strip's poster, when loaded.
func (a *App) posterThumb() *thumb {
	for _, t := range a.strip {
		if t.kind == "poster" && t.img != nil {
			return t
		}
	}
	return nil
}

func (s *uiState) drawFlat(a *App, dst *ebiten.Image) {
	t := a.strip[a.flat]
	s.fill(dst, s.main, colPane)
	img := t.full
	if img == nil {
		img = t.img
	}
	if img != nil {
		s.image(dst, img, fitRect(s.flatView, img.Bounds().Dx(), img.Bounds().Dy()), 1)
	} else if t.err != "" {
		s.textIn(dst, t.err, s.face, s.main, colErr)
	}
	cap := stripCaption(t)
	s.text(dst, cap, s.faceBold, s.main.Min.X+s.px(10), s.main.Max.Y-s.lineH(s.faceBold)-s.px(8), colText)
}

func stripCaption(t *thumb) string {
	switch t.kind {
	case "input":
		return "input " + t.name + "  view " + t.view
	case "poster":
		return "server poster"
	case "geometry_image":
		return "server geometry image"
	case "material_image":
		return "server material image"
	}
	return t.name
}

func (s *uiState) drawStrip(a *App, dst *ebiten.Image) {
	if s.stripR.Empty() {
		return
	}
	s.fill(dst, s.stripR, colBar)
	for i, r := range s.stripC {
		if i >= len(a.strip) {
			break
		}
		t := a.strip[i]
		s.fill(dst, r, colPane)
		if t.img != nil {
			s.image(dst, t.img, fitRect(r, t.img.Bounds().Dx(), t.img.Bounds().Dy()), 1)
		} else if t.err != "" {
			s.textIn(dst, "!", s.faceBold, r, colErr)
		} else {
			s.textIn(dst, "...", s.face, r, colDim)
		}
		label := map[string]string{"poster": "poster", "geometry_image": "geometry", "material_image": "material", "input": "input"}[t.kind]
		if t.kind == "input" {
			s.drawTag(dst, t.view, image.Rect(r.Min.X+s.px(3), r.Min.Y+s.px(3), r.Min.X+s.px(3)+s.px(30), r.Min.Y+s.px(3)+s.px(22)), s.faceBold)
		}
		s.text(dst, label, s.faceSmall, r.Min.X, r.Max.Y+s.px(3), colDim)
		if a.flat == i {
			s.stroke(dst, r, 2, colActive)
		}
		if a.highlightsImage(i) {
			s.glow(a, dst, r)
		}
	}
}

// drawTag draws a view tag badge.
func (s *uiState) drawTag(dst *ebiten.Image, tag string, r image.Rectangle, f *text.GoTextFace) {
	c := colBadge
	if tag == "?" || tag == "" {
		c = colUnknown
	}
	vector.FillRect(dst, float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()), c, false)
	if tag == "" {
		tag = "?"
	}
	s.textIn(dst, tag, f, r, colText)
}

// highlightsImage reports whether image i is highlighted (by index, or by
// its view tag).
func (a *App) highlightsImage(i int) bool {
	h := a.highlight
	if h == nil {
		return false
	}
	if h.ImageIndex != nil && *h.ImageIndex == i {
		return true
	}
	if h.View != nil {
		var items []*thumb
		if a.layout == viewer.LayoutInputs && a.inputs != nil {
			items = a.inputs.items
		} else {
			items = a.strip
		}
		if i < len(items) && items[i].kind == "input" && items[i].view == *h.View {
			return true
		}
	}
	return false
}

// glow outlines r, pulsing for 4 s after the highlight was set.
func (s *uiState) glow(a *App, dst *ebiten.Image, r image.Rectangle) {
	t := time.Since(a.highlightAt).Seconds()
	alpha := 1.0
	if t < 4 {
		alpha = 0.55 + 0.45*math.Cos(t*2*math.Pi)
	}
	c := colGlow
	c.A = uint8(255 * alpha)
	s.stroke(dst, r.Inset(-s.px(3)), 3, c)
}

func (s *uiState) drawInputs(a *App, dst *ebiten.Image) {
	s.fill(dst, s.main, colBg)
	if a.inputs == nil {
		return
	}
	pad := s.px(8)
	head := "Task inputs with their stored view tags (read-only; show_in_viewer task_inputs makes an editable copy)"
	if a.inputs.editable {
		head = fmt.Sprintf("Input set %s: click a tag to change it, drag a card to reorder, - and + set its weight. generate_model takes input_set %s.", a.inputs.id, a.inputs.id)
	}
	s.text(dst, clipText(head, 200), s.face, s.main.Min.X+pad, s.main.Min.Y+pad, colDim)
	for i, r := range s.cards {
		if i >= len(a.inputs.items) {
			break
		}
		it := a.inputs.items[i]
		s.fill(dst, r, colBar)
		imgR := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+r.Dx())
		s.fill(dst, imgR.Inset(s.px(4)), colPane)
		if it.img != nil {
			s.image(dst, it.img, fitRect(imgR.Inset(s.px(4)), it.img.Bounds().Dx(), it.img.Bounds().Dy()), 1)
		} else if it.err != "" {
			s.textIn(dst, clipText(it.err, 40), s.faceSmall, imgR, colErr)
		} else {
			s.textIn(dst, "...", s.face, imgR, colDim)
		}
		bs := s.px(34)
		badge := image.Rect(r.Min.X+s.px(6), r.Min.Y+s.px(6), r.Min.X+s.px(6)+bs+s.px(10), r.Min.Y+s.px(6)+bs)
		s.drawTag(dst, it.view, badge, s.faceBig)
		if a.inputs.editable && s.hover == "badge:"+strconv.Itoa(i) {
			s.stroke(dst, badge, 2, colText)
		}
		s.text(dst, fmt.Sprintf("#%d  %s", i, clipText(it.name, 26)), s.faceSmall, r.Min.X+s.px(6), imgR.Max.Y+s.px(4), colText)
		wtext := "weight auto"
		if it.weight != nil {
			wtext = fmt.Sprintf("weight %.2f", *it.weight)
		}
		wr := image.Rect(r.Min.X, r.Max.Y-s.px(30), r.Max.X, r.Max.Y-s.px(6))
		s.textIn(dst, wtext, s.faceSmall, wr, colDim)
		if a.selected.ImageIndex != nil && *a.selected.ImageIndex == i {
			s.stroke(dst, r, 2, colActive)
		}
		if a.highlightsImage(i) {
			s.glow(a, dst, r)
		}
		if s.dropTarget == i && strings.HasPrefix(a.in.pressed, "card:") && cardIndex(a.in.pressed) != i {
			s.stroke(dst, r.Inset(-s.px(2)), 3, colActive)
		}
	}
	if strings.HasPrefix(a.in.pressed, "card:") && a.in.moved {
		from := cardIndex(a.in.pressed)
		if from < len(s.cards) && from < len(a.inputs.items) && a.inputs.items[from].img != nil {
			r := s.cards[from]
			w, h := r.Dx()*2/3, r.Dx()*2/3
			ghost := image.Rect(a.in.x-w/2, a.in.y-h/2, a.in.x+w/2, a.in.y+h/2)
			s.image(dst, a.inputs.items[from].img, fitRect(ghost, a.inputs.items[from].img.Bounds().Dx(), a.inputs.items[from].img.Bounds().Dy()), 0.6)
		}
	}
	if a.popup >= 0 {
		var area image.Rectangle
		for _, w := range s.widgets {
			if strings.HasPrefix(w.id, "tag:") {
				area = area.Union(w.r)
			}
		}
		s.fill(dst, area.Inset(-s.px(6)), colBar)
		s.stroke(dst, area.Inset(-s.px(6)), 1, colDim)
	}
	s.drawCompass(a, dst)
}

// tagDir is where a view tag puts its camera round the model, seen from
// above with the front at the bottom: dx right (+X), dy down (+Z, front).
var tagDir = map[string][2]float64{
	"F": {0, 1}, "B": {0, -1}, "L": {-1, 0}, "R": {1, 0},
	"FL": {-0.7071, 0.7071}, "FR": {0.7071, 0.7071}, "BL": {-0.7071, -0.7071}, "BR": {0.7071, -0.7071},
}

// drawCompass draws the top-down placement of the input images round the
// model: each image sits at its tag's side; U and D inside, ? below.
func (s *uiState) drawCompass(a *App, dst *ebiten.Image) {
	r := s.compass
	if r.Empty() {
		return
	}
	s.fill(dst, r, colBar)
	s.text(dst, "Seen from above (front at the bottom)", s.faceSmall, r.Min.X+s.px(8), r.Min.Y+s.px(6), colDim)
	cx, cy := float32(r.Min.X+r.Dx()/2), float32(r.Min.Y+r.Dy()/2+s.px(8))
	rad := float32(r.Dx()) * 0.36
	vector.StrokeCircle(dst, cx, cy, rad, float32(s.u), colOff, true)
	box := float32(s.px(26))
	vector.FillRect(dst, cx-box/2, cy-box/2, box, box, colOff, false)
	for tag, d := range tagDir {
		x, y := cx+float32(d[0])*(rad+float32(s.px(16))), cy+float32(d[1])*(rad+float32(s.px(16)))
		lr := image.Rect(int(x)-s.px(12), int(y)-s.px(9), int(x)+s.px(12), int(y)+s.px(9))
		c := colDim
		if a.highlight != nil && a.highlight.View != nil && *a.highlight.View == tag {
			c = colGlow
		}
		s.textIn(dst, tag, s.faceSmall, lr, c)
	}
	var unknown []int
	stack := map[string]int{}
	for i, it := range a.inputs.items {
		var x, y float32
		switch it.view {
		case "U", "D":
			off := float32(-1)
			if it.view == "D" {
				off = 1
			}
			x, y = cx+float32(stack[it.view]*s.px(22)), cy+off*float32(s.px(30))
		default:
			d, ok := tagDir[it.view]
			if !ok {
				unknown = append(unknown, i)
				continue
			}
			k := float32(1 - 0.18*float64(stack[it.view]))
			x, y = cx+float32(d[0])*rad*k, cy+float32(d[1])*rad*k
			vector.StrokeLine(dst, cx, cy, x, y, float32(s.u), colOff, true)
		}
		stack[it.view]++
		s.marker(a, dst, i, x, y)
	}
	for k, i := range unknown {
		x := float32(r.Min.X+s.px(20)) + float32(k*s.px(28))
		y := float32(r.Max.Y - s.px(16))
		s.marker(a, dst, i, x, y)
	}
	if len(unknown) > 0 {
		s.text(dst, "? untagged", s.faceSmall, r.Min.X+s.px(8), r.Max.Y-s.px(40), colDim)
	}
}

func (s *uiState) marker(a *App, dst *ebiten.Image, i int, x, y float32) {
	rr := float32(s.px(11))
	c := colActive
	if a.highlightsImage(i) {
		c = colGlow
	}
	vector.FillCircle(dst, x, y, rr, c, true)
	s.textIn(dst, strconv.Itoa(i), s.faceSmall, image.Rect(int(x-rr), int(y-rr), int(x+rr), int(y+rr)), colText)
}

func (s *uiState) drawTextures(a *App, dst *ebiten.Image) {
	r := s.texView
	s.fill(dst, s.main, colBg)
	s.fill(dst, r, colPane)
	if a.texSel < 0 || a.texSel >= len(a.textures) {
		s.textIn(dst, "No texture to show.", s.face, r, colDim)
		return
	}
	t := a.textures[a.texSel]
	switch {
	case t.img != nil:
		fr := fitRect(r, t.img.Bounds().Dx(), t.img.Bounds().Dy())
		w, h := float64(fr.Dx())*a.texZoom, float64(fr.Dy())*a.texZoom
		cx, cy := float64(r.Min.X+r.Dx()/2)+a.texPan[0], float64(r.Min.Y+r.Dy()/2)+a.texPan[1]
		sub := dst.SubImage(r).(*ebiten.Image)
		s.image(sub, t.img, image.Rect(int(cx-w/2), int(cy-h/2), int(cx+w/2), int(cy+h/2)), 1)
		cap := fmt.Sprintf("%s  %dx%d  zoom %.1f", t.name, t.w, t.h, a.texZoom)
		if t.kind == "map" {
			cap += "  (a flat UV layout of the model's texture, not a view of the model)"
		}
		s.fill(dst, image.Rect(r.Min.X, r.Max.Y-s.px(24), r.Max.X, r.Max.Y), color.RGBA{0, 0, 0, 0xa0})
		s.text(dst, cap, s.faceSmall, r.Min.X+s.px(8), r.Max.Y-s.px(20), colText)
	case t.err != "":
		s.textIn(dst, t.name+": "+t.err, s.face, r, colErr)
	default:
		s.textIn(dst, "Loading "+t.name+"...", s.face, r, colDim)
	}
}

func (s *uiState) drawStatus(a *App, dst *ebiten.Image) {
	s.fill(dst, s.status, colBar)
	var left []string
	switch a.kind {
	case viewer.KindTask:
		if len(a.panes) == 1 && a.panes[0].vt != nil {
			p := a.panes[0]
			left = append(left, "task "+p.vt.UUID[:8], clipText(p.vt.Prompt, 50), stageLabel(p.stage.Name))
			if p.mesh != nil {
				left = append(left, fmt.Sprintf("%d triangles", len(p.mesh.Idx)/3))
			}
		}
	case viewer.KindCompare:
		left = append(left, "compare "+a.info.Tasks[0][:8]+" / "+a.info.Tasks[1][:8])
	case viewer.KindModelFile:
		left = append(left, filepath.Base(a.info.File))
	case viewer.KindInputSet, viewer.KindTaskInputs:
		left = append(left, "input set "+a.info.InputSetID)
	case viewer.KindTextures:
		left = append(left, "textures of "+a.info.TaskID[:8])
	case viewer.KindImages:
		left = append(left, fmt.Sprintf("%d images", len(a.info.Files)))
	}
	if len(a.visiblePanes()) > 0 {
		left = append(left, "mode "+a.mode, "view "+a.viewName)
	}
	s.text(dst, strings.Join(left, "  |  "), s.faceSmall, s.px(8), s.status.Min.Y+(s.status.Dy()-s.lineH(s.faceSmall))/2, colDim)
	right := fmt.Sprintf("%s  |  revision %d  |  %dx%d", a.quality(), a.rev, a.sw, a.sh)
	s.text(dst, right, s.faceSmall, s.status.Max.X-s.textW(right, s.faceSmall)-s.px(8), s.status.Min.Y+(s.status.Dy()-s.lineH(s.faceSmall))/2, colDim)
}

// Labels.

func layoutLabel(l string) string {
	return map[string]string{"single": "3D", "compare": "Compare", "inputs": "Inputs", "textures": "Textures"}[l]
}

func stageLabel(s string) string {
	return map[string]string{"geometry_draft": "Draft", "geometry": "Geometry", "material": "Material"}[s]
}

var modeNames = map[string]string{"normal": "Normal", "raw": "Raw", "classic": "Classic", "glossy": "Glossy", "copper": "Copper", "metal": "Metal",
	"ao": "AO", "wireframe": "Wireframe", "geometry": "Geometry", "pbr": "PBR", "shaded": "Shaded", "toon": "Toon", "tracer": "Tracer", "depth": "Depth", "uv_checker": "UV checker"}

func modeLabel(m string) string {
	if n, ok := modeNames[m]; ok {
		return n
	}
	return m
}

func viewLabel(v string) string {
	return map[string]string{"front": "Front", "back": "Back", "left": "Left", "right": "Right", "top": "Top", "three_quarter": "3/4", "web": "Web"}[v]
}

// export lists the widgets and regions on screen (id -> x, y, w, h), for
// inject_input.
func (s *uiState) export() map[string][4]int {
	out := map[string][4]int{}
	put := func(id string, r image.Rectangle) {
		if !r.Empty() {
			out[id] = [4]int{r.Min.X, r.Min.Y, r.Dx(), r.Dy()}
		}
	}
	for _, w := range s.widgets {
		put(w.id, w.r)
	}
	for i, r := range s.panes {
		put("pane:"+strconv.Itoa(i), r)
	}
	for i, r := range s.cards {
		put("card:"+strconv.Itoa(i), r)
	}
	put("tex", s.texView)
	put("compass", s.compass)
	put("main", s.main)
	return out
}
