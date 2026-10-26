package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sairaph/mcp-wizard/command"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/service"
	"github.com/sairaph/rodin3d-mcp/internal/viewer"
)

// The viewer commands: the same service methods as the viewer tools.
// shot=<file.png> saves the screenshot there (viewer-state also takes the
// .png file as a word).

// extraCommands are registered by build-tagged files (viewertest).
var extraCommands []func(reg *command.Registry, env Env)

// shotPath reads shot=<file>, and with words a .png word (taken out of
// a.pos).
func shotPath(a *args, words bool) (string, error) {
	path := a.opts["shot"]
	var keep []string
	for _, p := range a.pos {
		if words && strings.HasSuffix(strings.ToLower(p), ".png") && path == "" {
			path = p
			continue
		}
		keep = append(keep, p)
	}
	a.pos = keep
	if path == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

// saveShot writes the result's image to path and notes it in the body.
func saveShot(res *service.Result, path string) error {
	if path == "" || len(res.Images) == 0 {
		return nil
	}
	if err := domain.WriteFileAtomic(path, res.Images[0].PNG, 0o644); err != nil {
		return err
	}
	res.Body += "\n\nScreenshot saved to " + path + "."
	return nil
}

// taskRef reads <id>[:stage[:version]].
func taskRef(s string) viewer.TaskRef {
	parts := strings.SplitN(s, ":", 3)
	r := viewer.TaskRef{TaskID: parts[0]}
	if len(parts) > 1 {
		r.Stage = parts[1]
	}
	if len(parts) > 2 {
		r.Version = parts[2]
	}
	return r
}

// contentArgs reads show content: task <id> | compare <id>[:stage] <id>[:stage]
// | inputs <task> | input-set <id> | input-set <img>[:VIEW[:w]]... |
// textures <task> [maps=a,b] | model <file> | images <file>...
func contentArgs(a args) (viewer.Content, error) {
	var c viewer.Content
	kind, rest := a.arg(0), a.pos[min(1, len(a.pos)):]
	abs := func(p string) (string, error) { return filepath.Abs(p) }
	switch kind {
	case "task":
		if len(rest) != 1 {
			return c, usage("task takes one id")
		}
		r := taskRef(rest[0])
		if v := a.opts["stage"]; v != "" {
			r.Stage = v
		}
		if v := a.opts["version"]; v != "" {
			r.Version = v
		}
		c.Task = &r
	case "compare":
		if len(rest) != 2 {
			return c, usage("compare takes two ids, each <id>[:stage]")
		}
		c.Compare = []viewer.TaskRef{taskRef(rest[0]), taskRef(rest[1])}
	case "inputs":
		if len(rest) != 1 {
			return c, usage("inputs takes one task id")
		}
		c.TaskInputs = rest[0]
	case "input-set":
		if len(rest) == 0 {
			return c, usage("input-set takes an input set id or images <path>[:VIEW[:weight]]")
		}
		if len(rest) == 1 && strings.HasPrefix(rest[0], "is-") {
			c.InputSet = &viewer.InputSetRef{ID: rest[0]}
			break
		}
		set := &viewer.InputSetRef{}
		for _, spec := range rest {
			img, err := parseImage(spec)
			if err != nil {
				return c, err
			}
			set.Images = append(set.Images, viewer.InputImage{Path: img.Path, View: img.View, Weight: img.Weight})
		}
		c.InputSet = set
	case "textures":
		if len(rest) != 1 {
			return c, usage("textures takes one task id")
		}
		t := &viewer.TexturesRef{TaskID: rest[0]}
		if m := a.opts["maps"]; m != "" {
			t.Maps = strings.Split(m, ",")
		}
		c.Textures = t
	case "model":
		if len(rest) != 1 {
			return c, usage("model takes one file")
		}
		p, err := abs(rest[0])
		if err != nil {
			return c, err
		}
		c.ModelFile = p
	case "images":
		if len(rest) == 0 {
			return c, usage("images takes one or more files")
		}
		for _, f := range rest {
			p, err := abs(f)
			if err != nil {
				return c, err
			}
			c.Images = append(c.Images, p)
		}
	default:
		return c, usage("name what to show: task, compare, inputs, input-set, textures, model or images")
	}
	return c, nil
}

func int64Opt(a args, key string) (*int64, error) {
	v, ok := a.opts[key]
	if !ok || v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, usage(key + " must be a whole number")
	}
	return &n, nil
}

