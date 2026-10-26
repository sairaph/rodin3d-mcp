package fakehyper

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// WebP is a 64x48 lossy webp (a gradient with a white square) rendered by a
// browser canvas: a real file for the webp decoder.
var WebP = mustB64("UklGRuQCAABXRUJQVlA4WAoAAAAgAAAAPwAALwAASUNDUMgBAAAAAAHIAAAAAAQwAABtbnRyUkdCIFhZWiAH4AABAAEAAAAAAABhY3NwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAA9tYAAQAAAADTLQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAlkZXNjAAAA8AAAACRyWFlaAAABFAAAABRnWFlaAAABKAAAABRiWFlaAAABPAAAABR3dHB0AAABUAAAABRyVFJDAAABZAAAAChnVFJDAAABZAAAAChiVFJDAAABZAAAAChjcHJ0AAABjAAAADxtbHVjAAAAAAAAAAEAAAAMZW5VUwAAAAgAAAAcAHMAUgBHAEJYWVogAAAAAAAAb6IAADj1AAADkFhZWiAAAAAAAABimQAAt4UAABjaWFlaIAAAAAAAACSgAAAPhAAAts9YWVogAAAAAAAA9tYAAQAAAADTLXBhcmEAAAAAAAQAAAACZmYAAPKnAAANWQAAE9AAAApbAAAAAAAAAABtbHVjAAAAAAAAAAEAAAAMZW5VUwAAACAAAAAcAEcAbwBvAGcAbABlACAASQBuAGMALgAgADIAMAAxADZWUDgg9gAAABAHAJ0BKkAAMAA+bTCRRaQjIZlclABABsSyAE6coKkPDb71xg/PMMd4kPFhf4IBtdnTI9UB4is5QPt0Jyjx2uJMAP7qCKQfO89oqP/rev/EuX8gWZc3eVD3+//FRP2ywiOgl9r+uuxS5YV+3nUiTgF5fP5+CW3jASzU8BKBIVwn/TXUzTXLJU+mnhc5LnpDOny6iJEZxqsw4+GDDPzlLAfwfhVOcWZjbvVDgScxLxTeDEPtbxiJeKKyRTVJXLg/w1Kuz/XNR/jJt/6YI9P9YFa68QdHQRmBErlOLA8OCnDxQMPoQWFnPLkdG9fDXldwspXMVAGAAA==")

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func solid(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// PNG returns a w x h PNG of one colour.
func PNG(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, solid(w, h, color.RGBA{40, 120, 200, 255}))
	return b.Bytes()
}

// JPEG returns a w x h JPEG of one colour.
func JPEG(w, h int) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, solid(w, h, color.RGBA{200, 120, 40, 255}), nil)
	return b.Bytes()
}

// Fixture task ids.
const (
	DraftID    = "11111111-1111-4111-8111-111111111111" // geometry draft, v1 and v2
	GeometryID = "22222222-2222-4222-8222-222222222222" // geometry confirmed, mesh pack done
	MatDraftID = "33333333-3333-4333-8333-333333333333" // material draft
	PackedID   = "44444444-4444-4444-8444-444444444444" // textured and packed, two views
	RunningID  = "55555555-5555-4555-8555-555555555555" // geometry draft being generated
)

func baseSettings(tier string, labels []any, weights []any) map[string]any {
	return map[string]any{
		"view_weights": weights, "condition_mode": "concat", "image_label": labels, "height": 165.0,
		"allow_face_enhance": true, "default_face_enhance": false, "generation_mode": "Brilliant",
		"brilliant_mode": "gen-2.5-" + tier, "image_mode": "recon", "version": "v1", "type": "model_generate",
	}
}

