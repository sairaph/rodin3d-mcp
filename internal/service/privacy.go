package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/rodin"
)

// Task privacy (owner decision 2026-10-26: "private on by default"). Every
// task this tool creates is made private right after the request that
// created it, the way the web does when its private switch is on; public
// true on generate_model or fork_task keeps the new task public. set_private
// changes a task later. task/set_private flips the task, so every change
// reads the card before and after and sends the call at most once. After a
// toggle that may have reached Hyper3D without a card read showing its
// result, the reply sends the agent to get_task, never straight back to
// set_private, and the next change waits for a settled card (settledCard).

// PrivacySettle is how long after a task/set_private that may have reached
// Hyper3D the card of that task is not trusted to show the task's privacy:
// a later change of the task waits until then before the read that decides
// whether to send the toggle. The live card showed a change at once (notes
// 35); the bound covers a card that lags by seconds, as its stage fields do
// (P2a). Tests shorten it.
var PrivacySettle = 10 * time.Second

// privacyFront is the privacy: block of a reply that created a task.
type privacyFront struct {
	Private *bool  `yaml:"private"` // the card's is_private after the change; null when no card could be read
	Changed bool   `yaml:"changed"`
	Error   string `yaml:"error,omitempty"`
}

func privacyWord(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

// applyPrivacy brings task uuid to private (want true) or public: it reads
// the settled card (settledCard), sends set_private only when the card
// differs, and reads the card again. The card returned is the last one read
// (nil when none could be read, also when the read after a toggle failed);
// changed reports that the card now differs from the first read.
func (s *Service) applyPrivacy(ctx context.Context, uuid string, want bool) (*rodin.Card, bool, error) {
	card, err := s.settledCard(ctx, uuid)
	if err != nil {
		return nil, false, wrapf(err, "read the card of %s", short(uuid))
	}
	if card.IsPrivate == want {
		return card, false, nil
	}
	callErr := s.client.SetPrivate(ctx, uuid)
	reached := callErr == nil || sentUnanswered(callErr)
	if reached {
		s.markToggle(uuid)
	}
	after, err := s.client.Card(ctx, uuid)
	switch {
	case err == nil && after.IsPrivate == want:
		// Also when the call got no answer: the card is what counts.
		return after, true, nil
	case !reached && err != nil:
		// Hyper3D refused the toggle: nothing changed, and its error says
		// why (sign in again, the quota, ...).
		return card, false, callErr
	case !reached:
		return after, false, callErr
	case err != nil:
		return nil, false, toggleSent(uuid, want, callErr, err)
	}
	return after, false, toggleSent(uuid, want, callErr, nil)
}

// settledCard reads the card of task uuid once no task/set_private on it
// that may have reached Hyper3D ended less than PrivacySettle ago, in this
// session or another (the store records them): a card read sooner may
// still show the privacy from before that toggle, and a toggle sent on it
// would flip the task back. Without a store nothing is recorded or waited
// for.
func (s *Service) settledCard(ctx context.Context, uuid string) (*rodin.Card, error) {
	for {
		wait := time.Until(s.lastToggle(uuid).Add(PrivacySettle))
		if wait <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return s.newCard(ctx, uuid)
}

func (s *Service) markToggle(uuid string) {
	if st, err := s.Store(); err == nil {
		_ = st.MarkToggle(uuid)
	}
}

func (s *Service) lastToggle(uuid string) time.Time {
	st, err := s.Store()
	if err != nil {
		return time.Time{}
	}
	at, _ := st.LastToggle(uuid)
	return at
}

// toggleSent is the error of a change whose task/set_private may have
// reached Hyper3D (accepted: callErr nil; or sent without an answer) while
// no card read after it shows the wanted privacy: the read failed (readErr)
// or the card still shows the old privacy. Another toggle now could undo the
// first, so the reply sends the agent to get_task first, whatever the read's
// own error would advise.
func toggleSent(uuid string, want bool, callErr, readErr error) *Failure {
	sent := "was accepted"
	if callErr != nil {
		sent = "was sent but no answer came back (" + oneLine(callErr.Error()) + ")"
	}
	after := "its card still reads " + privacyWord(!want)
	if readErr != nil {
		after = "reading its card afterwards failed (" + oneLine(readErr.Error()) + ")"
	}
	return failFields(render.CodeUnavailable,
		fmt.Sprintf("The set_private request on task %s %s, but %s.", uuid, sent, after),
		fmt.Sprintf("Do not call set_private again now: call get_task with %s first to see the privacy it shows, and call set_private only if it "+
			"still shows %s (set_private sends Hyper3D's toggle, which flips the task).", uuid, privacyWord(!want)),
		map[string]any{"accepted": callErr == nil, "toggle_sent": true, "task_id": uuid})
}

// isToggleSent reports whether err is a toggleSent error.
func isToggleSent(err error) bool {
	var f *Failure
	return errors.As(err, &f) && f.E.Fields["toggle_sent"] == true
}

// privacyOfNew applies the privacy of a create to the new task uuid. A
// failure does not fail the create (the task exists and may be paid for); it
// is reported in the block and in note.
func (s *Service) privacyOfNew(ctx context.Context, uuid string, public bool) (p *privacyFront, note string) {
	card, changed, err := s.applyPrivacy(ctx, uuid, !public)
	p = &privacyFront{Changed: changed}
	if card != nil {
		v := card.IsPrivate
		p.Private = &v
	}
	if err == nil {
		return p, ""
	}
	p.Error = oneLine(err.Error())
	if isToggleSent(err) {
		// The toggle may have been sent: set_private now could undo it.
		return p, fmt.Sprintf("Privacy: the new task %s may not be %s yet: %s Do not call set_private now: call get_task with %s first to see "+
			"the privacy it shows, and set_private with task_id %s and private %t only if it still shows %s.",
			short(uuid), privacyWord(!public), p.Error, uuid, uuid, !public, privacyWord(public))
	}
	now := "may still be " + privacyWord(public)
	if p.Private != nil {
		now = "is " + privacyWord(*p.Private)
	}
	return p, fmt.Sprintf("Privacy: the new task %s could not be made %s (%s); it %s. Call set_private with task_id %s and private %t.",
		short(uuid), privacyWord(!public), p.Error, now, uuid, !public)
}

// withPrivacyNote adds the privacy note of a create (privacyOfNew) to the
// hint of the create's failure after its request was accepted
// (acceptedFailure), so that reply still says what the new task's privacy
// needs.
func withPrivacyNote(err error, note string) error {
	var f *Failure
	if note != "" && errors.As(err, &f) {
		f.E.Hint += " " + note
	}
	return err
}

// SetPrivateInput is the input of set_private.
type SetPrivateInput struct {
	TaskID  string
	Private bool
}

type setPrivateFront struct {
	Task    TaskFront `yaml:"task"`
	Changed bool      `yaml:"changed"`
}

// SetPrivate is set_private: make an existing task private or public
// (task/set_private, free). A task already in that state is left as it is.
func (s *Service) SetPrivate(ctx context.Context, in SetPrivateInput) (Result, error) {
	if _, err := s.signedIn(ctx); err != nil {
		return Result{}, err
	}
	uuid, err := s.resolveTask(in.TaskID)
	if err != nil {
		return Result{}, err
	}
	card, changed, err := s.applyPrivacy(ctx, uuid, in.Private)
	if err != nil {
		return Result{}, err
	}
	s.saveCard(card)
	body := fmt.Sprintf("Task %s is %s now.", short(uuid), privacyWord(card.IsPrivate))
	if !changed {
		body = fmt.Sprintf("Task %s was already %s; nothing was sent.", short(uuid), privacyWord(card.IsPrivate))
	}
	return Result{Front: setPrivateFront{Task: s.liveTaskFront(ctx, card), Changed: changed}, Body: body}, nil
}
