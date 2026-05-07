package zep

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"go.naturallyfunny.dev/adk/session"
)

type Option func(*SessionService)

type SessionService struct {
	client                   *client.Client
	agentName                string
	userDisplayName          string
	contextHistoryLength     int
	includeKnowledge         bool
	knowledgeContextTemplate *string
}

func WithContextHistoryLength(n int) Option {
	return func(s *SessionService) {
		s.contextHistoryLength = n
	}
}

func WithUserDisplayName(name string) Option {
	return func(s *SessionService) {
		s.userDisplayName = name
	}
}

func WithKnowledgeContext(contextTemplateID *string) Option {
	return func(s *SessionService) {
		s.includeKnowledge = true
		s.knowledgeContextTemplate = contextTemplateID
	}
}

func NewSessionService(client *client.Client, agentName string, opts ...Option) *SessionService {
	s := &SessionService{
		client:               client,
		agentName:            agentName,
		contextHistoryLength: 0,
		includeKnowledge:     false,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *SessionService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if err := s.ensureUser(ctx, req.UserID); err != nil {
		return nil, fmt.Errorf("zep ensure user: %w", err)
	}
	if _, err := s.client.Thread.Create(ctx, &zep.CreateThreadRequest{
		ThreadID: req.SessionID,
		UserID:   req.UserID,
	}); err != nil {
		return nil, fmt.Errorf("zep create thread: %w", err)
	}
	return &adksession.CreateResponse{
		Session: &zepSession{
			id:     req.SessionID,
			userID: req.UserID,
			app:    req.AppName,
		},
	}, nil
}

func (s *SessionService) ensureUser(ctx context.Context, userID string) error {
	_, err := s.client.User.Get(ctx, userID)
	if err == nil {
		return nil
	}
	var notFound *zep.NotFoundError
	if !errors.As(err, &notFound) {
		return err
	}
	_, err = s.client.User.Add(ctx, &zep.CreateUserRequest{UserID: userID})
	return err
}

func (s *SessionService) mapRoleToZep(role string) zep.RoleType {
	switch role {
	case "user", "human":
		return zep.RoleTypeUserRole
	case "system":
		return zep.RoleTypeSystemRole
	default:
		return zep.RoleTypeAssistantRole
	}
}

func (s *SessionService) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if event == nil {
		return nil
	}

	// Always update in-memory state: the ADK flow loop reads sess.Events() on
	// every iteration, so function-call and function-response events must be
	// visible even though they carry no text and are not persisted to Zep.
	if impl, ok := sess.(*zepSession); ok {
		impl.events = append(impl.events, event)
	}

	var contentStr string
	if event.Content != nil {
		for _, part := range event.Content.Parts {
			if part.Text != "" {
				contentStr += part.Text
			}
		}
	}
	if contentStr == "" {
		return nil
	}

	zepRole := s.mapRoleToZep(event.Author)

	msg := &zep.Message{
		Role:    zepRole,
		Content: contentStr,
	}
	switch zepRole {
	case zep.RoleTypeAssistantRole:
		msg.Name = &s.agentName
	case zep.RoleTypeUserRole:
		name := s.userDisplayName
		if name == "" {
			name = sess.UserID()
		}
		msg.Name = &name
	}

	_, err := s.client.Thread.AddMessages(ctx, sess.ID(), &zep.AddThreadMessagesRequest{
		Messages: []*zep.Message{msg},
	})
	return err
}