func strOpt(a args, key string) *string {
	v, ok := a.opts[key]
	if !ok {
		return nil
	}
	return &v
}

// controlArgs reads control-viewer key=value options.
func controlArgs(a args) (viewer.Control, error) {
	c := viewer.Control{Layout: strOpt(a, "layout"), Stage: strOpt(a, "stage"), Version: strOpt(a, "version"), Mode: strOpt(a, "mode"),
		View: strOpt(a, "view"), Background: strOpt(a, "background"), Message: strOpt(a, "message")}
	var err error
	for _, p := range a.pos {
		switch p {
		case "reset":
			c.ResetCamera = true
		case "unhighlight":
			c.ClearHighlight = true
		default:
			return c, usage("unknown word " + strconv.Quote(p) + " (options are key=value; words: reset, unhighlight)")
		}
	}
	az, err := floatOpt(a, "azimuth")
	if err != nil {
		return c, err
	}
	el, err := floatOpt(a, "elevation")
	if err != nil {
		return c, err
	}
	if az != nil || el != nil {
		c.Orbit = &viewer.Orbit{AzimuthDeg: az, ElevationDeg: el}
	}
	if c.Zoom, err = floatOpt(a, "zoom"); err != nil {
		return c, err
	}
	if v := a.opts["pan"]; v != "" {
		xs := strings.Split(v, ",")
		if len(xs) != 2 {
			return c, usage("pan is x,y")
		}
		x, e1 := strconv.ParseFloat(strings.TrimSpace(xs[0]), 64)
		y, e2 := strconv.ParseFloat(strings.TrimSpace(xs[1]), 64)
		if e1 != nil || e2 != nil {
			return c, usage("pan is x,y numbers")
		}
		c.Pan = &viewer.Pan{X: x, Y: y}
	}
	if c.Turntable, err = boolOpt(a, "turntable"); err != nil {
		return c, err
	}
	if c.WireframeOverlay, err = boolOpt(a, "wireframe"); err != nil {
		return c, err
	}
	sel := &viewer.Select{}
	if sel.ImageIndex, err = intPtrOpt(a, "select"); err != nil {
		return c, err
	}
	sel.TextureMap = strOpt(a, "texture")
	if sel.Pane, err = intPtrOpt(a, "pane"); err != nil {
		return c, err
	}
	if sel.ImageIndex != nil || sel.TextureMap != nil || sel.Pane != nil {
		c.Select = sel
	}
	if v := a.opts["tag"]; v != "" {
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			return c, usage("tag is <index>:<VIEW>[:weight]")
		}
		i, err := strconv.Atoi(parts[0])
		if err != nil {
			return c, usage("tag index must be a number")
		}
		t := &viewer.SetTag{ImageIndex: i, View: parts[1]}
		if len(parts) > 2 {
			w, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				return c, usage("tag weight must be a number")
			}
			t.Weight = &w
		}
		c.SetTag = t
	}
	if v := a.opts["reorder"]; v != "" {
		var from, to int
		if _, err := fmt.Sscanf(v, "%d:%d", &from, &to); err != nil {
			return c, usage("reorder is <from>:<to>")
		}
		c.Reorder = &viewer.Reorder{From: from, To: to}
	}
	if v := a.opts["highlight"]; v != "" {
		h := &viewer.Highlight{}
		if i, err := strconv.Atoi(v); err == nil {
			h.ImageIndex = &i
		} else {
			h.View = &v
		}
		c.Highlight = h
	}
	if c.ExpectedRevision, err = int64Opt(a, "expected"); err != nil {
		return c, err
	}
	return c, nil
}

