package commands

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// The staged-flow commands: the same service methods as the MCP tools.
// expected=<credits> is the quote_cost price (-1 for a price that is not
// known); nowait returns right after the request.

func floatOpt(a args, key string) (*float64, error) {
	v, ok := a.opts[key]
	if !ok || v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, usage(key + " must be a number")
	}
	return &f, nil
}

func boolOpt(a args, key string) (*bool, error) {
	v, ok := a.opts[key]
	if !ok || v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, usage(key + " must be true or false")
	}
	return &b, nil
}

func intPtrOpt(a args, key string) (*int, error) {
	v, ok := a.opts[key]
	if !ok || v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, usage(key + " must be a whole number")
	}
	return &n, nil
}

// waitOf is false when the word nowait is among the positional arguments,
// which it then leaves out.
func waitOf(a *args) bool {
	wait := true
	var keep []string
	for _, p := range a.pos {
		if strings.EqualFold(p, "nowait") {
			wait = false
			continue
		}
		keep = append(keep, p)
	}
	a.pos = keep
	return wait
}

// imageSpec is <path>[:VIEW[:weight]]; a Windows drive colon stays in the
// path.
var imageSpec = regexp.MustCompile(`^(.+?)(?::(F|FL|FR|B|BL|BR|L|R|U|D|\?))?(?::(\d*\.?\d+))?$`)

func parseImage(spec string) (service.GenerateImage, error) {
	m := imageSpec.FindStringSubmatch(spec)
	if m == nil {
		return service.GenerateImage{}, usage("image " + strconv.Quote(spec) + " is not <path>[:VIEW[:weight]]")
	}
	abs, err := filepath.Abs(m[1])
	if err != nil {
		return service.GenerateImage{}, err
	}
	img := service.GenerateImage{Path: abs, View: m[2]}
	if m[3] != "" {
		w, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			return service.GenerateImage{}, usage("weight of " + strconv.Quote(spec) + " is not a number")
		}
		img.Weight = &w
	}
	return img, nil
}

