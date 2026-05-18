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

	timeHarnessEnabled     bool
	timeHarnessFromContext bool
	timeHarnessStaticLoc   *time.Location // nil when FromContext, set otherwise

	// threadGet is a test hook; when non-nil it replaces Thread.Get in fetchHistory.
	threadGet func(ctx context.Context, sessionID string, lastn int) ([]*zep.Message, error)
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

// WithTimeHarness enables time-awareness with a static IANA timezone.
// Empty string means UTC. An invalid timezone PANICS at construction
// (fail-fast — invalid timezone is always a programmer error).
//
// When enabled:
//   - History message prefix becomes [YYYY-MM-DD HH:MM Name] instead of [Name]
//   - A current_time anchor event is appended after history
//   - Any message with unparseable CreatedAt causes Get to return an error
func WithTimeHarness(timezone string) Option {
	return func(s *SessionService) {
		s.timeHarnessEnabled = true
		s.timeHarnessFromContext = false
		if timezone == "" {
			s.timeHarnessStaticLoc = time.UTC
			return
		}
		loc, err := time.LoadLocation(timezone)
		if err != nil {
			panic(fmt.Sprintf("zep: WithTimeHarness: invalid timezone %q: %v", timezone, err))
		}
		s.timeHarnessStaticLoc = loc
	}
}

