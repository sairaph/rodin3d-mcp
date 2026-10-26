package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/modelfile"
	"github.com/sairaph/rodin3d-mcp/internal/render3d"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Helpers the tools of the other Rodin modes share: following scheduler jobs
// whose task card does not change, saving a result file next to the task's
// downloads, and drawing a local model file.

// JobPoll is how often waitJobs asks the scheduler; tests shorten it.
var JobPoll = 2 * time.Second

// jobsResult is the end of waitJobs.
type jobsResult struct {
	Status   string // Succeeded, Failed or Canceled when done; the last status read otherwise
	Done     bool
	TimedOut bool
}

// waitJobs follows scheduler jobs until every one ended or deadline passes.
// An unknown job reads as Canceled (notes 14 section 3.4), and a job may
// not be known yet right after its request, so Canceled counts only once a
// job was seen queued or running, or after it read Canceled on three polls in
// a row. Any Failed job makes the result Failed; else any Canceled makes it
// Canceled.
func (s *Service) waitJobs(ctx context.Context, jobs []string, deadline time.Time) (jobsResult, error) {
	if len(jobs) == 0 {
		return jobsResult{}, errors.New("the reply named no scheduler job to follow")
	}
	seen := map[string]bool{}
	canceled := map[string]int{}
	for {
		var res jobsResult
		res.Done = true
		failed, cancel := false, false
		for _, j := range jobs {
			st, err := s.client.JobStatus(ctx, j)
			if err != nil {
				return jobsResult{}, err
			}
			res.Status = st
			switch st {
			case "Succeeded":
			case "Failed":
				failed = true
			case "Canceled":
				canceled[j]++
				if !seen[j] && canceled[j] < 3 {
					res.Done = false
				} else {
					cancel = true
				}
			default: // Queued, Running, BackPressure, WaitingForDependencies
				seen[j] = true
				canceled[j] = 0
				res.Done = false
			}
		}
		if res.Done {
			switch {
			case failed:
				res.Status = "Failed"
			case cancel:
				res.Status = "Canceled"
			default:
				res.Status = "Succeeded"
			}
			return res, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			res.TimedOut = true
			return res, nil
		}
		select {
		case <-ctx.Done():
			return jobsResult{}, ctx.Err()
		case <-time.After(min(JobPoll, left)):
		}
	}
}

// saveResultFile downloads a result file of task uuid (a signed address) to
// target, as download_model does, and records it, so get_task lists it with
// the downloaded files.
func (s *Service) saveResultFile(ctx context.Context, uuid, signedURL, target string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	n, sha, err := s.fetchTo(ctx, signedURL, target)
	if err != nil {
		return 0, err
	}
	if st, err := s.Store(); err == nil {
		_ = st.RecordFile(store.File{UUID: uuid, Kind: "download", Path: target, Bytes: n, SHA256: sha})
	}
	return n, nil
}

// renderModelFile draws a local model file (glb, gltf, obj, stl) in mode
// from view as a size x size PNG, in the render slot get_preview uses.
func (s *Service) renderModelFile(ctx context.Context, path string, mode render3d.Mode, view render3d.View, size int) ([]byte, error) {
	release, err := s.r3d.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := modelfile.Load(ctx, path, s.decoder)
	if err != nil {
		return nil, err
	}
	img, _, err := render3d.Render(m.Mesh, m.Maps, render3d.Options{Mode: mode, View: view, Size: size, Cancel: ctx.Done()})
	if err != nil {
		return nil, err
	}
	defer debug.FreeOSMemory()
	return encodeRender(img)
}
