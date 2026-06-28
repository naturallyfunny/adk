package zep

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
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

// TZResolver resolves the user's timezone for the time harness.
// Use StaticTZ or TZFromContext to create one.
type TZResolver struct {
	resolve func(context.Context) (*time.Location, error)
}

type timeHarnessConfig struct {
	zoneResolver *TZResolver
}

// Speaker identifies the actor behind an inbound user-role turn: a display name
// and the Zep role under which the message is persisted. An empty Role means
// the ADK-derived role (user) is kept unchanged.
type Speaker struct {
	Name string
	Role zep.RoleType
}

// SpeakerResolver resolves the Speaker attributed to inbound user-role turns.
// Use Static or SpeakerFromContext to create one.
type SpeakerResolver struct {
	resolve func(context.Context) (Speaker, error)
}

// userClient is the slice of zep user functionality this service needs.
type userClient interface {
	Get(ctx context.Context, userID string, opts ...option.RequestOption) (*zep.User, error)
	Add(ctx context.Context, request *zep.CreateUserRequest, opts ...option.RequestOption) (*zep.User, error)
}
var _ userClient = (*userclient.Client)(nil)

// threadClient is the slice of zep thread functionality this service needs.
type threadClient interface {
	Create(ctx context.Context, request *zep.CreateThreadRequest, opts ...option.RequestOption) (*zep.Thread, error)
	AddMessages(ctx context.Context, threadID string, request *zep.AddThreadMessagesRequest, opts ...option.RequestOption) (*zep.AddThreadMessagesResponse, error)
	Get(ctx context.Context, threadID string, request *zep.ThreadGetRequest, opts ...option.RequestOption) (*zep.MessageListResponse, error)
}
var _ threadClient = (*threadclient.Client)(nil)

type SessionService struct {
	threadClient     threadClient
	userClient       userClient
	speakerResolver  *SpeakerResolver
	msgHistoryLength int
	instructionKey   string
	timeHarness      *timeHarnessConfig
}

type Option func(*SessionService)

