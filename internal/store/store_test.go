package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rodin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestListRowsCardsAndEvents(t *testing.T) {
	s := open(t)
	needs, err := s.UpsertListRow(ListRow{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelGenerate", Created: "2026-10-01T00:00:00Z"})
	if err != nil || !needs {
		t.Fatalf("new row: %v %v", needs, err)
	}
	changed, err := s.SaveCard(CardSummary{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelGenerate", UserState: "Generating", Views: []string{"F", "B"}}, []byte(`{"a":1}`))
	if err != nil || !changed {
		t.Fatalf("first card: %v %v", changed, err)
	}
	if needs, _ := s.UpsertListRow(ListRow{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelGenerate", Created: "2026-10-01T00:00:00Z"}); needs {
		t.Error("unchanged row needs a card")
	}
	if needs, _ := s.UpsertListRow(ListRow{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelRefine", Created: "2026-10-01T00:00:00Z"}); !needs {
		t.Error("changed step does not need a card")
	}
	if ids, _ := s.InFlight(); len(ids) != 1 {
		t.Errorf("in flight %v", ids)
	}
	changed, _ = s.SaveCard(CardSummary{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelGenerate", UserState: "DonePreview"}, []byte(`{"a":2}`))
	if !changed {
		t.Error("state change not seen")
	}
	if changed, _ := s.SaveCard(CardSummary{UUID: "aaaaaaaa-1", Prompt: "owl", Step: "ModelGenerate", UserState: "DonePreview"}, []byte(`{"a":2}`)); changed {
		t.Error("same state reported as change")
	}
	evs, _ := s.Events("aaaaaaaa-1")
	if len(evs) != 2 || evs[1].UserState != "DonePreview" {
		t.Errorf("events %+v", evs)
	}
	task, err := s.Task("aaaaaaaa-1")
	if err != nil || task.Created != "2026-10-01T00:00:00Z" || task.UserState != "DonePreview" {
		t.Errorf("task %+v %v", task, err)
	}
	card, _, err := s.Card("aaaaaaaa-1")
	if err != nil || string(card) != `{"a":2}` {
		t.Errorf("card %s %v", card, err)
	}
}

func TestResolveID(t *testing.T) {
	s := open(t)
	for _, id := range []string{"abcdef01-0000", "abcdef01-1111", "12345678-0000"} {
		s.UpsertListRow(ListRow{UUID: id})
	}
	if id, err := s.ResolveID("12345678"); err != nil || id != "12345678-0000" {
		t.Errorf("prefix: %s %v", id, err)
	}
	var amb *AmbiguousError
	if _, err := s.ResolveID("abcdef01"); !errors.As(err, &amb) || len(amb.Matches) != 2 {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := s.ResolveID("abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("short: %v", err)
	}
	if _, err := s.ResolveID("ffffffff"); !errors.Is(err, ErrNotFound) {
		t.Errorf("none: %v", err)
	}
	if _, err := s.ResolveID("abcdef0%"); !errors.Is(err, ErrNotFound) {
		t.Errorf("wildcard: %v", err)
	}
}

func TestClaimAccountAndPrune(t *testing.T) {
	s := open(t)
	if reset, _ := s.ClaimAccount("u1"); reset {
		t.Error("first claim reset")
	}
	s.UpsertListRow(ListRow{UUID: "a"})
	s.UpsertListRow(ListRow{UUID: "b"})
	s.MarkSynced(time.Now())
	if n, err := s.Prune(map[string]bool{"a": true}); err != nil || n != 1 {
		t.Errorf("prune %d %v", n, err)
	}
	if reset, _ := s.ClaimAccount("u1"); reset {
		t.Error("same account reset")
	}
	if reset, _ := s.ClaimAccount("u2"); !reset {
		t.Error("other account kept the tasks")
	}
	tasks, _ := s.Tasks()
	st, _ := s.State()
	if len(tasks) != 0 || !st.LastSync.IsZero() || st.UserUUID != "u2" {
		t.Errorf("after reset %d tasks, state %+v", len(tasks), st)
	}
}

func TestFilesAndPhase(t *testing.T) {
	s := open(t)
	s.RecordFile(File{UUID: "a", Kind: "cache:geometry/poster", Path: "/x", Bytes: 3, SHA256: "h", SourceKey: "k"})
	s.RecordFile(File{UUID: "a", Kind: "download", Path: "/y", Bytes: 4, SHA256: "h"})
	if f, _ := s.Files("a", "cache:"); len(f) != 1 || f[0].SourceKey != "k" {
		t.Errorf("cache files %+v", f)
	}
	if f, _ := s.Files("a", ""); len(f) != 2 {
		t.Errorf("all files %+v", f)
	}
	s.SetPhase(PhaseError, "boom")
	if st, _ := s.State(); st.Phase != PhaseError || st.LastError != "boom" {
		t.Errorf("state %+v", st)
	}
}

// L16: a data folder whose name holds URI characters (a home folder like
// "C:\Users\50%#off") opens the database at exactly that path.
func TestOpenUnderAFolderWithURICharacters(t *testing.T) {
	names := []string{"50% #off", "a%20b", "x%25y#z", "semi;colon&amp=1"}
	if os.PathSeparator == '/' {
		names = append(names, "what?")
	}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		p := filepath.Join(dir, "rodin.db")
		s, err := Open(p)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if _, err := s.UpsertListRow(ListRow{UUID: "a"}); err != nil {
			t.Fatalf("%q: write: %v", name, err)
		}
		s.Close()
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%q: the database is not at its path: %v", name, err)
		}
		entries, _ := os.ReadDir(filepath.Dir(dir))
		if len(entries) != 1 || entries[0].Name() != name {
			t.Fatalf("%q: folders created: %v", name, entries)
		}
		s, err = Open(p)
		if err != nil {
			t.Fatalf("%q: reopen: %v", name, err)
		}
		if tasks, _ := s.Tasks(); len(tasks) != 1 {
			t.Fatalf("%q: reopened a different database (%d tasks)", name, len(tasks))
		}
		s.Close()
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rodin.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertListRow(ListRow{UUID: "a"})
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if tasks, _ := s.Tasks(); len(tasks) != 1 {
		t.Fatalf("tasks after reopen: %d", len(tasks))
	}
}
