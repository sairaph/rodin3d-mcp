package daemon

import (
	"os"
	"path/filepath"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
)

// staleAge is how old a leftover temporary file must be before Sweep
// removes it; younger ones may belong to a write in progress.
const staleAge = time.Hour

// Sweep removes temporary files a hard stop left behind (on Windows the
// daemon is stopped by killing it, so deferred clean-ups never run): image
// fetches in the cache, downloads in the default download folder, and
// atomic writes in the data root. It returns how many it removed.
func Sweep(paths domain.Paths, now time.Time) int {
	removed := 0
	stale := func(path string, fi os.FileInfo) bool {
		name := fi.Name()
		temp := false
		for _, p := range []string{".fetch-", ".download-", ".credentials-", ".tmp-"} {
			if len(name) > len(p) && name[:len(p)] == p {
				temp = true
			}
		}
		return temp && !fi.IsDir() && now.Sub(fi.ModTime()) > staleAge
	}
	for _, dir := range []string{paths.Cache, paths.Downloads} {
		_ = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err == nil && stale(path, fi) && os.Remove(path) == nil {
				removed++
			}
			return nil
		})
	}
	if entries, err := os.ReadDir(paths.Root); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && stale(filepath.Join(paths.Root, e.Name()), fi) && os.Remove(filepath.Join(paths.Root, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}
