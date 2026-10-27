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

// jobFresh is how long after the reply that named it (the recorded call's
// At, set when its jobs are recorded) a job that reads Canceled may still be
// one the scheduler does not know yet (waitJobs: three polls).
const jobFresh = 10 * time.Second

// claim is a call the card does not show, recorded before it is sent
// (claimJob), so get_task and later calls follow it; id 0 when nothing is
// recorded (no store).
type claim struct {
	s  *Service
	id int64
}

// claimJob records a call of kind on task uuid and key about to be sent, in
// one store step with the check that no call of a kind in blocking runs on
// the same task and key (other than the recorded call except): two sessions
// cannot both pass a busy check and send. Every call of a blocking kind the
// store still marks running is read from the scheduler first and its end
// recorded, so only a call that really runs refuses (also an older one no
// other read follows, such as a plan sent before the newest plan). busy is
// the running call that refused it.
func (s *Service) claimJob(ctx context.Context, uuid, kind, key string, detail map[string]string, blocking []string, except int64) (c claim, busy *store.Job, err error) {
	c.s = s
	st, err := s.Store()
	if err != nil {
		return c, nil, nil // no store: nothing is recorded or followed
	}
	running, err := st.RunningJobs(uuid, key, blocking, except)
	if err != nil {
		return c, nil, wrapf(err, "read the recorded calls before sending the %s call", kind)
	}
	for _, j := range running {
		if _, err := s.jobState(ctx, j); err != nil {
			return c, nil, wrapf(err, "read the state of the %s call sent at %s", j.Kind, sentAt(j))
		}
	}
	id, blocker, err := st.ClaimJob(store.Job{UUID: uuid, Kind: kind, Key: key, Detail: detail}, blocking, except)
	switch {
	case errors.Is(err, store.ErrBusy):
		return c, &blocker, nil
	case err != nil:
		return c, nil, wrapf(err, "record the %s call before sending it", kind)
	}
	c.id = id
	return c, nil, nil
}

// fill records the scheduler jobs the reply named (and detail values of the
// reply) and returns the record's id; a reply without jobs leaves nothing to
// follow, so the claim is removed (0). When the jobs cannot be recorded (a
// store error), the claim stays: the call was sent and runs, so it keeps
// blocking a repeat until store.ClaimBound, as a claim being sent does.
func (c claim) fill(jobs []string, detail map[string]string) int64 {
	if c.id == 0 {
		return 0
	}
	st, err := c.s.Store()
	if err != nil {
		return 0
	}
	if len(jobs) == 0 {
		_ = st.DropJob(c.id)
		return 0
	}
	// One retry: a store held past its busy timeout by another process is
	// the usual cause, and an unrecorded call is followed only until
	// store.ClaimBound (60-L1).
	if err := st.SetJobs(c.id, jobs, detail); err != nil {
		time.Sleep(time.Second)
		_ = st.SetJobs(c.id, jobs, detail)
	}
	return c.id
}

// drop removes the claim of a call whose send failed.
func (c claim) drop() {
	if c.id == 0 {
		return
	}
	if st, err := c.s.Store(); err == nil {
		_ = st.DropJob(c.id)
	}
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
	if len(j.Jobs) == 0 {
		// Claimed and being sent (claimJob); a claim past store.ClaimBound
		// is never read.
		return "running", nil
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
			// moment after the reply that named it (j.At, however long the
			// send took). The scheduler keeps ended jobs (read
			// 2026-10-27: a job of 2026-09-16 still answered Succeeded), so
			// past jobFresh Canceled is the job's own end.
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
