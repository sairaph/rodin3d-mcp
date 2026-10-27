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

// Kinds of the calls recordJobs keeps: their jobs run on the scheduler while
// the task card shows nothing (notes 49: the card does not change while
// rigging; a Bang plan or split leaves the draft a draft).
const (
	jobRig    = "rig"           // rodin_rigging; key: the rig source
	jobReskin = "reskin"        // the second Import FBX rodin_rigging; key: the rig source
	jobPlan   = "bang_plan"     // key: the geometry version
	jobSplit  = "bang_generate" // key: the geometry version
)

// jobFresh is how long after its request a job that reads Canceled may
// still be one the scheduler does not know yet (waitJobs: three polls).
const jobFresh = 10 * time.Second

// recordJobs keeps the scheduler jobs of a call the card does not show, so
// get_task and later calls follow them; 0 when they cannot be kept.
func (s *Service) recordJobs(uuid, kind, key string, detail map[string]string, jobs []string) int64 {
	st, err := s.Store()
	if err != nil || len(jobs) == 0 {
		return 0
	}
	id, err := st.AddJob(store.Job{UUID: uuid, Kind: kind, Key: key, Detail: detail, Jobs: jobs})
	if err != nil {
		return 0
	}
	return id
}

// endJobs records the end waitJobs read; jobs still running stay running.
func (s *Service) endJobs(id int64, jr jobsResult) {
	if id == 0 || !jr.Done {
		return
	}
	if st, err := s.Store(); err == nil {
		_ = st.EndJob(id, jobWord(jr))
	}
}

// latestJob is the newest call of kind this tool recorded on task uuid for
// key ("" any key).
func (s *Service) latestJob(uuid, kind, key string) (store.Job, bool) {
	st, err := s.Store()
	if err != nil {
		return store.Job{}, false
	}
	j, err := st.LatestJob(uuid, kind, key)
	return j, err == nil
}

// jobState is the state of a recorded call: its recorded end, else one read
// of the scheduler (running, succeeded, failed or canceled), which is then
// recorded when it is an end. Any job not ended makes it running.
func (s *Service) jobState(ctx context.Context, j store.Job) (string, error) {
	if j.State != store.JobRunning {
		return j.State, nil
	}
	failed, canceled := false, false
	for _, id := range j.Jobs {
		st, err := s.client.JobStatus(ctx, id)
		if err != nil {
			return "", err
		}
		switch st {
		case "Succeeded":
		case "Failed":
			failed = true
		case "Canceled":
			// An unknown job reads Canceled, and a job may be unknown for a
			// moment after its request.
			if time.Since(j.At) < jobFresh {
				return "running", nil
			}
			canceled = true
		default: // Queued, Running, BackPressure, WaitingForDependencies
			return "running", nil
		}
	}
	state := "succeeded"
	switch {
	case failed:
		state = "failed"
	case canceled:
		state = "canceled"
	}
	if st, err := s.Store(); err == nil {
		_ = st.EndJob(j.ID, state)
	}
	return state, nil
}

// sentAt is the local time a recorded call was sent, for the texts.
func sentAt(j store.Job) string { return j.At.Format("15:04:05") }

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
