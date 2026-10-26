package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/assets"
	"github.com/sairaph/rodin3d-mcp/internal/daemon"
	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Every tool that spends credits or changes a task goes through the same
// steps, in this order, so a refusal never costs anything:
//
//  1. permit: the [permission] key of config.toml (read on every call).
//  2. the task's stage allows the tool (actionCard).
//  3. prepare: the wallet and privileges (user/get_info), the quote of the
//     price table, plan gates, and the expected_credits compare-and-swap.
//  4. open: a ledger row is written before the request is sent.
//  5. the request, sent once.
//  6. settle: the card is read (and stored, so the daemon watches the task)
//     until the job finished or the wait ends.
//  7. close: the wallet is read again and the ledger row finished.

// UnknownPrice is the expected_credits value that accepts a price quote_cost
// reports as not known.
const UnknownPrice = -1.0

// FlowWait bounds how long a stage tool waits for its job (plan 2.6.3: 45 s).
const FlowWait = 45 * time.Second

// permit returns the permission key of op, or forbidden when config.toml
// does not allow it.
func (s *Service) permit(op string) (string, error) {
	key, ok := price.PermissionKey(op)
	if !ok {
		return "", fmt.Errorf("no permission key for %s", op)
	}
	cfg, err := s.Config()
	if err != nil {
		return "", err // malformed: every write is denied, the error says why
	}
	if !cfg.Permission.Allowed(key) {
		value, _ := cfg.Permission.Get(key)
		return "", failFields(render.CodeForbidden,
			fmt.Sprintf("%s is not allowed: [permission] %s is %q in %s.", op, key, value, s.paths.Config),
			fmt.Sprintf("Ask the user whether to allow it; they set %s = \"allow\" under [permission] in %s, and the next call reads it.", key, s.paths.Config),
			map[string]any{"permission": key, "value": value, "file": s.paths.Config})
	}
	return key, nil
}

