package assets

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sairaph/rodin3d-mcp/internal/fakehyper"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

func cacheFixture(t *testing.T) (*fakehyper.Server, *Cache, *store.Store) {
	t.Helper()
	fake := fakehyper.New()
	t.Cleanup(fake.Close)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rodin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// File addresses are signed; they need no bearer.
	client := rodin.New(fake.Base(), rodin.StaticToken(""), "test")
	return fake, NewCache(filepath.Join(dir, "cache"), st, client), st
}

func signed(fake *fakehyper.Server, path string) string {
	return fake.HTTP.URL + "/files/" + path + "?sig=" + fakehyper.SignatureMark + "1&exp=1"
}

// L10: a cached image is revalidated with its ETag; an unchanged file costs
// a 304, a rewritten one is fetched again, and a host that fails leaves the
// stored copy serving.
func TestCacheRevalidates(t *testing.T) {
	fake, cache, st := cacheFixture(t)
	ctx := context.Background()
	fake.SetFile("t/poster.webp", []byte("version one"))
	a := Asset{Stage: StageGeometryDraft, Mode: "poster", Step: rodin.StepModelGenerate, Version: "v1", URL: signed(fake, "t/poster.webp")}

	file, fetched, err := cache.Get(ctx, "t", a)
	if err != nil || !fetched {
		t.Fatalf("first get: fetched %v err %v", fetched, err)
	}
	rows, _ := st.Files("t", "cache:poster")
	if len(rows) != 1 || rows[0].Validator != fakehyper.ETag([]byte("version one")) {
		t.Fatalf("validator not stored: %+v", rows)
	}

	// Same file, re-signed address: a conditional request, answered 304.
	a.URL = signed(fake, "t/poster.webp") + "&again=1"
	if f, fetched, err := cache.Get(ctx, "t", a); err != nil || fetched || f != file {
		t.Fatalf("second get: %s fetched %v err %v", f, fetched, err)
	}
	if ok, notMod := fake.FileReplies(); ok != 1 || notMod != 1 {
		t.Fatalf("file replies: %d with body, %d not modified; want 1 and 1", ok, notMod)
	}

	// The server rewrites the file at the same address: fetched again.
	fake.SetFile("t/poster.webp", []byte("version two!"))
	if _, fetched, err := cache.Get(ctx, "t", a); err != nil || !fetched {
		t.Fatalf("changed file: fetched %v err %v", fetched, err)
	}
	if data, _ := os.ReadFile(file); string(data) != "version two!" {
		t.Fatalf("cached content %q", data)
	}

	// The host fails: the stored copy serves, nothing is lost.
	for _, status := range []int{503, 403} {
		fake.SetFileStatus(status)
		if f, fetched, err := cache.Get(ctx, "t", a); err != nil || fetched || f != file {
			t.Fatalf("host %d: %s fetched %v err %v", status, f, fetched, err)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != "version two!" {
		t.Fatalf("copy damaged by a failed fetch: %q", data)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".fetch-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	// Without a stored copy, a failing host is an error.
	b := Asset{Stage: StageGeometryDraft, Mode: "poster", Step: rodin.StepModelGenerate, Version: "v2", URL: signed(fake, "t/other.webp")}
	if _, _, err := cache.Get(ctx, "t", b); err == nil {
		t.Fatal("no copy and no host, but no error")
	}
}

// A copy whose file was deleted or cut short is fetched again in full.
func TestCacheRefetchesAMissingCopy(t *testing.T) {
	fake, cache, _ := cacheFixture(t)
	ctx := context.Background()
	fake.SetFile("t/poster.webp", []byte("content"))
	a := Asset{Stage: StageGeometryDraft, Mode: "poster", Step: rodin.StepModelGenerate, Version: "v1", URL: signed(fake, "t/poster.webp")}
	file, _, err := cache.Get(ctx, "t", a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("cut"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, fetched, err := cache.Get(ctx, "t", a); err != nil || !fetched {
		t.Fatalf("cut copy: fetched %v err %v", fetched, err)
	}
	if data, _ := os.ReadFile(file); string(data) != "content" {
		t.Fatalf("content %q", data)
	}
}
