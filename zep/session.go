package zep

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
	"github.com/getzep/zep-go/v3/option"
	threadclient "github.com/getzep/zep-go/v3/thread/client"
	userclient "github.com/getzep/zep-go/v3/user"

	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// userClient is the slice of zep user functionality this service needs.
type userClient interface {
	Get(ctx context.Context, userID string, opts ...option.RequestOption) (*zep.User, error)
	Add(ctx context.Context, request *zep.CreateUserRequest, opts ...option.RequestOption) (*zep.User, error)
}

// threadClient is the slice of zep thread functionality this service needs.
type threadClient interface {
	Create(ctx context.Context, request *zep.CreateThreadRequest, opts ...option.RequestOption) (*zep.Thread, error)
	AddMessages(ctx context.Context, threadID string, request *zep.AddThreadMessagesRequest, opts ...option.RequestOption) (*zep.AddThreadMessagesResponse, error)
	Get(ctx context.Context, threadID string, request *zep.ThreadGetRequest, opts ...option.RequestOption) (*zep.MessageListResponse, error)
	GetUserContext(ctx context.Context, threadID string, request *zep.ThreadGetUserContextRequest, opts ...option.RequestOption) (*zep.ThreadContextResponse, error)
}

type knowledgeConfig struct {
	templateID *string
}

// Zone describes where the time harness resolves the user's timezone.
// Use StaticZone or ZoneFromContext to create one.
type Zone struct {
	resolve func(context.Context) (*time.Location, error)
}

type timeHarnessConfig struct {
	zone *Zone
}

type SessionService struct {
	thread                threadClient
	user                  userClient
	agentName             string
	userDisplayName       string
	messagesHistoryLength int
	sessionInstruction    string
	knowledge             *knowledgeConfig
	timeHarness           *timeHarnessConfig
}

type Option func(*SessionService)

