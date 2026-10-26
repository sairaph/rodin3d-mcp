package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// DownloadInput is the input of download_model.
type DownloadInput struct {
	TaskID    string
	OutputDir string
	Files     []string // names from get_task files.pack or files.videos; empty: the whole pack
}

type downloadFront struct {
	Task  TaskFront   `yaml:"task"`
	Dir   string      `yaml:"dir"`
	Files []fileFront `yaml:"files"`
}

// maxDownload bounds one downloaded file.
const maxDownload = 4 << 30

// Download is download_model: save the files of a finished pack (and, when
// named, the stage videos) to a folder.
func (s *Service) Download(ctx context.Context, in DownloadInput) (Result, error) {
	uuid, err := s.resolveTask(in.TaskID)
	if err != nil {
		return Result{}, err
	}
	card, err := s.client.Card(ctx, uuid)
	if err != nil {
		return Result{}, err
	}
	front := downloadFront{Task: s.taskFront(card)}

	type item struct{ name, url string }
	var pack []item
	items, err := s.client.GetDownload(ctx, uuid)
	var se *rodin.ServiceError
	switch {
	case errors.As(err, &se) && se.Code == "INVALID_REQUEST":
		// No pack yet.
	case err != nil:
		return Result{}, err
	}
	for _, it := range items {
		if it.Name != "" && it.URL != "" {
			pack = append(pack, item{safeName(it.Name), it.URL})
		}
	}
	videos := map[string]item{}
	for _, k := range videoNames(card) {
		u := card.Poster[k]
		ext := strings.ToLower(path.Ext(strings.SplitN(u, "?", 2)[0]))
		if ext == "" {
			ext = ".mp4"
		}
		videos[k] = item{k + ext, u}
	}

	var chosen []item
	if len(in.Files) == 0 {
		if len(pack) == 0 {
			return Result{}, notPacked(front.Task)
		}
		chosen = pack
	} else {
		for _, want := range in.Files {
			want = strings.TrimSpace(want)
			found := false
			for _, p := range pack {
				if p.name == want {
					chosen, found = append(chosen, p), true
					break
				}
			}
			if v, ok := videos[want]; ok && !found {
				chosen, found = append(chosen, v), true
			}
			if !found {
				var names []string
				for _, p := range pack {
					names = append(names, p.name)
				}
				for k := range videos {
					names = append(names, k)
				}
				if len(names) == 0 {
					return Result{}, notPacked(front.Task)
				}
				return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("The task has no file %q.", want),
					"Call download_model with files from: "+strings.Join(names, ", ")+" (or without files for the whole pack).")
			}
		}
	}

	dir := in.OutputDir
	if dir == "" {
		dir = front.Task.FilesDir
	} else if !filepath.IsAbs(dir) {
		return Result{}, fail(render.CodeInvalidInput, "output_dir must be an absolute path.", "Call download_model with an absolute output_dir, or without it for "+front.Task.FilesDir+".")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, wrapf(err, "create %s", dir)
	}
	front.Dir = dir
	st, _ := s.Store()
	for _, it := range chosen {
		target := filepath.Join(dir, it.name)
		n, sum, err := s.fetchTo(ctx, it.url, target)
		if err != nil {
			return Result{}, wrapf(err, "download %s", it.name)
		}
		front.Files = append(front.Files, fileFront{Name: it.name, Path: target, Bytes: n})
		if st != nil {
			_ = st.RecordFile(store.File{UUID: uuid, Kind: "download", Path: target, Bytes: n, SHA256: sum})
		}
	}
	var total int64
	for _, f := range front.Files {
		total += f.Bytes
	}
	body := fmt.Sprintf("Saved %d file(s), %s, to %s.\n\nNext: open the files from dir; get_preview shows the task's images.", len(front.Files), sizeText(total), dir)
	return Result{Front: front, Body: body}, nil
}

func notPacked(t TaskFront) error {
	st := stage.Status{Stage: t.Stage, State: t.State, Step: t.Step, UserState: t.UserState}
	where := describeStage(st)
	pack := "pack it in the Hyper3D web app (Download panel)"
	if registered("export_model") {
		pack = "pack it with export_model"
	}
	var msg, hint string
	switch {
	case t.State == stage.Packed:
		msg = fmt.Sprintf("Hyper3D lists no files for the pack of this task (%s), so there is nothing to download.", where)
		hint = "Pack it again (" + pack + "), then call download_model."
	case t.State == stage.Running:
		msg = fmt.Sprintf("A job is still working on this task (%s); it has no finished pack yet.", where)
		hint = "Call wait_task until the task is ready, then " + pack + " and call download_model."
	case t.Stage == stage.GeometryDraft:
		msg = "The task is a geometry draft: files can be downloaded only after the geometry is confirmed and packed."
		hint = "Confirm the geometry, " + pack + ", then call download_model."
	default:
		msg = fmt.Sprintf("The task (%s) has no finished pack yet.", where)
		hint = strings.ToUpper(pack[:1]) + pack[1:] + ", then call download_model again."
	}
	return fail(render.CodeConflict, msg, hint)
}

// fetchTo downloads a signed URL into target through a temporary file and
// returns its size and SHA-256.
func (s *Service) fetchTo(ctx context.Context, signedURL, target string) (int64, string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return 0, "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, _, err := s.client.Fetch(ctx, signedURL, io.MultiWriter(tmp, h), maxDownload)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, "", err
	}
	if err := domain.ReplaceFile(tmp.Name(), target); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// safeName keeps a server file name to its base and harmless characters.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r < 32:
			return '_'
		}
		return r
	}, name)
	if name == "." || name == ".." || name == "" {
		return "file"
	}
	return name
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
