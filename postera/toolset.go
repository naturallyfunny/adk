package postera

import (
	"errors"
	"time"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/postera"
	"go.naturallyfunny.dev/postera/agent"
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

type listArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type listIncomingArgs struct{}

type listOutput struct {
	Entries []posterumView `json:"entries"`
}

func Tools(ts *agent.ToolSet) ([]adktool.Tool, error) {
	if ts == nil {
		return nil, errors.New("adk: Tools: ts must not be nil")
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
			p, err := ts.Create(toolCtx, agent.CreateArgs{
				Message:   in.Message,
				TriggerAt: in.TriggerAt,
			})
			if err != nil {
				return posterumView{}, err
			}
			return toPosterumView(p, ts.LocationFromContext(toolCtx)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	list, err := functiontool.New(
		functiontool.Config{
			Name: "list_recalls",
			Description: `List recalls scheduled within an optional time window.

WHEN TO USE:
- The human asks what recalls are scheduled in a given period
- You want to check whether a recall already exists before scheduling
  a new one to avoid duplicates

HOW TO USE:
- Provide from and/or to as ISO 8601 datetimes without a timezone suffix
  (e.g. "2026-05-07T09:00:00"). No conversion needed — time is localized
  consistently across human, you (agent), and Postera.
- Leave from empty to retrieve all recalls up to the to bound.
- Leave to empty to retrieve all recalls from the from bound onward.
- Leave both empty to retrieve all recalls ever scheduled.`,
		},
		func(toolCtx adktool.Context, in listArgs) (listOutput, error) {
			entries, err := ts.List(toolCtx, agent.ListArgs{
				From: in.From,
				To:   in.To,
			})
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(toolCtx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	listIncoming, err := functiontool.New(
		functiontool.Config{
			Name: "list_upcoming_recalls",
			Description: `List all recalls scheduled to trigger from this moment onward.

WHEN TO USE:
- The human asks what is coming up or what future recalls are pending
- You want to confirm to the human what is still in schedule

WHEN NOT TO USE:
- When the human wants to see past recalls — use list_recalls with a to
  bound in the past instead`,
		},
		func(toolCtx adktool.Context, _ listIncomingArgs) (listOutput, error) {
			entries, err := ts.ListIncoming(toolCtx)
			if err != nil {
				return listOutput{}, err
			}
			return listOutput{Entries: toPosterumViews(entries, ts.LocationFromContext(toolCtx))}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []adktool.Tool{create, list, listIncoming}, nil
}

func toPosterumView(p postera.Posterum, loc *time.Location) posterumView {
	return posterumView{
		ID:        p.ID,
		Message:   p.Message,
		TriggerAt: p.TriggerAt.In(loc).Format(agent.TimeLayout),
		CreatedAt: p.CreatedAt.In(loc).Format(agent.TimeLayout),
	}
}

func toPosterumViews(entries []postera.Posterum, loc *time.Location) []posterumView {
	views := make([]posterumView, len(entries))
	for i, entry := range entries {
		views[i] = toPosterumView(entry, loc)
	}
	return views
}