func (s *SessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	sess := &zepSession{
		id:     req.SessionID,
		userID: req.UserID,
		app:    req.AppName,
	}

	events, lastTime, err := s.buildContext(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	sess.events = events
	sess.lastUpdate = lastTime

	return &adksession.GetResponse{Session: sess}, nil
}

func (s *SessionService) buildContext(ctx context.Context, sessionID string) ([]*adksession.Event, time.Time, error) {
	var events []*adksession.Event

	if s.includeKnowledge {
		if knowledge := s.fetchKnowledge(ctx, sessionID, s.knowledgeContextTemplate); knowledge != "" {
			events = append(events, s.newSystemEvent("knowledge", knowledge))
		}
	}

	// fetchHistory is always called: it verifies the thread exists in Zep,
	// which lets the ADK runner trigger Create (via autoCreateSession) when needed.
	history, lastTime, err := s.fetchHistory(ctx, sessionID)
	if err != nil {
		return nil, time.Time{}, err
	}
	events = append(events, history...)

	return events, lastTime, nil
}

func (s *SessionService) fetchKnowledge(ctx context.Context, sessionID string, templateID *string) string {
	resp, err := s.client.Thread.GetUserContext(ctx, sessionID, &zep.ThreadGetUserContextRequest{
		TemplateID: templateID,
	})
	if err != nil {
		return ""
	}

	if resp == nil || resp.GetContext() == nil {
		return ""
	}

	ctxStr := *resp.GetContext()
	if ctxStr == "" {
		return ""
	}

	return fmt.Sprintf("[KNOWLEDGE]\n%s\n[/KNOWLEDGE]", ctxStr)
}

func (s *SessionService) fetchHistory(ctx context.Context, sessionID string) ([]*adksession.Event, time.Time, error) {
	lastn := s.contextHistoryLength
	if lastn == 0 {
		lastn = 1 // minimum fetch to verify the thread exists in Zep
	}

	resp, err := s.client.Thread.Get(ctx, sessionID, &zep.ThreadGetRequest{
		Lastn: zep.Int(lastn),
	})
	if err != nil {
		return nil, time.Time{}, err
	}

	if s.contextHistoryLength == 0 {
		return nil, time.Time{}, nil // thread verified; caller requested no history
	}

	loc := locationFromContext(ctx)

	var events []*adksession.Event
	var lastTime time.Time
	for _, msg := range resp.GetMessages() {
		if msg == nil {
			continue
		}

		role := s.unmapRole(msg.Role)
		evt := adksession.NewEvent(derefOrEmpty(msg.UUID))
		evt.Author = role

		contentRole := "model"
		if role == "user" {
			contentRole = "user"
		}

		content := msg.Content

		// Prepend a unified [timestamp name] header to every message so the LLM
		// can ground itself temporally and identify speakers without relying on
		// role types. Role types are misleading in multi-agent sessions where an
		// agent holds the "user" role when calling another agent — the name field
		// is the reliable identity signal. Timezone abbreviation is omitted: all
		// timestamps are already localised to the same user timezone per-request,
		// so the abbreviation is redundant noise for the LLM.
		// Format: [2026-05-07 23:57 Ava] or [2026-05-07 23:57] when name is
		// absent. Skip prefix entirely when CreatedAt is absent or unparseable.
		if msg.CreatedAt != nil {
			if t, ok := parseTimestamp(*msg.CreatedAt); ok {
				local := t.In(loc)
				header := local.Format("2006-01-02 15:04")
				if name := derefOrEmpty(msg.Name); name != "" {
					header = header + " " + name
				}
				content = fmt.Sprintf("[%s] %s", header, content)
				if t.After(lastTime) {
					lastTime = t
				}
			}
		}

		evt.LLMResponse = model.LLMResponse{
			Content: genai.NewContentFromText(content, genai.Role(contentRole)),
		}
		events = append(events, evt)
	}

	return events, lastTime, nil
}

func (s *SessionService) unmapRole(role zep.RoleType) string {
	if role == zep.RoleTypeUserRole {
		return "user"
	}
	return s.agentName
}

func (s *SessionService) newSystemEvent(category, content string) *adksession.Event {
	evt := adksession.NewEvent(category)
	evt.Author = "system"

	evt.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText(content, genai.Role("model")),
	}

	return evt
}

func (s *SessionService) List(_ context.Context, _ *adksession.ListRequest) (*adksession.ListResponse, error) {
	return &adksession.ListResponse{}, nil
}

func (s *SessionService) Delete(_ context.Context, _ *adksession.DeleteRequest) error {
	return nil
}

// locationFromContext resolves *time.Location from context using
// session.TimezoneKey set by the decorator. Always returns non-nil:
// time.UTC is the fallback when timezone is absent or unrecognised.
// The empty-string guard lives in session.TimezoneFromContext (ok && v != ""),
// so time.LoadLocation is never called with an empty string here.
func locationFromContext(ctx context.Context) *time.Location {
	tz, ok := session.TimezoneFromContext(ctx)
	if !ok {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// parseTimestamp tries RFC3339Nano then RFC3339. Returns false when neither
// matches, allowing callers to skip the timestamp prefix gracefully rather
// than surface a parse error to the LLM.
func parseTimestamp(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type zepSession struct {
	id         string
	userID     string
	app        string
	events     []*adksession.Event
	lastUpdate time.Time // zero if no timestamped messages were fetched
}

func (z *zepSession) ID() string     { return z.id }
func (z *zepSession) AppName() string { return z.app }
func (z *zepSession) UserID() string { return z.userID }

// LastUpdateTime returns the timestamp of the most recent message fetched from
// Zep. Returns zero time.Time{} when no messages carry a parseable CreatedAt
// (e.g. a brand-new or empty thread).
func (z *zepSession) LastUpdateTime() time.Time { return z.lastUpdate }

func (z *zepSession) State() adksession.State   { return zepState{} }
func (z *zepSession) Events() adksession.Events { return zepEvents(z.events) }

type zepState struct{}

func (zepState) Get(_ string) (any, error)   { return nil, adksession.ErrStateKeyNotExist }
func (zepState) Set(_ string, _ any) error   { return nil }
func (zepState) All() iter.Seq2[string, any] { return func(func(string, any) bool) {} }

type zepEvents []*adksession.Event

func (e zepEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, evt := range e {
			if !yield(evt) {
				return
			}
		}
	}
}

func (e zepEvents) Len() int { return len(e) }

func (e zepEvents) At(i int) *adksession.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}
