// Package daemon is the background syncer: it keeps the access token fresh,
// watches tasks a job is working on, and mirrors the account's task history
// into the store. It is a singleton (daemon.lock) and the store's only
// syncer; every surface starts it with EnsureRunning when signed in.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sairaph/mcp-wizard/daemon/lock"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/auth"
	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Timing (plan 2.5).
const (
	Tick          = 5 * time.Second  // in-flight watch and loop period
	SyncInterval  = 15 * time.Minute // full history sync
	RefreshAhead  = 10 * time.Minute // proactive token refresh
	pageSize      = 50
	maxPages      = 200 // loop guard: 10,000 tasks
	cardWorkers   = 4
	cycleDeadline = 10 * time.Minute
)

// LockOptions are the singleton files under paths.
func LockOptions(paths domain.Paths) lock.Options {
	return lock.Options{LockFile: paths.DaemonLock, PIDFile: paths.DaemonPID, LogFile: paths.DaemonLog}
}

// EnsureRunning starts `<exe> daemon` detached unless one runs. Losing the
// start race to another process counts as running.
func EnsureRunning(exe string, paths domain.Paths) bool {
	return lock.EnsureRunning(exe, []string{"daemon"}, LockOptions(paths))
}

// IsRunning reports whether a daemon holds the lock.
func IsRunning(paths domain.Paths) bool { return lock.IsRunning(paths.DaemonLock) }

// Stop stops the running daemon by the PID it recorded.
func Stop(paths domain.Paths) error { return lock.Stop(paths.DaemonPID, paths.DaemonLock) }

// Daemon is one sync loop.
type Daemon struct {
	Store   *store.Store
	Client  *rodin.Client
	Session *auth.Session
	Cache   *assets.Cache
	Log     *log.Logger
	Now     func() time.Time

	lastNeedsLogin bool
}

// ErrSignedOut ends Serve: there is nothing to sync without an account.
var ErrSignedOut = errors.New("signed out")

// Serve runs cycles every Tick until ctx ends or the user signs out.
func (d *Daemon) Serve(ctx context.Context) error {
	if d.Now == nil {
		d.Now = time.Now
	}
	d.logf("daemon started, pid %d", os.Getpid())
	defer d.logf("daemon stopped")
	for {
		err := d.Cycle(ctx)
		if errors.Is(err, ErrSignedOut) {
			d.logf("signed out; nothing to sync")
			return nil
		}
		if err != nil && ctx.Err() == nil {
			d.logf("cycle: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(Tick):
		}
	}
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
	}
}

// Cycle runs one pass: token, in-flight watch, and the history sync when due.
func (d *Daemon) Cycle(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, cycleDeadline)
	defer cancel()

	creds, err := d.Session.Store().Load(ctx)
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}
	switch creds.State() {
	case auth.StateSignedOut:
		return ErrSignedOut
	case auth.StateNeedsLogin:
		return d.needsLogin()
	}
	d.lastNeedsLogin = false
	if reset, err := d.Store.ClaimAccount(creds.UserUUID); err != nil {
		return err
	} else if reset {
		d.logf("signed in as another account; the task index starts over")
	}

	if refreshed, err := d.Session.RefreshIfDue(ctx, RefreshAhead); err != nil {
		if auth.IsLoginRequired(err) {
			return d.needsLogin()
		}
		d.logf("token refresh: %v", err) // the current token may still serve
	} else if refreshed {
		d.logf("access token refreshed")
	}

	watching, err := d.watch(ctx)
	if err != nil {
		return d.fail(err)
	}
	st, err := d.Store.State()
	if err != nil {
		return err
	}
	if st.LastSync.IsZero() || d.Now().Sub(st.LastSync) >= SyncInterval {
		if err := d.SyncHistory(ctx, creds.UserUUID); err != nil {
			return d.fail(err)
		}
	}
	phase := store.PhaseIdle
	if watching > 0 {
		phase = store.PhaseWatching
	}
	return d.Store.SetPhase(phase, "")
}

func (d *Daemon) needsLogin() error {
	if !d.lastNeedsLogin {
		d.logf("the session ended; waiting for a new login")
	}
	d.lastNeedsLogin = true
	return d.Store.SetPhase(store.PhaseNeedsLogin, "sign in again with `"+domain.BinaryName+" login`")
}

// fail records err as the phase. A refused session becomes needs_login.
func (d *Daemon) fail(err error) error {
	if auth.IsLoginRequired(err) || errors.Is(err, rodin.ErrUnauthorized) {
		return d.needsLogin()
	}
	_ = d.Store.SetPhase(store.PhaseError, oneLine(err.Error()))
	return err
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// watch refreshes the card of every task a job works on and returns how
// many there were.
func (d *Daemon) watch(ctx context.Context) (int, error) {
	ids, err := d.Store.InFlight()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := d.refreshCard(ctx, id); err != nil {
			var se *rodin.ServiceError
			if errors.As(err, &se) && se.Code == "NO_SUCH_TASK" {
				continue
			}
			if auth.IsLoginRequired(err) || errors.Is(err, rodin.ErrUnauthorized) {
				return 0, err
			}
			d.logf("watch %s: %v", short(id), err)
		}
	}
	return len(ids), nil
}

