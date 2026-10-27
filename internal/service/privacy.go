package service

import (
	"context"
	"fmt"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/rodin"
)

// Task privacy (owner decision 2026-10-26: "private on by default"). Every
// task this tool creates is made private right after the request that
// created it, the way the web does when its private switch is on; public
// true on generate_model or fork_task keeps the new task public. set_private
// changes a task later. task/set_private flips the task, so every change
// reads the card before and after and sends the call at most once.

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
// the card, sends set_private only when the card differs, and reads the card
// again. The card returned is the last one read (nil when none could be
// read); changed reports that the card now differs from the first read.
func (s *Service) applyPrivacy(ctx context.Context, uuid string, want bool) (*rodin.Card, bool, error) {
	card, err := s.newCard(ctx, uuid)
	if err != nil {
		return nil, false, wrapf(err, "read the card of %s", short(uuid))
	}
	if card.IsPrivate == want {
		return card, false, nil
	}
	callErr := s.client.SetPrivate(ctx, uuid)
	after, err := s.client.Card(ctx, uuid)
	switch {
	case err != nil && callErr != nil:
		return card, false, callErr
	case err != nil:
		return card, false, wrapf(err, "set_private was accepted; read the card of %s again", short(uuid))
	case after.IsPrivate == want:
		// Also when the call got no answer: the card is what counts.
		return after, true, nil
	case callErr != nil:
		return after, false, callErr
	}
	return after, false, fail(render.CodeUnavailable,
		fmt.Sprintf("task/set_private was accepted, but the card of %s still reads %s.", short(uuid), privacyWord(after.IsPrivate)),
		"Call get_task in a few seconds and call set_private again only when it still shows the old privacy: set_private sends Hyper3D's "+
			"toggle whenever the card reads the other privacy, so a card that has not caught up with this change would make it flip the task back.")
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
	now := "may still be " + privacyWord(public)
	if p.Private != nil {
		now = "is " + privacyWord(*p.Private)
	}
	return p, fmt.Sprintf("Privacy: the new task %s could not be made %s (%s); it %s. Call set_private with task_id %s and private %t.",
		short(uuid), privacyWord(!public), p.Error, now, uuid, !public)
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