func NewSessionService(c *client.Client, agentName string, opts ...Option) *SessionService {
	s := &SessionService{
		agentName:             agentName,
		messagesHistoryLength: 0,
	}
	if c != nil {
		s.thread = c.Thread
		s.user = c.User
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func WithMessagesHistoryLength(n int) Option {
	return func(s *SessionService) {
		s.messagesHistoryLength = n
	}
}

func WithUserDisplayName(name string) Option {
	return func(s *SessionService) {
		s.userDisplayName = name
	}
}

func WithSessionInstruction(instruction string) Option {
	return func(s *SessionService) {
		s.sessionInstruction = instruction
	}
}

func WithKnowledgeContext(contextTemplateID *string) Option {
	return func(s *SessionService) {
		s.knowledge = &knowledgeConfig{templateID: contextTemplateID}
	}
}

// StaticZone returns a zone backed by a fixed IANA timezone.
// Empty string means UTC. Invalid timezone names panic because they are
// programmer configuration errors.
func StaticZone(timezone string) *Zone {
	if timezone == "" {
		return &Zone{
			resolve: func(context.Context) (*time.Location, error) {
				return time.UTC, nil
			},
		}
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		panic(fmt.Sprintf("zep: StaticZone: invalid timezone %q: %v", timezone, err))
	}
	return &Zone{
		resolve: func(context.Context) (*time.Location, error) {
			return loc, nil
		},
	}
}

type timezoneContextKey struct{}

// ContextWithTimezone returns a child context carrying an IANA timezone string
// for ZoneFromContext.
func ContextWithTimezone(ctx context.Context, timezone string) context.Context {
	return context.WithValue(ctx, timezoneContextKey{}, timezone)
}

// ZoneFromContext configures the time harness to resolve the timezone from the
// request context. Use ContextWithTimezone to provide the timezone per request.
func ZoneFromContext() *Zone {
	return &Zone{
		resolve: func(ctx context.Context) (*time.Location, error) {
			tz, _ := ctx.Value(timezoneContextKey{}).(string)
			if tz == "" {
				return nil, fmt.Errorf("zep: ZoneFromContext active but timezone absent or empty")
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return nil, fmt.Errorf("zep: ZoneFromContext: invalid timezone %q: %w", tz, err)
			}
			return loc, nil
		},
	}
}
// WithTimeHarness enables time-awareness using the provided zone source.
// A nil zone enables time-awareness without timezone conversion; timestamps are
// formatted in their parsed timezone, and the current-time anchor uses UTC.
//
// When enabled:
//   - History message prefix becomes [YYYY-MM-DD HH:MM Name] instead of [Name]
//   - A current_time anchor event is appended after history
//   - Any message with unparseable CreatedAt causes Get to return an error
func WithTimeHarness(zone *Zone) Option {
	if zone != nil && zone.resolve == nil {
		panic("zep: WithTimeHarness: zone must be created by StaticZone or ZoneFromContext")
	}
	return func(s *SessionService) {
		s.timeHarness = &timeHarnessConfig{zone: zone}
	}
}

// isNotFound reports whether err is (or wraps) a Zep NotFound response.
func isNotFound(err error) bool {
	var nf *zep.NotFoundError
	return errors.As(err, &nf)
}

func (s *SessionService) ensureUser(ctx context.Context, userID string) error {
	_, err := s.user.Get(ctx, userID)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	_, err = s.user.Add(ctx, &zep.CreateUserRequest{UserID: userID})
	return err
}

// ErrSessionOwnerMismatch is returned by Get when the requesting user is not
// the owner of the requested thread. Callers (e.g. an HTTP handler) should map
// it to 403 Forbidden.
var ErrSessionOwnerMismatch = errors.New("zep: session does not belong to user")

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// verifyThreadOwner enforces that an existing thread belongs to the requesting
// user. It runs only when expectedUserID is non-empty (an authenticated
// request carries a user identity); requests without an identity — internal
// callers, tests — are not gated.
//
// It is fail-closed: when a user identity is present but Zep does not report a
// thread owner, ownership cannot be confirmed and access is denied. This is the
// security path, so an unconfirmable owner is treated as a mismatch rather than
// waved through.
func verifyThreadOwner(resp *zep.MessageListResponse, expectedUserID string) error {
	if expectedUserID == "" {
		return nil
	}
	if derefOrEmpty(resp.GetUserID()) != expectedUserID {
		return ErrSessionOwnerMismatch
	}
	return nil
}

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

func (s *SessionService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if err := s.ensureUser(ctx, req.UserID); err != nil {
		return nil, fmt.Errorf("zep ensure user: %w", err)
	}

	// Ownership guard. The ADK runner calls Create on ANY error from Get —
	// including ErrSessionOwnerMismatch — when AutoCreateSession is enabled, so a
	// cross-user request that Get rejected would otherwise fall through to
	// thread.Create against someone else's thread, whose server-side outcome
	// (conflict, idempotent success, or owner rebind) is undefined. Decide the
	// outcome here rather than depend on Zep: an existing thread must belong to
	// the requester; a brand-new thread (NotFound) proceeds normally.
	existing, err := s.thread.Get(ctx, req.SessionID, &zep.ThreadGetRequest{Lastn: zep.Int(1)})
	switch {
	case err == nil:
		if err := verifyThreadOwner(existing, req.UserID); err != nil {
			return nil, err
		}
	case isNotFound(err):
		// Thread does not exist yet — safe to create below.
	default:
		return nil, fmt.Errorf("zep create thread (ownership precheck): %w", err)
	}

	if _, err := s.thread.Create(ctx, &zep.CreateThreadRequest{
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

func (s *SessionService) roleFromADK(role string) zep.RoleType {
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

	zepRole := s.roleFromADK(event.Author)

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

	_, err := s.thread.AddMessages(ctx, sess.ID(), &zep.AddThreadMessagesRequest{
		Messages: []*zep.Message{msg},
	})
	return err
}

// timeHarnessEnabled reports whether time-awareness is configured.
func (s *SessionService) timeHarnessEnabled() bool {
	return s.timeHarness != nil
}

// resolveLocation returns the timezone for header formatting. A nil location
// means timestamps should be formatted in their parsed timezone. It assumes
// time-awareness is enabled.
func (s *SessionService) resolveLocation(ctx context.Context) (*time.Location, error) {
	if s.timeHarness.zone == nil {
		return nil, nil
	}
	return s.timeHarness.zone.resolve(ctx)
}

func (s *SessionService) roleToADK(role zep.RoleType) string {
	if role == zep.RoleTypeUserRole {
		return "user"
	}
	return s.agentName
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

func (s *SessionService) fetchHistory(ctx context.Context, sessionID, expectedUserID string) ([]*adksession.Event, time.Time, error) {
	lastn := s.messagesHistoryLength
	if lastn == 0 {
		lastn = 1 // minimum fetch to verify the thread exists in Zep
	}

	var loc *time.Location
	if s.timeHarnessEnabled() {
		var err error
		loc, err = s.resolveLocation(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
	}

	resp, err := s.thread.Get(ctx, sessionID, &zep.ThreadGetRequest{Lastn: zep.Int(lastn)})
	if err != nil {
		return nil, time.Time{}, err
	}

	// Ownership guard: an existing thread must belong to the requesting user.
	// A nonexistent thread returns NotFound above (before this point), so the
	// AutoCreateSession path — which binds the thread to req.UserID — is never
	// blocked here. The guard runs before the messagesHistoryLength == 0 early
	// return so the verify-only path is protected too.
	if err := verifyThreadOwner(resp, expectedUserID); err != nil {
		return nil, time.Time{}, err
	}

	if s.messagesHistoryLength == 0 {
		return nil, time.Time{}, nil // thread verified; caller requested no history
	}

	var events []*adksession.Event
	var lastTime time.Time
	for _, msg := range resp.GetMessages() {
		if msg == nil {
			continue
		}

		role := s.roleToADK(msg.Role)
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

		if s.timeHarnessEnabled() {
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
			local := t
			if loc != nil {
				local = t.In(loc)
			}
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

func (s *SessionService) fetchKnowledge(ctx context.Context, sessionID string, templateID *string) string {
	resp, err := s.thread.GetUserContext(ctx, sessionID, &zep.ThreadGetUserContextRequest{
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

	return fmt.Sprintf("[SYSTEM_RETRIEVED_RELATED_KNOWLEDGE]\n%s\n[/SYSTEM_RETRIEVED_RELATED_KNOWLEDGE]", ctxStr)
}

func (s *SessionService) newSystemEvent(category, content string) *adksession.Event {
	evt := adksession.NewEvent(category)
	evt.Author = "system"

	evt.LLMResponse = model.LLMResponse{
		Content: genai.NewContentFromText(content, genai.Role("model")),
	}

	return evt
}

func (s *SessionService) buildMessagesHistoryPreamble() string {
	if s.timeHarnessEnabled() {
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

func (s *SessionService) buildMessagesHistoryPostamble() string {
	if s.timeHarnessEnabled() {
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

const timeAwarenessFraming = "You are time-aware: the time shown above is the authoritative " +
	"current local time. Rely on it whenever you need to know or reason about the " +
	"current date or time — you never need to look it up elsewhere or call a tool " +
	"to obtain it. Within this session — the conversation history above and the " +
	"current date and time shown here — all times are already in the user's local " +
	"timezone, so you do not need to convert or reason about timezones for them."

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

func (s *SessionService) buildCurrentTimeAnchor(loc *time.Location, lastTime time.Time) string {
	now := time.Now().UTC()
	if loc != nil {
		now = now.In(loc)
	}
	nowStr := now.Format("2006-01-02 15:04")

	if lastTime.IsZero() {
		return fmt.Sprintf("[CURRENT_TIME]\nCurrent date and time: %s\n\n%s\n[/CURRENT_TIME]", nowStr, timeAwarenessFraming)
	}

	elapsed := time.Since(lastTime)
	return fmt.Sprintf(
		"[CURRENT_TIME]\nCurrent date and time: %s\nTime since previous message: %s\n\n%s\n[/CURRENT_TIME]",
		nowStr, formatElapsed(elapsed), timeAwarenessFraming)
}

func (s *SessionService) buildContext(ctx context.Context, sessionID, expectedUserID string) ([]*adksession.Event, time.Time, error) {
	var events []*adksession.Event

	if s.sessionInstruction != "" {
		events = append(events, s.newSystemEvent("session_instruction", s.sessionInstruction))
	}

	if s.knowledge != nil {
		if knowledge := s.fetchKnowledge(ctx, sessionID, s.knowledge.templateID); knowledge != "" {
			events = append(events, s.newSystemEvent("knowledge", knowledge))
		}
	}

	// fetchHistory is always called: it verifies the thread exists in Zep,
	// which lets the ADK runner trigger Create (via autoCreateSession) when needed.
	// It also enforces the ownership guard against expectedUserID.
	history, lastTime, err := s.fetchHistory(ctx, sessionID, expectedUserID)
	if err != nil {
		return nil, time.Time{}, err
	}

	if len(history) > 0 {
		events = append(events, s.newSystemEvent("messages_history_preamble", s.buildMessagesHistoryPreamble()))
		events = append(events, history...)
		events = append(events, s.newSystemEvent("messages_history_postamble", s.buildMessagesHistoryPostamble()))
	}

	if s.timeHarnessEnabled() {
		loc, err := s.resolveLocation(ctx)
		if err != nil {
			return nil, time.Time{}, err
		}
		events = append(events, s.newSystemEvent("current_time", s.buildCurrentTimeAnchor(loc, lastTime)))
	}

	return events, lastTime, nil
}

func (s *SessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	sess := &zepSession{
		id:     req.SessionID,
		userID: req.UserID,
		app:    req.AppName,
	}

	events, lastTime, err := s.buildContext(ctx, req.SessionID, req.UserID)
	if err != nil {
		return nil, err
	}

	sess.events = events
	sess.lastUpdate = lastTime

	return &adksession.GetResponse{Session: sess}, nil
}

func (s *SessionService) List(_ context.Context, _ *adksession.ListRequest) (*adksession.ListResponse, error) {
	return &adksession.ListResponse{}, nil
}

func (s *SessionService) Delete(_ context.Context, _ *adksession.DeleteRequest) error {
	return nil
}

var (
	_ threadClient = (*threadclient.Client)(nil)
	_ userClient   = (*userclient.Client)(nil)
)