func registerViewer(reg *command.Registry, env Env, run func(fn func(context.Context, *service.Service, args) (service.Result, error)) func(context.Context, []string) int) {
	withShot := func(words bool, fn func(context.Context, *service.Service, args) (service.Result, error)) func(context.Context, []string) int {
		return run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			shot, err := shotPath(&a, words)
			if err != nil {
				return service.Result{}, err
			}
			res, err := fn(ctx, s, a)
			if err != nil {
				return res, err
			}
			return res, saveShot(&res, shot)
		})
	}
	reg.Register(command.Handler{Name: "open-viewer", Description: "Open the viewer window (optionally showing content)",
		Usage: "open-viewer [task <id>[:stage[:version]] | compare <id> <id> | inputs <task> | input-set <id|img[:VIEW[:w]]...> | textures <task> | model <file> | images <file>...] [mode=..] [view=..] [shot=file.png] [json]",
		Run: withShot(false, func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ViewerOpenInput{Mode: a.opts["mode"], View: a.opts["view"]}
			if len(a.pos) > 0 {
				c, err := contentArgs(a)
				if err != nil {
					return service.Result{}, err
				}
				in.Content = &c
			}
			return s.OpenViewer(ctx, in)
		})})
	reg.Register(command.Handler{Name: "show-in-viewer", Description: "Show content in the open viewer window",
		Usage: "show-in-viewer <task <id>[:stage[:version]] | compare <id>[:stage] <id>[:stage] | inputs <task> | input-set <id|img[:VIEW[:w]]...> | textures <task> [maps=a,b] | model <file> | images <file>...> [mode=..] [view=..] [expected=N] [shot=file.png] [json]",
		Run: withShot(false, func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			c, err := contentArgs(a)
			if err != nil {
				return service.Result{}, err
			}
			exp, err := int64Opt(a, "expected")
			if err != nil {
				return service.Result{}, err
			}
			return s.ShowInViewer(ctx, viewer.ShowParams{Content: c, Mode: a.opts["mode"], View: a.opts["view"], ExpectedRevision: exp})
		})})
	reg.Register(command.Handler{Name: "control-viewer", Description: "Change the open viewer (mode, stage, camera, layout, tags, banner)",
		Usage: "control-viewer [layout=..] [stage=..] [version=..] [mode=..] [view=..] [reset] [azimuth=deg] [elevation=deg] [zoom=..] [pan=x,y] [turntable=true] [wireframe=true] [background=default|light|dark] [select=N] [texture=name] [pane=N] [tag=N:VIEW[:w]] [reorder=a:b] [highlight=N|VIEW] [unhighlight] [message=\"..\"] [expected=N] [json]",
		Run: run(func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			c, err := controlArgs(a)
			if err != nil {
				return service.Result{}, err
			}
			return s.ControlViewer(ctx, c)
		})})
	reg.Register(command.Handler{Name: "viewer-state", Description: "Show the viewer's state, the user's changes and a screenshot",
		Usage: "viewer-state [file.png] [size=N] [since=N] [noshot] [json]",
		Run: withShot(true, func(ctx context.Context, s *service.Service, a args) (service.Result, error) {
			in := service.ViewerStateInput{Screenshot: true}
			for _, p := range a.pos {
				if p == "noshot" {
					in.Screenshot = false
					continue
				}
				return service.Result{}, usage("unknown word " + strconv.Quote(p))
			}
			size, err := intOpt(a, "size", 0)
			if err != nil {
				return service.Result{}, err
			}
			in.Size = size
			if since, err := int64Opt(a, "since"); err != nil {
				return service.Result{}, err
			} else if since != nil {
				in.SinceRevision = *since
			}
			return s.ViewerState(ctx, in)
		})})
	reg.Register(command.Handler{Name: "close-viewer", Description: "Close the viewer window", Usage: "close-viewer [json]",
		Run: run(func(ctx context.Context, s *service.Service, _ args) (service.Result, error) {
			return s.CloseViewer(ctx)
		})})
	for _, extra := range extraCommands {
		extra(reg, env)
	}
}