// actionCard reads the task and checks that its stage offers tool now.
func (s *Service) actionCard(ctx context.Context, id, tool string) (string, *rodin.Card, stage.Status, error) {
	uuid, err := s.resolveTask(id)
	if err != nil {
		return "", nil, stage.Status{}, err
	}
	card, err := s.client.Card(ctx, uuid)
	if err != nil {
		return "", nil, stage.Status{}, err
	}
	st := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
	actions := stage.Actions(st)
	if st.State != stage.Running && contains(actions, tool) {
		return uuid, card, st, nil
	}
	front := s.taskFront(card)
	hint := "Call wait_task with this id until the job finishes, then get_task for the actions."
	if st.State != stage.Running {
		hint = "Allowed now: " + orNone(front.Actions) + "."
	}
	return "", nil, stage.Status{}, failFields(render.CodeConflict,
		fmt.Sprintf("%s is not possible now: the task is at the %s (%s).", tool, describeStage(st), st.State), hint,
		map[string]any{"stage": st.Stage, "state": st.State, "actions": front.Actions})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// WalletBrief is the part of the wallet a spend changes.
type WalletBrief struct {
	Available float64 `yaml:"available"`
	Frozen    float64 `yaml:"frozen"`
}

// chargeFront is the charge: block of a paid tool's reply.
type chargeFront struct {
	Quote        price.Quote  `yaml:"quote"`
	Expected     *float64     `yaml:"expected_credits,omitempty"`
	WalletBefore WalletBrief  `yaml:"wallet_before"`
	WalletAfter  *WalletBrief `yaml:"wallet_after,omitempty"`
}

// spend is one paid or changing call on its way through the steps above.
type spend struct {
	s        *Service
	op       string
	uuid     string
	info     *rodin.UserInfo
	quote    price.Quote
	expected *float64
	before   Wallet
	ledger   int64
	after    *Wallet
}

// prepare reads the wallet, prices the call and checks expected. required
// is true for tools whose schema requires expected_credits; the others need
// it only when the price is not zero.
func (s *Service) prepare(ctx context.Context, op, uuid string, pin price.Input, expected *float64, required bool) (*spend, error) {
	info, err := s.client.GetInfo(ctx)
	if err != nil {
		return nil, err
	}
	pin.Operation = op
	q, err := price.For(pin, price.Account{Privileges: info.Meta.Privileges, Plan: info.Meta.Subscriptions.HighestLevel})
	if err != nil {
		return nil, err
	}
	if err := checkExpected(op, uuid, q, expected, required); err != nil {
		return nil, err
	}
	return &spend{s: s, op: op, uuid: uuid, info: info, quote: q, expected: expected, before: WalletOf(info)}, nil
}

// checkExpected is the compare-and-swap of plan D6: the call runs only for
// the price the caller was quoted.
func checkExpected(op, uuid string, q price.Quote, expected *float64, required bool) error {
	quoteHint := "Call quote_cost with operation " + op
	if uuid != "" {
		quoteHint += " and task_id " + uuid
	}
	quoteHint += " and the same settings, show the price to the user if they have not agreed to it, and pass its credits as expected_credits (" +
		fmt.Sprintf("%g", UnknownPrice) + " when it says the price is not known)."
	fields := map[string]any{"quote": q}
	if expected != nil {
		fields["expected_credits"] = *expected
	}
	free := q.Known && q.Credits != nil && *q.Credits == 0
	switch {
	case expected == nil && (required || !free):
		return failFields(render.CodeConflict, op+" needs expected_credits: the price this call was quoted.", quoteHint, fields)
	case expected == nil:
		return nil
	case !q.Known:
		if *expected != UnknownPrice {
			return failFields(render.CodeConflict,
				fmt.Sprintf("The price of %s is not known (%s), but expected_credits is %g.", op, q.Basis, *expected), quoteHint, fields)
		}
	case math.Abs(*q.Credits-*expected) > 0.001:
		return failFields(render.CodeConflict,
			fmt.Sprintf("The price of %s is %g credits now, not the expected %g.", op, *q.Credits, *expected), quoteHint, fields)
	}
	return nil
}

// open writes the ledger row of the call about to be sent.
func (sp *spend) open() {
	st, err := sp.s.Store()
	if err != nil {
		return
	}
	e := store.LedgerEntry{Op: sp.op, UUID: sp.uuid, Expected: sp.expected, FrozenBefore: sp.before.Frozen, BalanceBefore: sp.before.Available}
	if sp.quote.Known {
		e.Quoted = sp.quote.Credits
	}
	if id, err := st.AddLedger(e); err == nil {
		sp.ledger = id
	}
}

// close reads the wallet after the call and finishes the ledger row with
// result (ok, or the error).
func (sp *spend) close(ctx context.Context, callErr error) {
	if info, err := sp.s.client.GetInfo(ctx); err == nil {
		w := WalletOf(info)
		sp.after = &w
	}
	st, err := sp.s.Store()
	if err != nil || sp.ledger == 0 {
		return
	}
	result := "ok"
	switch {
	case callErr != nil && sentUnanswered(callErr):
		result = "unknown: " + oneLine(callErr.Error())
	case callErr != nil:
		result = "error: " + oneLine(callErr.Error())
	}
	var frozen, avail *float64
	if sp.after != nil {
		frozen, avail = &sp.after.Frozen, &sp.after.Available
	}
	_ = st.FinishLedger(sp.ledger, sp.uuid, frozen, avail, result)
}

func (sp *spend) front() *chargeFront {
	c := &chargeFront{Quote: sp.quote, Expected: sp.expected, WalletBefore: WalletBrief{sp.before.Available, sp.before.Frozen}}
	if sp.after != nil {
		c.WalletAfter = &WalletBrief{sp.after.Available, sp.after.Frozen}
	}
	return c
}

// walletText describes the wallet change of a finished spend.
func (sp *spend) walletText() string {
	if sp.after == nil {
		return fmt.Sprintf("Wallet before: %s available, %s frozen (the wallet could not be read after the call).", num(sp.before.Available), num(sp.before.Frozen))
	}
	return fmt.Sprintf("Wallet: %s -> %s available, %s -> %s frozen (frozen credits are taken when the job finishes, or released).",
		num(sp.before.Available), num(sp.after.Available), num(sp.before.Frozen), num(sp.after.Frozen))
}

// sentUnanswered reports whether err means the request may have reached
// the service without an answer: it must not be sent again.
func sentUnanswered(err error) bool {
	var ne *rodin.NetError
	var netErr net.Error
	return errors.As(err, &ne) || errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// sendFailure turns the error of the sent request into the reply: a
// request that got no answer is never to be sent again.
func sendFailure(op, uuid string, err error) error {
	if !sentUnanswered(err) {
		return err
	}
	where := "list_tasks (newest first) to see whether the new task exists"
	if uuid != "" {
		where = "get_task with " + uuid + " to see whether the job started"
	}
	return fail(render.CodeUnavailable,
		fmt.Sprintf("The %s request was sent but no answer came back (%s); it may have started on Hyper3D.", op, oneLine(err.Error())),
		"Do not send it again: call "+where+".")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// settled is a task's card once the job of a call finished or the wait
// ended.
type settled struct {
	card     *rodin.Card
	status   stage.Status
	waited   int
	timedOut bool
}

// settle reads the task's card after a call that started jobs and waits,
// when wait is true, until the job finished: the card is not running and
// either it moved away from before, it was seen running, or every job of
// the reply reports done on the scheduler (the card may not show the new
// job in the first second). The first and the last card are stored, so the
// daemon watches the task and the index shows it.
func (s *Service) settle(ctx context.Context, uuid string, before stage.Status, jobs []string, wait bool) (*settled, error) {
	start := time.Now()
	card, err := s.client.Card(ctx, uuid)
	// A task created a moment ago may not have a card yet: ask again a few
	// times before giving up.
	for i := 0; i < 5 && isNoSuchTask(err); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		card, err = s.client.Card(ctx, uuid)
	}
	if err != nil {
		return nil, err
	}
	s.saveCard(card)
	out := &settled{card: card, status: stage.Of(card.Step, card.UserState, int(card.PendingJobs))}
	if !wait {
		return out, nil
	}
	deadline := start.Add(FlowWait)
	seenRunning := false
	for {
		st := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
		out.card, out.status = card, st
		if st.State == stage.Running {
			seenRunning = true
		} else if st != before || seenRunning || s.jobsDone(ctx, jobs) {
			break
		}
		left := time.Until(deadline)
		if left <= 0 {
			out.timedOut = true
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(WaitPoll, left)):
		}
		next, err := s.client.Card(ctx, uuid)
		if err != nil {
			return nil, err
		}
		card = next
	}
	s.saveCard(out.card)
	out.waited = int(time.Since(start).Round(time.Second).Seconds())
	return out, nil
}

func isNoSuchTask(err error) bool {
	var se *rodin.ServiceError
	return errors.As(err, &se) && se.Code == "NO_SUCH_TASK"
}

// jobsDone reports whether every job has left the scheduler's queue. No
// jobs, or a scheduler that cannot be asked, is "not known done".
func (s *Service) jobsDone(ctx context.Context, jobs []string) bool {
	if len(jobs) == 0 {
		return false
	}
	for _, j := range jobs {
		status, err := s.client.JobStatus(ctx, j)
		if err != nil {
			return false
		}
		switch status {
		case "Succeeded", "Failed", "Canceled":
		default:
			return false
		}
	}
	return true
}

// saveCard stores a card a tool read after changing its task, so the
// index lists a new task at once and the daemon watches a running one.
func (s *Service) saveCard(card *rodin.Card) {
	if st, err := s.Store(); err == nil {
		_, _ = daemon.SaveCard(st, card)
	}
}

// flowFront is the reply of a stage tool.
type flowFront struct {
	Task     TaskFront    `yaml:"task"`
	Charge   *chargeFront `yaml:"charge,omitempty"`
	Waited   int          `yaml:"waited_seconds"`
	TimedOut bool         `yaml:"timed_out"`
	Version  string       `yaml:"version,omitempty"`
	Extra    any          `yaml:"detail,omitempty"`
}

// finishFlow builds the reply of a stage tool from its settled card.
func (s *Service) finishFlow(ctx context.Context, sp *spend, set *settled, lead string, extra any) Result {
	front := flowFront{Task: s.taskFront(set.card), Charge: sp.front(), Waited: set.waited, TimedOut: set.timedOut, Extra: extra}
	var b strings.Builder
	b.WriteString(lead)
	fmt.Fprintf(&b, " Now: %s, %s.", describeStage(set.status), set.status.State)
	if set.status.State == stage.Unknown {
		fmt.Fprintf(&b, " The card reports user_state %q, which this build does not know (a failed job is one possibility); get_task shows the card.", set.status.UserState)
	}
	b.WriteString("\n\n" + sp.walletText())
	switch {
	case set.timedOut || set.status.State == stage.Running:
		b.WriteString("\n\nNext: wait_task with " + set.card.TaskUUID + " until the job finishes.")
	default:
		b.WriteString("\n\nNext: " + nextFor(front.Task))
	}
	res := Result{Front: front, Body: b.String()}
	if set.status.State != stage.Running {
		if img, ok := s.posterImage(ctx, set.card.TaskUUID, assetsOf(set.card), set.status); ok {
			res.Images = append(res.Images, img)
		}
	}
	return res
}

// pinFromCard fills the price inputs a task's card answers and the caller
// left empty: model, tier, the texture tier a material redo repeats, and
// the task's age (fork rule).
func pinFromCard(pin *price.Input, card *rodin.Card) {
	if pin.Model == "" {
		pin.Model = stage.Model(card.Setting("generation_mode"))
	}
	if pin.Tier == "" {
		pin.Tier = stage.Tier(card.Setting("brilliant_mode"))
	}
	if pin.TextureMode == "" && pin.Operation == "redo_material" {
		pin.TextureMode = card.Setting("texture_mode")
	}
	if ts, err := time.Parse(time.RFC3339, card.Time); err == nil {
		pin.TaskCreated = ts
	}
}

// assetsOf lists the server images on a card (no history read).
func assetsOf(card *rodin.Card) []assets.Asset { return assets.Index(card, assets.History{}) }
