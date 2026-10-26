package app

import (
	"image"
	"image/draw"
	"runtime/debug"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/sairaph/rodin3d-mcp/internal/render3d"
)

// Pictures go to the GPU in bands of rows of at most uploadBand bytes.
// Ebitengine copies the pixels of each WritePixels call into a buffer that
// it keeps for reuse afterwards, up to 32 buffers or 1 GiB; a whole 8192 x
// 8192 map (256 MiB) per call filled that pool to its gigabyte. In bands the
// pool holds at most 32 x 1 MiB.
const uploadBand = 1 << 20

// atlasEdge is the largest picture side that goes on Ebitengine's shared
// texture atlas. A larger picture gets a texture of its own: on the atlas an
// 8192 x 8192 map grew a page to 16384 x 16384 (1 GiB), which stayed while
// any small picture placed on it later lived.
const atlasEdge = 1024

// newImage makes a GPU image of w x h pixels: on the atlas when small, its
// own texture (freed by Deallocate) when large.
func newImage(w, h int) *ebiten.Image {
	return ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: w > atlasEdge || h > atlasEdge})
}

// newImageFrom makes a GPU image of img, written in bands.
func newImageFrom(img image.Image) *ebiten.Image {
	b := img.Bounds()
	dst := newImage(b.Dx(), b.Dy())
	w, h := b.Dx(), b.Dy()
	rows := max(1, uploadBand/(4*w))
	band := image.NewRGBA(image.Rect(0, 0, w, min(rows, h)))
	for y := 0; y < h; y += rows {
		n := min(rows, h-y)
		part := &image.RGBA{Pix: band.Pix[:4*w*n], Stride: 4 * w, Rect: image.Rect(0, 0, w, n)}
		// Src converts to the premultiplied RGBA that WritePixels takes.
		draw.Draw(part, part.Rect, img, image.Pt(b.Min.X, b.Min.Y+y), draw.Src)
		dst.SubImage(image.Rect(0, y, w, y+n)).(*ebiten.Image).WritePixels(part.Pix)
	}
	return dst
}

// writeFrame writes a rendered frame (opaque, so its NRGBA bytes are
// premultiplied already) into dst, which has its size, in bands.
func writeFrame(dst *ebiten.Image, img *image.NRGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	rows := max(1, uploadBand/(4*w))
	for y := 0; y < h; y += rows {
		n := min(rows, h-y)
		o := img.PixOffset(img.Rect.Min.X, img.Rect.Min.Y+y)
		dst.SubImage(image.Rect(0, y, w, y+n)).(*ebiten.Image).WritePixels(img.Pix[o : o+4*w*n])
	}
}

// textureImage is a render map as an image, sharing its pixels.
func textureImage(t *render3d.Texture) image.Image {
	return &image.NRGBA{Pix: t.Pix, Stride: 4 * t.W, Rect: image.Rect(0, 0, t.W, t.H)}
}

// release frees a GPU image now rather than when the collector finds it.
func release(img **ebiten.Image) {
	if *img != nil {
		(*img).Deallocate()
		*img = nil
	}
}

// trimDelay is how long after the view drops content the process collects
// it and gives the memory back to the system: long enough for a cancelled
// render to let go of it, short enough that the next content's loads find
// the memory free instead of growing the heap past it.
const trimDelay = 250 * time.Millisecond

// trimSoon asks for the memory freed by a change to go back to the system
// (game loop).
func (a *App) trimSoon() { a.trimAt = time.Now().Add(trimDelay) }

// trim returns freed memory to the system once trimAt has passed: without
// it the Go heap keeps the pages of dropped meshes and maps until a later
// collection and the background scavenger return them.
func (a *App) trim(now time.Time) {
	if a.trimAt.IsZero() || now.Before(a.trimAt) {
		return
	}
	a.trimAt = time.Time{}
	go debug.FreeOSMemory()
}
