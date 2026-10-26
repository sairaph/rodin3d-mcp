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
// atomic writes in the data root, and downloads in outputDirs (folders the
// caller chose earlier). It returns how many it removed.
func Sweep(paths domain.Paths, now time.Time, outputDirs ...string) int {
	removed := 0
	// Our own folders get all four prefixes; a folder the caller chose only
	// the one prefix download_model creates there, never a user's own file.
	ours := []string{".fetch-", ".download-", ".credentials-", ".tmp-"}
	stale := func(fi os.FileInfo, prefixes []string) bool {
		name := fi.Name()
		temp := false
		for _, p := range prefixes {
			if len(name) > len(p) && name[:len(p)] == p {
				temp = true
			}
		}
		return temp && !fi.IsDir() && now.Sub(fi.ModTime()) > staleAge
	}
	for _, dir := range []string{paths.Cache, paths.Downloads} {
		_ = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err == nil && stale(fi, ours) && os.Remove(path) == nil {
				removed++
			}
			return nil
		})
	}
	// The data root and the folders earlier downloads went to (an output_dir
	// the caller chose): their own files only, not their subfolders.
	sweepDir := func(dir string, prefixes []string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && stale(fi, prefixes) && os.Remove(filepath.Join(dir, e.Name())) == nil {
				removed++
			}
		}
	}
	sweepDir(paths.Root, ours)
	for _, dir := range outputDirs {
		sweepDir(dir, []string{".download-"})
	}
	return removed
}