func NewSessionService(c *client.Client, opts ...Option) *SessionService {
	s := &SessionService{}
	if c != nil {
		s.threadClient = c.Thread
		s.userClient = c.User
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func WithMessageHistoryLength(n int) Option {
	return func(s *SessionService) {
		s.msgHistoryLength = n
	}
}

// Static returns a speaker resolver backed by a fixed Speaker value.
// An empty Name means inbound user turns fall back to the session's UserID.
// An empty Role keeps the ADK-derived role unchanged.
func Static(sp Speaker) *SpeakerResolver {
	return &SpeakerResolver{
		resolve: func(context.Context) (Speaker, error) {
			return sp, nil
		},
	}
}

type speakerContextKey struct{}

// WithSpeaker returns a child context carrying the Speaker for SpeakerFromContext.
func WithSpeaker(ctx context.Context, sp Speaker) context.Context {
	return context.WithValue(ctx, speakerContextKey{}, sp)
}

// SpeakerFromContext returns a speaker resolver that reads the inbound user-turn
// Speaker from the request context. Use WithSpeaker to provide the Speaker per
// request. AppendEvent returns an error when the Speaker is absent or has an empty
// Name, so a forgotten context does not silently mislabel the turn.
func SpeakerFromContext() *SpeakerResolver {
	return &SpeakerResolver{
		resolve: func(ctx context.Context) (Speaker, error) {
			sp, ok := ctx.Value(speakerContextKey{}).(Speaker)
			if !ok || sp.Name == "" {
				return Speaker{}, fmt.Errorf("zep: SpeakerFromContext active but speaker absent or name empty")
			}
			return sp, nil
		},
	}
}

// WithSpeakerResolver sets how inbound user-role turns are attributed in the Zep
// thread, using the provided speaker source. Without this option, user turns are
// attributed to the session's UserID.
func WithSpeakerResolver(r *SpeakerResolver) Option {
	if r == nil || r.resolve == nil {
		panic("zep: WithSpeakerResolver: resolver must be created by Static or SpeakerFromContext")
	}
	return func(s *SessionService) {
		s.speakerResolver = r
	}
}

// WithInstruction registers the state key under which a combined session
// instruction block is written during Get. The block includes a message format
// section whenever history is present, and a current-time section when the time
// harness is enabled. The consumer includes {key?} (or {key}) in their agent
// instruction for the placeholder to be resolved by the ADK runner. When not
// set, no instruction is written to state.
func WithInstruction(key string) Option {
	return func(s *SessionService) {
		s.instructionKey = key
	}
}

// StaticTZ returns a zone backed by a fixed IANA timezone.
// Empty string means UTC. Invalid timezone names panic because they are
// programmer configuration errors.
func StaticTZ(tz string) *TZResolver {
	if tz == "" {
		return &TZResolver{
			resolve: func(context.Context) (*time.Location, error) {
				return time.UTC, nil
			},
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(fmt.Sprintf("zep: StaticTZ: invalid timezone %q: %v", tz, err))
	}
	return &TZResolver{
		resolve: func(context.Context) (*time.Location, error) {
			return loc, nil
		},
	}
}

type timezoneContextKey struct{}

// WithTimezone returns a child context carrying an IANA timezone string
// for ZoneFromContext.
func WithTimezone(ctx context.Context, tz string) context.Context {
	return context.WithValue(ctx, timezoneContextKey{}, tz)
}

// ZoneFromContext configures the time harness to resolve the timezone from the
// request context. Use WithTimezone to provide the timezone per request.
func TZFromContext() *TZResolver {
	return &TZResolver{
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
//   - Any message with unparseable CreatedAt causes Get to return an error
func WithTimeHarness(zoneResolver *TZResolver) Option {
	if zoneResolver != nil && zoneResolver.resolve == nil {
		panic("zep: WithTimeHarness: zone must be created by StaticTZ or TZFromContext")
	}
	return func(s *SessionService) {
		s.timeHarness = &timeHarnessConfig{zoneResolver: zoneResolver}
	}
}

// isNotFound reports whether err is (or wraps) a Zep NotFound response.
func isNotFound(err error) bool {
	var nf *zep.NotFoundError
	return errors.As(err, &nf)
}

func (s *SessionService) ensureUser(ctx context.Context, userID string) error {
	_, err := s.userClient.Get(ctx, userID)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	_, err = s.userClient.Add(ctx, &zep.CreateUserRequest{UserID: userID})
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

type state struct {
	mu sync.RWMutex
	m  map[string]any
}

var _ adksession.State = (*state)(nil)

func (s *state) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *state) Set(key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = val
	return nil
}

func (s *state) All() iter.Seq2[string, any] {
	s.mu.RLock()
	cp := make(map[string]any, len(s.m))
	for k, v := range s.m {
		cp[k] = v
	}
	s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range cp {
			if !yield(k, v) {
				return
			}
		}
	}
}

type events []*adksession.Event

func (e events) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, evt := range e {
			if !yield(evt) {
				return
			}
		}
	}
}

func (e events) Len() int { return len(e) }

func (e events) At(i int) *adksession.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}

type session struct {
	id         string
	userID     string
	app        string
	events     []*adksession.Event
	state      *state
	lastUpdate time.Time // zero if no timestamped messages were fetched
}

func (z *session) ID() string      { return z.id }
func (z *session) AppName() string { return z.app }
func (z *session) UserID() string  { return z.userID }

// LastUpdateTime returns the timestamp of the most recent message fetched from
// Zep. Returns zero time.Time{} when no messages carry a parseable CreatedAt
// (e.g. a brand-new or empty thread).
func (z *session) LastUpdateTime() time.Time { return z.lastUpdate }

func (z *session) State() adksession.State   { return z.state }
func (z *session) Events() adksession.Events { return events(z.events) }

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
	existing, err := s.threadClient.Get(ctx, req.SessionID, &zep.ThreadGetRequest{Lastn: zep.Int(1)})
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

	if _, err := s.threadClient.Create(ctx, &zep.CreateThreadRequest{
		ThreadID: req.SessionID,
		UserID:   req.UserID,
	}); err != nil {
		return nil, fmt.Errorf("zep create thread: %w", err)
	}
	return &adksession.CreateResponse{
		Session: &session{
			id:     req.SessionID,
			userID: req.UserID,
			app:    req.AppName,
			state:  &state{m: make(map[string]any)},
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
	if impl, ok := sess.(*session); ok {
		impl.events = append(impl.events, event)
		for k, v := range event.Actions.StateDelta {
			_ = impl.state.Set(k, v)
		}
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
	var speakerName string

	switch zepRole {
	case zep.RoleTypeAssistantRole:
		speakerName = event.Author
	case zep.RoleTypeUserRole:
		speakerName = sess.UserID()
		if s.speakerResolver != nil {
			sp, err := s.speakerResolver.resolve(ctx)
			if err != nil {
				return err
			}
			if sp.Name != "" {
				speakerName = sp.Name
			}
			if sp.Role != "" {
				zepRole = sp.Role
			}
		}
	}

	msg := &zep.Message{
		Role:    zepRole,
		Content: contentStr,
	}
	if speakerName != "" {
		msg.Name = &speakerName
	}

	_, err := s.threadClient.AddMessages(ctx, sess.ID(), &zep.AddThreadMessagesRequest{
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
	if s.timeHarness.zoneResolver == nil {
		return nil, nil
	}
	return s.timeHarness.zoneResolver.resolve(ctx)
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
	lastn := s.msgHistoryLength
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

	resp, err := s.threadClient.Get(ctx, sessionID, &zep.ThreadGetRequest{Lastn: zep.Int(lastn)})
	if err != nil {
		return nil, time.Time{}, err
	}

	// Ownership guard: an existing thread must belong to the requesting user.
	// A nonexistent thread returns NotFound above (before this point), so the
	// AutoCreateSession path — which binds the thread to req.UserID — is never
	// blocked here. The guard runs before the msgHistoryLength == 0 early
	// return so the verify-only path is protected too.
	if err := verifyThreadOwner(resp, expectedUserID); err != nil {
		return nil, time.Time{}, err
	}

	if s.msgHistoryLength == 0 {
		return nil, time.Time{}, nil // thread verified; caller requested no history
	}

	var events []*adksession.Event
	var lastTime time.Time
	for _, msg := range resp.GetMessages() {
		if msg == nil {
			continue
		}

		isUser := msg.Role == zep.RoleTypeUserRole

		name := derefOrEmpty(msg.Name)
		var author string
		if isUser {
			author = "user"
			if name == "" {
				name = "user"
			}
		} else {
			if name == "" {
				name = "assistant"
			}
			author = name
		}

		evt := adksession.NewEvent(derefOrEmpty(msg.UUID))
		evt.Author = author

		contentRole := "model"
		if isUser {
			contentRole = "user"
		}

		content := msg.Content

		if s.timeHarnessEnabled() {
			if msg.CreatedAt == nil {
				return nil, time.Time{}, fmt.Errorf(
					"zep: TimeHarness enabled but message %s has nil CreatedAt",
					derefOrEmpty(msg.UUID),
				)
			}
			t, ok := parseTimestamp(*msg.CreatedAt)
			if !ok {
				return nil, time.Time{}, fmt.Errorf(
					"zep: TimeHarness enabled but message %s has unparseable CreatedAt: %q",
					derefOrEmpty(msg.UUID), *msg.CreatedAt,
				)
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

func (s *SessionService) buildMessageFormatInstruction() string {
	if s.timeHarnessEnabled() {
		return `[MESSAGES_HISTORY_FORMAT]
The conversation history uses this format:

  [YYYY-MM-DD HH:MM Name] raw message content

The bracketed prefix is system-provided metadata for time-awareness and
speaker identification. All timestamps are already localized to the speaker's
local time — you do not need to think about timezones; what you see is
always the user's local time.

IMPORTANT: Never produce responses with this bracketed prefix. Respond
with raw message content only.
[/MESSAGES_HISTORY_FORMAT]`
	}
	return `[MESSAGES_HISTORY_FORMAT]
The conversation history uses this format:

  [Name] raw message content

The bracketed prefix is system-provided metadata for speaker identification.

IMPORTANT: Never produce responses with this bracketed prefix. Respond
with raw message content only.
[/MESSAGES_HISTORY_FORMAT]`
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

func (s *SessionService) buildTimeAwarenessInstruction(loc *time.Location, lastTime time.Time) string {
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
		nowStr, formatElapsed(elapsed), timeAwarenessFraming,
	)
}

func (s *SessionService) buildInstruction(ctx context.Context, hasHistory bool, lastTime time.Time) (string, error) {
	var parts []string
	if hasHistory {
		parts = append(parts, s.buildMessageFormatInstruction())
	}
	if s.timeHarnessEnabled() {
		loc, err := s.resolveLocation(ctx)
		if err != nil {
			return "", err
		}
		parts = append(parts, s.buildTimeAwarenessInstruction(loc, lastTime))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (s *SessionService) buildContext(ctx context.Context, sessionID, expectedUserID string, state *state) ([]*adksession.Event, time.Time, error) {
	history, lastTime, err := s.fetchHistory(ctx, sessionID, expectedUserID)
	if err != nil {
		return nil, time.Time{}, err
	}

	if s.instructionKey != "" {
		instruction, err := s.buildInstruction(ctx, len(history) > 0, lastTime)
		if err != nil {
			return nil, time.Time{}, err
		}
		if instruction != "" {
			if err := state.Set(s.instructionKey, instruction); err != nil {
				return nil, time.Time{}, err
			}
		}
	}

	return history, lastTime, nil
}

func (s *SessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	sess := &session{
		id:     req.SessionID,
		userID: req.UserID,
		app:    req.AppName,
		state:  &state{m: make(map[string]any)},
	}

	events, lastTime, err := s.buildContext(ctx, req.SessionID, req.UserID, sess.state)
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