// refreshCard fetches and stores one card, caching its images on a change.
func (d *Daemon) refreshCard(ctx context.Context, id string) error {
	card, err := d.Client.Card(ctx, id)
	if err != nil {
		return err
	}
	changed, err := SaveCard(d.Store, card)
	if err != nil {
		return err
	}
	if changed {
		d.cacheImages(ctx, card)
	}
	return nil
}

// SyncHistory lists every page of the account's tasks, stores the rows,
// fetches the cards of new and changed tasks, and records the sync.
func (d *Daemon) SyncHistory(ctx context.Context, userUUID string) error {
	if err := d.Store.SetPhase(store.PhaseSyncing, ""); err != nil {
		return err
	}
	var need []string
	seen := map[string]bool{}
	total := -1
	for page := 1; page <= maxPages; page++ {
		res, err := d.Client.ListMine(ctx, userUUID, page, pageSize)
		if err != nil {
			return fmt.Errorf("list tasks (page %d): %w", page, err)
		}
		total = res.Pagination.Total
		for _, row := range res.Data {
			if row.TaskUUID == "" || seen[row.TaskUUID] {
				continue
			}
			seen[row.TaskUUID] = true
			needs, err := d.Store.UpsertListRow(store.ListRow{
				UUID: row.TaskUUID, Prompt: row.Prompt, Step: row.Step, Private: row.IsPrivate,
				PendingJobs: int(row.PendingJobs), Created: row.Time,
			})
			if err != nil {
				return err
			}
			if needs {
				need = append(need, row.TaskUUID)
			}
		}
		if len(res.Data) == 0 || page >= res.Pagination.TotalPages {
			break
		}
	}

	type result struct {
		card *rodin.Card
		id   string
		err  error
	}
	jobs := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for range cardWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				card, err := d.Client.Card(ctx, id)
				results <- result{card: card, id: id, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range need {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var firstAuthErr, saveErr error
	failed := 0
	for r := range results { // writes stay on this goroutine; always drain
		if saveErr != nil {
			continue
		}
		if r.err != nil {
			if auth.IsLoginRequired(r.err) || errors.Is(r.err, rodin.ErrUnauthorized) {
				firstAuthErr = r.err
			}
			failed++
			d.logf("card %s: %v", short(r.id), r.err)
			continue
		}
		changed, err := SaveCard(d.Store, r.card)
		if err != nil {
			saveErr = err
			continue
		}
		if changed {
			d.cacheImages(ctx, r.card)
		}
	}
	if saveErr != nil {
		return saveErr
	}
	if firstAuthErr != nil {
		return firstAuthErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if total == len(seen) {
		if n, err := d.Store.Prune(seen); err != nil {
			return err
		} else if n > 0 {
			d.logf("removed %d tasks the account no longer lists", n)
		}
	}
	if failed > 0 {
		d.logf("history sync: %d tasks, %d cards fetched, %d failed (retried next sync)", len(seen), len(need)-failed, failed)
	} else {
		d.logf("history sync: %d tasks, %d cards fetched", len(seen), len(need))
	}
	return d.Store.MarkSynced(d.Now())
}

// cacheImages stores the card's own stage images (poster, geometry,
// material). Failures are logged; get_preview fetches on demand anyway.
func (d *Daemon) cacheImages(ctx context.Context, card *rodin.Card) {
	if d.Cache == nil {
		return
	}
	for _, a := range assets.Index(card, assets.History{}) {
		switch a.Mode {
		case "poster", "geometry", "material":
		default:
			continue
		}
		if _, _, err := d.Cache.Get(ctx, card.TaskUUID, a); err != nil {
			d.logf("cache %s %s/%s: %v", short(card.TaskUUID), a.Stage, a.Mode, err)
		}
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// SaveCard stores card (its signed addresses stripped) with its summary and
// reports whether its step or user_state changed.
func SaveCard(st *store.Store, card *rodin.Card) (bool, error) {
	stripped, err := rodin.StripSignatures(card.Raw)
	if err != nil {
		return false, fmt.Errorf("strip card %s: %w", short(card.TaskUUID), err)
	}
	return st.SaveCard(Summarize(card), stripped)
}

// Summarize extracts the index columns of a card.
func Summarize(card *rodin.Card) store.CardSummary {
	mode := stage.Model(card.Setting("generation_mode"))
	tier := stage.Tier(card.Setting("brilliant_mode"))
	if mode == "gen-1.5" {
		tier = stage.Gen15Mode(card.Setting("generation_mode"))
	}
	var views []string
	if labels, ok := card.Settings["image_label"].([]any); ok {
		for _, l := range labels {
			if s, ok := l.(string); ok {
				views = append(views, s)
			}
		}
	}
	return store.CardSummary{
		UUID: card.TaskUUID, Prompt: card.Prompt, Step: card.Step, UserState: card.UserState,
		Mode: mode, Tier: tier, Views: views, Private: card.IsPrivate,
		PendingJobs: int(card.PendingJobs), Created: card.Time, Updated: card.GenerateTime,
	}
}