func merge(ms ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var confirmSettings = map[string]any{"quality": "medium", "symmetric": true, "use_hyper": true, "mesh_export_type": "Raw", "mesh_smooth": false,
	"mesh_simplify": true, "quality_override": 500000.0, "quad_normal": false, "model_multi_udim": []any{}}

var textureSettings = map[string]any{"escore": 7.0, "face_enhance": false, "superres_texture": false, "sharp_texture": false,
	"texture_delight": false, "texture_mode": "high", "reference_image": "ALL", "seed": 1183.0}

// Standard returns a fake with one task per stage, the files they point
// at, and a running task that turns into a draft after two card reads.
func Standard() *Server {
	s := New()
	poster := WebP
	s.AddTask(&Task{
		UUID: DraftID, Prompt: "A ceramic owl figurine. sharp edges, complex geometry", Created: "2026-10-20T10:00:00.000Z",
		Step: "ModelGenerate", UserState: "DonePreview", Private: true,
		Settings: baseSettings("high", []any{"F"}, []any{1.0}), Inputs: []string{"0000.webp"},
		Poster: map[string]string{"preview_image": "draft.webp"}, Preview: map[string]string{"preview_sdf": "draft.drc"},
		GeoVersions: []string{"v1", "v2"},
	})
	s.Tasks(DraftID).Settings["version"] = "v2"
	s.AddTask(&Task{
		UUID: GeometryID, Prompt: "A hockey helmet", Created: "2026-10-15T10:00:00.000Z",
		Step: "ModelRefine", UserState: "DoneBasic",
		Settings: merge(baseSettings("high", []any{"?"}, []any{1.0}), confirmSettings, map[string]any{"geometry": "Base", "geometry_file_format": "obj"}),
		Inputs:   []string{"0000.webp"},
		Poster:   map[string]string{"preview_image": "geo-poster.webp", "geometry_image": "geometry.webp", "geometry_video": "geometry.mp4", "render_video": "render.mp4"},
		Preview:  map[string]string{"model": "model.glb", "preview_sdf": "geo.drc"},
		Confirmed: map[string]any{"model_version": "v1", "model_confirm_at": "2026-10-15T10:10:00.000Z"},
		GeoVersions: []string{"v1"}, Pack: []string{"base.obj"},
	})
	s.AddTask(&Task{
		UUID: MatDraftID, Prompt: "A wooden dog", Created: "2026-10-10T10:00:00.000Z",
		Step: "TextureGenerate", UserState: "DonePreview",
		Settings: merge(baseSettings("high", []any{"F"}, []any{1.0}), confirmSettings, textureSettings, map[string]any{"reference_image": "0000.webp"}),
		Inputs:   []string{"0000.webp"},
		Poster:   map[string]string{"preview_image": "mat-poster.webp", "geometry_image": "geometry.webp"},
		Preview:  map[string]string{"model": "model.glb"},
		Confirmed: map[string]any{"model_version": "v1", "model_confirm_at": "2026-10-10T10:10:00.000Z"},
		GeoVersions: []string{"v1"}, MatVersions: map[string][]string{"0000.webp": {"v1", "v2"}},
	})
	s.AddTask(&Task{
		UUID: PackedID, Prompt: "A human figure in a red coat, standing", Created: "2026-10-05T10:00:00.000Z",
		Step: "TextureRefine", UserState: "DoneBasic",
		Settings: merge(baseSettings("high", []any{"F", "B"}, []any{0.5, 0.5}), confirmSettings, textureSettings,
			map[string]any{"geometry": "Base", "geometry_file_format": "obj", "material": "All", "resolution": "Basic",
				"voxel_condition": string(bytes.Repeat([]byte("A"), 500))}),
		Inputs: []string{"0000.webp", "0001.webp"},
		Poster: map[string]string{"preview_image": "packed-poster.webp", "geometry_image": "geometry.webp", "material_image": "material.webp", "material_video": "material.mp4"},
		Preview: map[string]string{"model": "model.glb", "texture_diffuse": "diffuse.jpg", "texture_pbr": "pbr.jpg",
			"texture_normal": "normal.jpg", "texture_shaded": "shaded.jpg"},
		Confirmed: map[string]any{"model_version": "v1", "model_confirm_at": "2026-10-05T10:10:00.000Z", "texture_version": "v1",
			"texture_confirm_at": "2026-10-05T10:20:00.000Z", "texture_superres": false},
		GeoVersions: []string{"v1"}, MatVersions: map[string][]string{"ALL": {"v1"}},
		Pack: []string{"base_basic_pbr.obj", "base_basic_shaded.obj", "texture_diffuse.png"},
	})
	s.AddTask(&Task{
		UUID: RunningID, Prompt: "A robot", Created: "2026-10-25T10:00:00.000Z",
		Step: "ModelGenerate", UserState: "Generating", Pending: 1,
		Settings: baseSettings("extreme-low", []any{"?"}, []any{1.0}),
		Inputs:   []string{"0000.webp"},
		Poster:   map[string]string{"preview_image": "run.webp"},
		Next:     []State{{Step: "ModelGenerate", UserState: "Generating", Pending: 1}, {Step: "ModelGenerate", UserState: "DonePreview"}},
		GeoVersions: []string{"v1"},
	})
	for _, id := range []string{DraftID, GeometryID, MatDraftID, PackedID, RunningID} {
		for _, f := range []string{"draft.webp", "geo-poster.webp", "geometry.webp", "mat-poster.webp", "packed-poster.webp", "material.webp", "run.webp",
			"geo-v1.webp", "geo-v2.webp"} {
			s.SetFile(id+"/"+f, poster)
		}
		for _, f := range []string{"diffuse.jpg", "pbr.jpg", "normal.jpg", "shaded.jpg", "mat-v1.jpg", "mat-v2.jpg",
			"diffuse-v1.jpg", "pbr-v1.jpg", "normal-v1.jpg", "shaded-v1.jpg", "diffuse-v2.jpg", "pbr-v2.jpg", "normal-v2.jpg", "shaded-v2.jpg"} {
			s.SetFile(id+"/"+f, JPEG(96, 96))
		}
		s.SetFile(id+"/geometry.mp4", []byte("fake mp4 geometry"))
		s.SetFile(id+"/material.mp4", []byte("fake mp4 material"))
	}
	s.SetFile(GeometryID+"/pack/base.obj", []byte("o base\nv 0 0 0\n"))
	s.SetFile(PackedID+"/pack/base_basic_pbr.obj", []byte("o pbr\nv 0 0 0\n"))
	s.SetFile(PackedID+"/pack/base_basic_shaded.obj", []byte("o shaded\nv 0 0 0\n"))
	s.SetFile(PackedID+"/pack/texture_diffuse.png", PNG(32, 32))
	return s
}

// Tasks returns a registered task for editing in a test.
func (s *Server) Tasks(id string) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id]
}
