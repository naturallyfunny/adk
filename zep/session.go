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
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type Option func(*SessionService)

type SessionService struct {
	client                   *client.Client
	agentName                string
	conversationHistory      int
	includeKnowledge         bool
	knowledgeContextTemplate *string
}

func WithConversationHistory(n int) Option {
	return func(s *SessionService) {
		s.conversationHistory = n
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
		client:              client,
		agentName:           agentName,
		conversationHistory: 0,
		includeKnowledge:    false,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *SessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if err := s.ensureUser(ctx, req.UserID); err != nil {
		return nil, fmt.Errorf("zep ensure user: %w", err)
	}
	if _, err := s.client.Thread.Create(ctx, &zep.CreateThreadRequest{
		ThreadID: req.SessionID,
		UserID:   req.UserID,
	}); err != nil {
		return nil, fmt.Errorf("zep create thread: %w", err)
	}
	return &session.CreateResponse{
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

func (s *SessionService) AppendEvent(ctx context.Context, sess session.Session, event *session.Event) error {
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
		userID := sess.UserID()
		msg.Name = &userID
	}

	_, err := s.client.Thread.AddMessages(ctx, sess.ID(), &zep.AddThreadMessagesRequest{
		Messages: []*zep.Message{msg},
	})
	return err
}

func (s *SessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	sess := &zepSession{
		id:     req.SessionID,
		userID: req.UserID,
		app:    req.AppName,
	}

	events, err := s.buildContext(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	sess.events = events

	return &session.GetResponse{Session: sess}, nil
}

func (s *SessionService) buildContext(ctx context.Context, sessionID string) ([]*session.Event, error) {
	var events []*session.Event

	if s.includeKnowledge {
		if knowledge := s.fetchKnowledge(ctx, sessionID, s.knowledgeContextTemplate); knowledge != "" {
			events = append(events, s.newSystemEvent("knowledge", knowledge))
		}
	}

	// fetchHistory is always called: it verifies the thread exists in Zep,
	// which lets the ADK runner trigger Create (via autoCreateSession) when needed.
	history, err := s.fetchHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	events = append(events, history...)

	return events, nil
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

func (s *SessionService) fetchHistory(ctx context.Context, sessionID string) ([]*session.Event, error) {
	lastn := s.conversationHistory
	if lastn == 0 {
		lastn = 1 // minimum fetch to verify the thread exists in Zep
	}

	resp, err := s.client.Thread.Get(ctx, sessionID, &zep.ThreadGetRequest{
		Lastn: zep.Int(lastn),
	})
	if err != nil {
		return nil, err
	}

	if s.conversationHistory == 0 {
		return nil, nil // thread verified; caller requested no history
	}

	var events []*session.Event
	for _, msg := range resp.GetMessages() {
		if msg == nil {
			continue
		}

		role := s.unmapRole(msg.Role)
		evt := session.NewEvent(derefOrEmpty(msg.UUID))
		evt.Author = role

		contentRole := "model"
		if role == "user" {
			contentRole = "user"
		}

		content := msg.Content
		if msg.Name != nil && *msg.Name != "" && *msg.Name != s.agentName {
			content = fmt.Sprintf("<speaker name=%q/>\n%s", *msg.Name, content)
		}
		evt.LLMResponse = model.LLMResponse{
			Content: genai.NewContentFromText(content, genai.Role(contentRole)),
		}
		events = append(events, evt)
	}

	return events, nil
}

func (s *SessionService) unmapRole(role zep.RoleType) string {
	if role == zep.RoleTypeUserRole {
		return "user"
	}
	return s.agentName
}

func (s *SessionService) newSystemEvent(category, content string) *session.Event {
	evt := session.NewEvent(category)
	evt.Author = "system"

	evt.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText(content, genai.Role("model")),
	}

	return evt
}

func (s *SessionService) List(_ context.Context, _ *session.ListRequest) (*session.ListResponse, error) {
	return &session.ListResponse{}, nil
}

func (s *SessionService) Delete(_ context.Context, _ *session.DeleteRequest) error {
	return nil
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type zepSession struct {
	id     string
	userID string
	app    string
	events []*session.Event
}

func (z *zepSession) ID() string                { return z.id }
func (z *zepSession) AppName() string           { return z.app }
func (z *zepSession) UserID() string            { return z.userID }
func (z *zepSession) LastUpdateTime() time.Time { return time.Now() }
func (z *zepSession) State() session.State      { return zepState{} }
func (z *zepSession) Events() session.Events    { return zepEvents(z.events) }

type zepState struct{}

func (zepState) Get(_ string) (any, error)   { return nil, session.ErrStateKeyNotExist }
func (zepState) Set(_ string, _ any) error   { return nil }
func (zepState) All() iter.Seq2[string, any] { return func(func(string, any) bool) {} }

type zepEvents []*session.Event

func (e zepEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, evt := range e {
			if !yield(evt) {
				return
			}
		}
	}
}

func (e zepEvents) Len() int { return len(e) }

func (e zepEvents) At(i int) *session.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}