// WithTimeHarnessFromContext enables time-awareness with per-request
// timezone resolution. Reads session.TimezoneKey from context at fetch
// time. If the key is absent or holds an invalid IANA timezone at
// request time, Get returns an error.
//
// Intended for multi-user public agents where the end-user's timezone
// arrives via the session decorator's WithTimezoneFromContext bridge.
//
// If WithTimeHarness is also called, last call wins.
func WithTimeHarnessFromContext() Option {
	return func(s *SessionService) {
		s.timeHarnessEnabled = true
		s.timeHarnessFromContext = true
		s.timeHarnessStaticLoc = nil
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

	if len(history) > 0 {
		events = append(events, s.newSystemEvent("format_preamble", s.buildPreamble()))
		events = append(events, history...)
		events = append(events, s.newSystemEvent("format_postamble", s.buildPostamble()))
	}

	if s.timeHarnessEnabled {
		loc, err := s.resolveLocation(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		events = append(events, s.newSystemEvent("current_time", s.buildCurrentTimeAnchor(loc, lastTime)))
	}

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

	return fmt.Sprintf("<system-retrieved-related-knowldege>\n%s\n</system-retrieved-related-knowledge>", ctxStr)
}

func (s *SessionService) fetchHistory(ctx context.Context, sessionID string) ([]*adksession.Event, time.Time, error) {
	lastn := s.contextHistoryLength
	if lastn == 0 {
		lastn = 1 // minimum fetch to verify the thread exists in Zep
	}

	loc, err := s.resolveLocation(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}

	var rawMsgs []*zep.Message

	if s.threadGet != nil {
		rawMsgs, err = s.threadGet(ctx, sessionID, lastn)
		if err != nil {
			return nil, time.Time{}, err
		}
	} else {
		resp, rerr := s.client.Thread.Get(ctx, sessionID, &zep.ThreadGetRequest{Lastn: zep.Int(lastn)})
		if rerr != nil {
			return nil, time.Time{}, rerr
		}
		if s.contextHistoryLength == 0 {
			return nil, time.Time{}, nil // thread verified; caller requested no history
		}
		rawMsgs = resp.GetMessages()
	}

	var events []*adksession.Event
	var lastTime time.Time
	for _, msg := range rawMsgs {
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

		name := derefOrEmpty(msg.Name)
		if name == "" {
			name = role // fallback so prefix is never bare []
		}

		content := msg.Content

		if s.timeHarnessEnabled {
			if msg.CreatedAt == nil {
				return nil, time.Time{}, fmt.Errorf(
					"zep: TimeHarness enabled but message %s has nil CreatedAt",
					derefOrEmpty(msg.UUID))
			}
			t, ok := parseTimestamp(*msg.CreatedAt)
			if !ok {
				return nil, time.Time{}, fmt.Errorf(
					"zep: TimeHarness enabled but message %s has unparseable CreatedAt: %q",
					derefOrEmpty(msg.UUID), *msg.CreatedAt)
			}
			local := t.In(loc)
			header := fmt.Sprintf("%s %s", local.Format("2006-01-02 15:04"), name)
			content = fmt.Sprintf("[%s] %s", header, content)
			if t.After(lastTime) {
				lastTime = t
			}
		} else {
			content = fmt.Sprintf("[%s] %s", name, content)
		}

		evt.LLMResponse = model.LLMResponse{
			Content: genai.NewContentFromText(content, genai.Role(contentRole)),
		}
		events = append(events, evt)
	}

	return events, lastTime, nil
}

// resolveLocation returns the timezone for header formatting.
// Returns (nil, nil) when TimeHarness is disabled — callers must
// branch on that case. Returns an error when TimeHarness is enabled
// but the timezone is unresolvable.
func (s *SessionService) resolveLocation(ctx context.Context) (*time.Location, error) {
	if !s.timeHarnessEnabled {
		return nil, nil
	}
	if !s.timeHarnessFromContext {
		return s.timeHarnessStaticLoc, nil
	}
	tz, ok := session.TimezoneFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("zep: TimeHarnessFromContext active but session.TimezoneKey absent in context")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("zep: TimeHarnessFromContext: invalid timezone %q: %w", tz, err)
	}
	return loc, nil
}

func (s *SessionService) buildPreamble() string {
	if s.timeHarnessEnabled {
		return `[MESSAGES_HISTORY_FORMAT]
The conversation history below uses this format:

  [YYYY-MM-DD HH:MM Name] raw message content

The bracketed prefix is system-provided metadata for time-awareness and
speaker identification. All timestamps are already localized to the user's
local time — you do not need to think about timezones; what you see is
always the user's local time.

IMPORTANT: Never produce responses with this bracketed prefix. Respond
with raw message content only.
[/MESSAGES_HISTORY_FORMAT]`
	}
	return `[MESSAGES_HISTORY_FORMAT]
The conversation history below uses this format:

  [Name] raw message content

The bracketed prefix is system-provided metadata for speaker
identification.

IMPORTANT: Never produce responses with this bracketed prefix. Respond
with raw message content only.
[/MESSAGES_HISTORY_FORMAT]`
}

func (s *SessionService) buildPostamble() string {
	if s.timeHarnessEnabled {
		return `[CRITICAL_FORMAT_REMINDER]
CRITICAL: Do not respond using the [YYYY-MM-DD HH:MM Name] format. Output
raw message content only.
[/CRITICAL_FORMAT_REMINDER]`
	}
	return `[CRITICAL_FORMAT_REMINDER]
CRITICAL: Do not respond using the [Name] format. Output raw message
content only.
[/CRITICAL_FORMAT_REMINDER]`
}

func (s *SessionService) buildCurrentTimeAnchor(loc *time.Location, lastTime time.Time) string {
	now := time.Now().In(loc)
	nowStr := now.Format("2006-01-02 15:04")

	if lastTime.IsZero() {
		return fmt.Sprintf("[CURRENT_TIME]\nCurrent date and time: %s\n[/CURRENT_TIME]", nowStr)
	}

	elapsed := time.Since(lastTime)
	return fmt.Sprintf(
		"[CURRENT_TIME]\nCurrent date and time: %s\nTime since previous message: %s\n[/CURRENT_TIME]",
		nowStr, formatElapsed(elapsed))
}

// formatElapsed returns a human-readable duration.
// Pluralization is always-plural by design (e.g. "1 minutes") — LLMs
// tolerate this and avoiding plural/singular branching keeps the
// function simple.
//
//	< 1 minute  → "N seconds"
//	< 60 min    → "N minutes"
//	< 24 hours  → "H hours M minutes"
//	≥ 24 hours  → "D days H hours M minutes"
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%d hours %d minutes", h, m)
	}
	totalMinutes := int(d.Minutes())
	days := totalMinutes / (24 * 60)
	remaining := totalMinutes - days*24*60
	h := remaining / 60
	m := remaining - h*60
	return fmt.Sprintf("%d days %d hours %d minutes", days, h, m)
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

// parseTimestamp tries RFC3339Nano then RFC3339. Returns false when neither
// matches. When TimeHarness is enabled, fetchHistory treats false as a
// hard error (invariant violation). When TimeHarness is disabled, the
// caller path does not invoke parseTimestamp at all.
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

func (z *zepSession) ID() string      { return z.id }
func (z *zepSession) AppName() string { return z.app }
func (z *zepSession) UserID() string  { return z.userID }

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
