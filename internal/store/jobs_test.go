package store

import (
	"errors"
	"testing"
	"time"
)

// TestClaimJob covers every branch of a claim: a free key records, a
// running call of a blocking kind refuses (also another session's claim
// without jobs), the caller's own call (except) and ended calls do not, a
// filled claim keeps its jobs and merged detail, a dropped one is gone, and
// a claim without jobs past ClaimBound is left out of reads and checks.
func TestClaimJob(t *testing.T) {
	s := open(t)
	rig := Job{UUID: "t1", Kind: "rig", Key: "model_refine", Detail: map[string]string{"template": "custom"}}
	block := []string{"rig", "reskin"}

	id, _, err := s.ClaimJob(rig, block, 0)
	if err != nil || id == 0 {
		t.Fatalf("free claim: %d %v", id, err)
	}
	// A second session while the first is sending (no jobs yet).
	if _, busy, err := s.ClaimJob(Job{UUID: "t1", Kind: "reskin", Key: "model_refine"}, block, 0); !errors.Is(err, ErrBusy) || busy.ID != id {
		t.Fatalf("claim during a send: %v, busy %d", err, busy.ID)
	}
	// Another key and another task are free.
	if _, _, err := s.ClaimJob(Job{UUID: "t1", Kind: "rig", Key: "texture_refine"}, block, 0); err != nil {
		t.Fatalf("other key: %v", err)
	}
	// A send that took long: the filled row's time is the fill's (a read of
	// its jobs is measured from the reply, review 58 L1).
	if _, err := s.db.Exec(`UPDATE jobs SET at_ms = ? WHERE id = ?`, time.Now().Add(-time.Minute).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobs(id, []string{"j1"}, map[string]string{"result": "r1"}); err != nil {
		t.Fatal(err)
	}
	j, err := s.LatestJob("t1", "rig", "model_refine")
	if err != nil || len(j.Jobs) != 1 || j.Detail["template"] != "custom" || j.Detail["result"] != "r1" || j.State != JobRunning || time.Since(j.At) > 10*time.Second {
		t.Fatalf("filled claim: %+v %v", j, err)
	}
	// The caller's own running call does not block its next step.
	rs, _, err := s.ClaimJob(Job{UUID: "t1", Kind: "reskin", Key: "model_refine"}, block, id)
	if err != nil || rs == 0 {
		t.Fatalf("own call excepted: %v", err)
	}
	if err := s.DropJob(rs); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestJob("t1", "reskin", "model_refine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dropped claim still read: %v", err)
	}
	if err := s.EndJob(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimJob(Job{UUID: "t1", Kind: "reskin", Key: "model_refine"}, block, 0); err != nil {
		t.Fatalf("after the end: %v", err)
	}
	// A claim of a process that died mid-send.
	old := time.Now().Add(-ClaimBound - time.Second).UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO jobs (uuid, kind, key, detail, jobs, state, at_ms) VALUES ('t2', 'bang_generate', 'v1', '{}', '[]', 'running', ?)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestJob("t2", "bang_generate", "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale claim read: %v", err)
	}
	if _, _, err := s.ClaimJob(Job{UUID: "t2", Kind: "bang_generate", Key: "v1"}, []string{"bang_generate"}, 0); err != nil {
		t.Fatalf("stale claim blocked: %v", err)
	}
	if err := s.SetJobs(id, nil, nil); err == nil {
		t.Fatal("a fill without jobs was taken")
	}
}
