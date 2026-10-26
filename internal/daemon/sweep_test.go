package daemon_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

func TestSweepRemovesOldTemporaryFilesOnly(t *testing.T) {
	paths := domain.PathsUnder(t.TempDir())
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	write := func(path string, mtime time.Time) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gone := []string{
		write(filepath.Join(paths.Cache, "task", "ModelGenerate-v1", ".fetch-123"), old),
		write(filepath.Join(paths.Downloads, "1111-owl", ".download-456"), old),
		write(filepath.Join(paths.Root, ".tmp-789"), old),
		write(filepath.Join(paths.Root, ".credentials-1.tmp"), old),
	}
	kept := []string{
		write(filepath.Join(paths.Cache, "task", "ModelGenerate-v1", ".fetch-young"), now.Add(-10*time.Minute)), // a fetch in progress
		write(filepath.Join(paths.Cache, "task", "ModelGenerate-v1", "poster.webp"), old),
		write(filepath.Join(paths.Downloads, "1111-owl", "model.obj"), old),
		write(filepath.Join(paths.Root, "credentials.json"), old),
		write(filepath.Join(t.TempDir(), ".fetch-elsewhere"), old), // outside the data root
	}
	// A folder named like a temporary file is not a file to remove.
	if err := os.MkdirAll(filepath.Join(paths.Cache, ".fetch-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(paths.Cache, ".fetch-dir"), old, old); err != nil {
		t.Fatal(err)
	}
	if n := daemon.Sweep(paths, now); n != len(gone) {
		t.Errorf("removed %d, want %d", n, len(gone))
	}
	for _, p := range gone {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there (%v)", p, err)
		}
	}
	for _, p := range append(kept, filepath.Join(paths.Cache, ".fetch-dir")) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	// Nothing to do on a data root that does not exist yet.
	if n := daemon.Sweep(domain.PathsUnder(filepath.Join(t.TempDir(), "none")), now); n != 0 {
		t.Errorf("empty root: %d", n)
	}
}
