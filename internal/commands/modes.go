package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// The commands of the other Rodin modes: the same service methods as the
// MCP tools generate_agentic, split_parts, rig_model, animate_model,
// list_animation_presets and texture_model.

// absPaths turns command line paths into absolute ones.
func absPaths(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, p := range list {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		out = append(out, abs)
	}
	return out, nil
}

// readText reads a text file named on the command line.
func readText(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", usage("cannot read " + abs + ": " + err.Error())
	}
	return string(b), nil
}

// listOpt splits a comma-separated option.
func listOpt(a args, key string) []string {
	var out []string
	for _, v := range strings.Split(a.opts[key], ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func registerModes(reg *command.Registry, run func(func(context.Context, *service.Service, args) (service.Result, error)) func(context.Context, []string) int) {
	reg.Register(command.Handler{Name: "agentic", Description: "Start an Agentic build from 1-5 images (paid)",
		Usage: "agentic <image>... budget=basic|standard|pro [prompt=..] [ngons=false] [public=true] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.AgenticInput{Wait: waitOf(&a), Budget: a.opts["budget"], Prompt: a.opts["prompt"]}
			var err error
			if in.Images, err = absPaths(a.pos); err != nil {
				return service.Result{}, err
			}
			var e1, e2, e3 error
			in.Expected, e1 = floatOpt(a, "expected")
			ngons, e2 := boolOpt(a, "ngons")
			public, e3 := boolOpt(a, "public")
			if err := optErrs(e1, e2, e3); err != nil {
				return service.Result{}, err
			}
			in.NGons = ngons == nil || *ngons
			in.Public = public != nil && *public
			return s.GenerateAgentic(ctx, in)
		})})

	reg.Register(command.Handler{Name: "split-parts", Description: "Split a geometry draft into parts (paid)",
		Usage: "split-parts <id> [version=vN] [strength=1-12] [step=both|plan|generate] [bang_version=.. part=..] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.SplitInput{Wait: waitOf(&a), TaskID: a.arg(0), Version: a.opts["version"], Step: a.opts["step"],
				BangVersion: a.opts["bang_version"], Part: a.opts["part"]}
			var e1, e2 error
			in.Expected, e1 = floatOpt(a, "expected")
			in.Strength, e2 = intOpt(a, "strength", 0)
			if err := optErrs(e1, e2); err != nil {
				return service.Result{}, err
			}
			return s.SplitParts(ctx, in)
		})})

	reg.Register(command.Handler{Name: "rig", Description: "Give a confirmed model a skeleton and skin (paid)",
		Usage: "rig <id> [template=human|quadruped|bird|dragon|kaiju | skeleton=<file> | description=.. | fbx=<file> [reskin]] [skinning=shell|core] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.RigInput{Wait: waitOf(&a), TaskID: a.arg(0), Template: a.opts["template"], Description: a.opts["description"],
				Skinning: a.opts["skinning"]}
			if f := a.opts["skeleton"]; f != "" {
				text, err := readText(f)
				if err != nil {
					return service.Result{}, err
				}
				in.Skeleton = text
			}
			if f := a.opts["fbx"]; f != "" {
				abs, err := filepath.Abs(f)
				if err != nil {
					return service.Result{}, err
				}
				in.FBXSkeleton = abs
			}
			var e1, e2 error
			in.Expected, e1 = floatOpt(a, "expected")
			reskin, e2 := boolOpt(a, "reskin")
			if err := optErrs(e1, e2); err != nil {
				return service.Result{}, err
			}
			in.Reskin = reskin != nil && *reskin
			return s.RigModel(ctx, in)
		})})

	reg.Register(command.Handler{Name: "animate", Description: "Export a rigged model with animations (paid)",
		Usage: "animate <id> [presets=human/Walking,...] [files=<fbx>,...] [format=glb|fbx|usdz] [resolution=2k] [material=all] [height=N] [align_ground=true] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.AnimateInput{Wait: waitOf(&a), TaskID: a.arg(0), Presets: listOpt(a, "presets"), Format: a.opts["format"],
				Resolution: a.opts["resolution"], Material: a.opts["material"]}
			var err error
			if in.Files, err = absPaths(listOpt(a, "files")); err != nil {
				return service.Result{}, err
			}
			var e1, e2, e3 error
			in.Expected, e1 = floatOpt(a, "expected")
			in.Height, e2 = floatOpt(a, "height")
			in.AlignGround, e3 = boolOpt(a, "align_ground")
			if err := optErrs(e1, e2, e3); err != nil {
				return service.Result{}, err
			}
			return s.AnimateModel(ctx, in)
		})})

	reg.Register(command.Handler{Name: "animation-presets", Description: "List the animation presets",
		Usage: "animation-presets [category] [template=..] [json]",
		Run: run(func(_ context.Context, s *service.Service, a args) (service.Result, error) {
			return s.AnimationPresets(service.AnimationPresetsInput{Category: a.arg(0), Template: a.opts["template"]})
		})})

	reg.Register(command.Handler{Name: "texture-model", Description: "Start a 3D Editing task from your own model file (paid)",
		Usage: "texture-model <model file> [<image>...] [model=gen-2.5|gen-1.5] [mode=..] [prompt=..] [public=true] expected=<credits> [nowait] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.TextureModelInput{Wait: waitOf(&a), Model: a.opts["model"], Mode: a.opts["mode"], Prompt: a.opts["prompt"]}
			paths, err := absPaths(a.pos)
			if err != nil {
				return service.Result{}, err
			}
			if len(paths) == 0 {
				return service.Result{}, usage("name the model file, e.g. texture-model chair.glb")
			}
			in.ModelPath, in.Images = paths[0], paths[1:]
			var e1, e2 error
			in.Expected, e1 = floatOpt(a, "expected")
			public, e2 := boolOpt(a, "public")
			if err := optErrs(e1, e2); err != nil {
				return service.Result{}, err
			}
			in.Public = public != nil && *public
			return s.TextureModel(ctx, in)
		})})
}
