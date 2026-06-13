package postera

import (
	"errors"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
)

type posterumView struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
	CreatedAt string `json:"created_at"`
}

type createArgs struct {
	Message   string `json:"message"`
	TriggerAt string `json:"trigger_at"`
}

type listUpcomingArgs struct{}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

type cancelArgs struct {
	ID string `json:"id"`
}

type cancelOutput struct {
	ID        string `json:"id"`
	Cancelled bool   `json:"cancelled"`
}

func Tools(p *postera.Postarius) ([]adktool.Tool, error) {
	if p == nil {
		return nil, errors.New("adk: Tools: postarius must not be nil")
	}

	create, err := functiontool.New(
		functiontool.Config{
			Name: "schedule_recall",
			Description: `Schedule a message to your future self that Postera (Your Agentic Self Recall System) will trigger at a precise date and time.

WHEN TO USE:
- You need to follow up on something at a specific future time
- The human asks you to remind them or check in on something later
- A task or decision needs to be revisited at a defined point in the future
- You want to ensure continuity of a plan across sessions

HOW TO USE:
- Write message as a clear, self-contained instruction to your future self.
  Assume your future self has no memory of the current conversation.
  Include all the context needed to act: who, what, and why.
- Provide trigger_at as an ISO 8601 datetime without a timezone suffix
  (e.g. "2026-05-07T22:00:00"). Time is always localized consistently
  across human, you (agent), and Postera — no conversion needed.

GOOD message EXAMPLES:
- "Follow up with the human on whether they submitted the Q3 report they
   mentioned. They were waiting on approval from their manager."
- "Check in on the deployment scheduled for this morning. Ask the human
   if it succeeded and whether any issues came up."

BAD message EXAMPLES:
- "Follow up" — too vague, no context for future self
- "Reminder" — not actionable, missing all substance`,
		},
		func(toolCtx adktool.Context, in createArgs) (posterumView, error) {
			pstr, err := p.Create(toolCtx, postera.CreateArgs{
				Message:   in.Message,
				TriggerAt: in.TriggerAt,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(pstr, p.LocationFromContext(toolCtx)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	listUpcoming, err := functiontool.New(
		functiontool.Config{
			Name: "list_upcoming_recalls",
			Description: `List all recalls scheduled to trigger from this moment onward.

WHEN TO USE:
- The human asks what is coming up or what future recalls are pending
- You want to confirm to the human what is still in schedule
- You need a recall's id before cancelling it with cancel_recall`,
		},
		func(toolCtx adktool.Context, _ listUpcomingArgs) (listOutput, error) {
			entries, err := p.ListUpcoming(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, p.LocationFromContext(toolCtx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	cancel, err := functiontool.New(
		functiontool.Config{
			Name: "cancel_recall",
			Description: `Cancel a previously scheduled recall so it will no longer trigger.

WHEN TO USE:
- The human asks to cancel, drop, or call off a scheduled recall
- A follow-up is no longer needed because its purpose was already resolved
- You scheduled a recall in error and want to remove it

HOW TO USE:
- Provide id, the recall identifier (e.g. "pstr_...") returned by
  schedule_recall or list_upcoming_recalls.
- If you do not know the id, call list_upcoming_recalls first to find it.
- A recall that does not exist (or is outside the current scope) is
  reported as not found.`,
		},
		func(toolCtx adktool.Context, in cancelArgs) (cancelOutput, error) {
			if err := p.Cancel(toolCtx, in.ID); err != nil {
				return cancelOutput{}, err
			}
			return cancelOutput{ID: in.ID, Cancelled: true}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{create, listUpcoming, cancel}, nil
}

func toPosterumView(p postera.Posterum, loc *time.Location) posterumView {
	return posterumView{
		ID:        p.ID,
		Message:   p.Message,
		TriggerAt: p.TriggerAt.In(loc).Format(postera.TimeLayout),
		CreatedAt: p.CreatedAt.In(loc).Format(postera.TimeLayout),
	}
}

func toPosterumViews(entries []postera.Posterum, loc *time.Location) []posterumView {
	views := make([]posterumView, len(entries))
	for i, entry := range entries {
		views[i] = toPosterumView(entry, loc)
	}
	return views
}