// optErrs returns the first error of a list of option reads.
func optErrs(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func registerFlow(reg *command.Registry, run func(func(context.Context, *service.Service, args) (service.Result, error)) func(context.Context, []string) int) {
	reg.Register(command.Handler{Name: "generate", Description: "Submit a geometry draft from 1-5 images (paid)",
		Usage: "generate <image>[:VIEW[:weight]]... | input_set=<id> [tier=..] [model=..] [mode=..] [prompt=..] [image_mode=..] [face_enhance=..] [seed=N] [copy=<id>] [public=true] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.GenerateInput{Wait: waitOf(&a), Model: a.opts["model"], Tier: a.opts["tier"], Mode: a.opts["mode"], Prompt: a.opts["prompt"],
				ImageMode: a.opts["image_mode"], CopySettingsFrom: a.opts["copy"], InputSet: a.opts["input_set"]}
			for _, p := range a.pos {
				img, err := parseImage(p)
				if err != nil {
					return service.Result{}, err
				}
				in.Images = append(in.Images, img)
			}
			var e1, e2, e3, e4, e5 error
			in.Expected, e1 = floatOpt(a, "expected")
			in.FaceEnhance, e2 = boolOpt(a, "face_enhance")
			seed, e3 := intPtrOpt(a, "seed")
			batch, e4 := intOpt(a, "batch", 0)
			public, e5 := boolOpt(a, "public")
			if err := optErrs(e1, e2, e3, e4, e5); err != nil {
				return service.Result{}, err
			}
			if seed != nil {
				in.Advanced = &service.Advanced{Seed: seed}
			}
			in.Batch = batch
			in.Public = public != nil && *public
			return s.Generate(ctx, in)
		})})

	reg.Register(command.Handler{Name: "redo-geometry", Description: "Redo a geometry draft (free below extreme-high)",
		Usage: "redo-geometry <id> [prompt=..] [seed=N] [expected=<credits>] [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.RedoGeometryInput{Wait: waitOf(&a), TaskID: a.arg(0), Prompt: a.opts["prompt"]}
			var e1, e2 error
			in.Expected, e1 = floatOpt(a, "expected")
			seed, e2 := intPtrOpt(a, "seed")
			if err := optErrs(e1, e2); err != nil {
				return service.Result{}, err
			}
			if seed != nil {
				in.Advanced = &service.Advanced{Seed: seed}
			}
			return s.RedoGeometry(ctx, in)
		})})

	reg.Register(command.Handler{Name: "confirm-geometry", Description: "Confirm a geometry draft (paid)",
		Usage: "confirm-geometry <id> [mesh=raw|quad] [faces=N] [quality=..] [smart_low_poly=true] [bake_normal=true] [symmetric=false] [smooth=true] [version=vN] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ConfirmGeometryInput{Wait: waitOf(&a), TaskID: a.arg(0), Mesh: a.opts["mesh"], Quality: a.opts["quality"], Version: a.opts["version"]}
			var errs [7]error
			in.Expected, errs[0] = floatOpt(a, "expected")
			in.Faces, errs[1] = intPtrOpt(a, "faces")
			smart, err := boolOpt(a, "smart_low_poly")
			errs[2] = err
			in.BakeNormal, errs[3] = boolOpt(a, "bake_normal")
			in.Symmetric, errs[4] = boolOpt(a, "symmetric")
			in.Smooth, errs[5] = boolOpt(a, "smooth")
			in.Hyper, errs[6] = boolOpt(a, "hyper")
			if err := optErrs(errs[:]...); err != nil {
				return service.Result{}, err
			}
			in.SmartLowPoly = smart != nil && *smart
			return s.ConfirmGeometry(ctx, in)
		})})

	texture := func(a *args) (service.TextureInput, error) {
		in := service.TextureInput{Wait: waitOf(a), TaskID: a.arg(0), TextureMode: a.opts["texture_mode"], ReferenceImage: a.opts["reference"],
			Prompt: a.opts["prompt"], Seed: a.opts["seed"]}
		var errs [7]error
		in.Expected, errs[0] = floatOpt(*a, "expected")
		in.HD, errs[1] = boolOpt(*a, "hd_texture")
		in.Sharp, errs[2] = boolOpt(*a, "sharp")
		in.Delight, errs[3] = boolOpt(*a, "delight")
		in.FaceEnhance, errs[4] = boolOpt(*a, "face_enhance")
		in.Escore, errs[5] = floatOpt(*a, "escore")
		in.ReferenceScale, errs[6] = floatOpt(*a, "reference_scale")
		if ref := in.ReferenceImage; ref != "" && ref != "ALL" && strings.ContainsAny(ref, `/\`) {
			abs, err := filepath.Abs(ref)
			if err != nil {
				return in, err
			}
			in.ReferenceImage = abs
		}
		return in, optErrs(errs[:]...)
	}
	reg.Register(command.Handler{Name: "texture", Description: "Generate the material draft of a confirmed geometry (paid)",
		Usage: "texture <id> [texture_mode=..] [hd_texture=true] [sharp=true] [delight=true] [face_enhance=true] [escore=7] [reference=<name|ALL|path>] [prompt=..] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in, err := texture(&a)
			if err != nil {
				return service.Result{}, err
			}
			return s.GenerateTexture(ctx, in)
		})})
	reg.Register(command.Handler{Name: "redo-material", Description: "Redo a material draft (quote redo_material for its price)",
		Usage: "redo-material <id> [texture_mode=..] [hd_texture=..] [sharp=..] [delight=..] [face_enhance=..] [escore=..] [seed=keep|new] [prompt=..] [expected=<credits>] [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in, err := texture(&a)
			if err != nil {
				return service.Result{}, err
			}
			return s.RedoMaterial(ctx, in)
		})})

	reg.Register(command.Handler{Name: "confirm-material", Description: "Confirm a material draft (paid)",
		Usage: "confirm-material <id> [version=vN] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ConfirmMaterialInput{Wait: waitOf(&a), TaskID: a.arg(0), Version: a.opts["version"]}
			var err error
			if in.Expected, err = floatOpt(a, "expected"); err != nil {
				return service.Result{}, err
			}
			return s.ConfirmMaterial(ctx, in)
		})})

	reg.Register(command.Handler{Name: "export", Description: "Pack a task's files for download (paid)",
		Usage: "export <id> [format=obj] [resolution=2k] [geometry=base] [material=all] [align_ground=true] [height=N] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ExportInput{Wait: waitOf(&a), TaskID: a.arg(0), Type: a.opts["type"], Format: a.opts["format"], Resolution: a.opts["resolution"],
				Geometry: a.opts["geometry"], Material: a.opts["material"]}
			var errs [3]error
			in.Expected, errs[0] = floatOpt(a, "expected")
			in.AlignGround, errs[1] = boolOpt(a, "align_ground")
			in.Height, errs[2] = floatOpt(a, "height")
			if err := optErrs(errs[:]...); err != nil {
				return service.Result{}, err
			}
			return s.Export(ctx, in)
		})})

	reg.Register(command.Handler{Name: "fork", Description: "Branch a new task from a task's geometry or material (paid)",
		Usage: "fork <id> geometry|material [public=true] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ForkInput{Wait: waitOf(&a), TaskID: a.arg(0), Stage: a.arg(1)}
			var e1, e2 error
			in.Expected, e1 = floatOpt(a, "expected")
			public, e2 := boolOpt(a, "public")
			if err := optErrs(e1, e2); err != nil {
				return service.Result{}, err
			}
			in.Public = public != nil && *public
			return s.Fork(ctx, in)
		})})

	reg.Register(command.Handler{Name: "set-private", Description: "Make a task private, or public with private=false (free)",
		Usage: "set-private <id> [private=false] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			private, err := boolOpt(a, "private")
			if err != nil {
				return service.Result{}, err
			}
			return s.SetPrivate(ctx, service.SetPrivateInput{TaskID: a.arg(0), Private: private == nil || *private})
		})})

	reg.Register(command.Handler{Name: "select-candidate", Description: "Keep one draft of a batch",
		Usage: "select-candidate <id> <version> [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			return s.SelectCandidate(ctx, service.SelectInput{TaskID: a.arg(0), Candidate: a.arg(1)})
		})})
}
