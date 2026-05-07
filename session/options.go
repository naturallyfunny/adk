package session

import (
	"context"
	"iter"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type decoratedService struct {
	base         session.Service
	persistUser  bool
	persistAgent bool
	policy       string
	timezoneKey  any // external context key bridged to TimezoneKey per-request
}

type Option func(*decoratedService)

func WithoutUserMessagePersistence() Option {
	return func(d *decoratedService) {
		d.persistUser = false
	}
}

func WithoutAgentResponsePersistence() Option {
	return func(d *decoratedService) {
		d.persistAgent = false
	}
}

func WithPolicy(instruction string) Option {
	return func(d *decoratedService) {
		d.policy = instruction
	}
}

// WithTimezoneFromContext registers an external context key from which an IANA
// timezone string is read per-request. On each Get call, the decorator copies
// the value from the external key into TimezoneKey so that base implementations
// (e.g. zep) can access it via TimezoneFromContext without knowing about
// identity packages or any caller-specific key.
//
// The value is read dynamically on every request — not at construction time —
// so different callers with different timezones in context are handled correctly.
func WithTimezoneFromContext(key any) Option {
	if key == nil {
		panic("session: WithTimezoneFromContext: key must not be nil")
	}
	return func(d *decoratedService) { d.timezoneKey = key }
}

func Wrap(base session.Service, opts ...Option) session.Service {
	d := &decoratedService{
		base:         base,
		persistUser:  true,
		persistAgent: true,
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

func (d *decoratedService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	return d.base.Create(ctx, req)
}

func (d *decoratedService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	return d.base.Delete(ctx, req)
}

func (d *decoratedService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	return d.base.List(ctx, req)
}

func (d *decoratedService) AppendEvent(ctx context.Context, sess session.Session, event *session.Event) error {
	if event == nil {
		return nil
	}

	isUser := event.Author == "user" || event.Author == "human"
	if isUser && !d.persistUser {
		return nil
	}
	if !isUser && !d.persistAgent {
		return nil
	}

	return d.base.AppendEvent(ctx, sess, event)
}

func (d *decoratedService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	// Bridge external timezone key → TimezoneKey BEFORE calling base.Get.
	// The base implementation (e.g. zep.fetchHistory) reads TimezoneKey from
	// context while fetching — bridging after base.Get would be too late.
	if d.timezoneKey != nil {
		if tz, ok := ctx.Value(d.timezoneKey).(string); ok && tz != "" {
			ctx = context.WithValue(ctx, TimezoneKey, tz)
		}
	}

	resp, err := d.base.Get(ctx, req)
	if err != nil {
		return nil, err
	}

	sess := resp.Session
	if sess == nil {
		return resp, nil
	}

	var assembled []*session.Event
	for e := range sess.Events().All() {
		assembled = append(assembled, e)
	}

	if d.policy != "" {
		assembled = append(assembled, d.newSystemEvent("policy", d.policy))
	}

	return &session.GetResponse{
		Session: &decoratedSession{
			Session: sess,
			events:  assembled,
		},
	}, nil
}

func (d *decoratedService) newSystemEvent(category, content string) *session.Event {
	evt := session.NewEvent(category)
	evt.Author = "system"

	evt.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText(content, genai.Role("model")),
	}

	return evt
}

type decoratedSession struct {
	session.Session
	events []*session.Event
}

func (ds *decoratedSession) Events() session.Events {
	return decoratedEvents(ds.events)
}

type decoratedEvents []*session.Event

func (e decoratedEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, evt := range e {
			if !yield(evt) {
				return
			}
		}
	}
}

func (e decoratedEvents) Len() int { return len(e) }

func (e decoratedEvents) At(i int) *session.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}
