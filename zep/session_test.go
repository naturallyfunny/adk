package zep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	zepgo "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/option"

	adksession "google.golang.org/adk/session"
)

// ptr returns a pointer to the given string.
func ptr(s string) *string { return &s }

type fakeThread struct {
	getResp     *zepgo.MessageListResponse
	getErr      error
	createCalls int
}

func (f *fakeThread) Create(context.Context, *zepgo.CreateThreadRequest, ...option.RequestOption) (*zepgo.Thread, error) {
	f.createCalls++
	return nil, nil
}

func (f *fakeThread) AddMessages(context.Context, string, *zepgo.AddThreadMessagesRequest, ...option.RequestOption) (*zepgo.AddThreadMessagesResponse, error) {
	return nil, nil
}

func (f *fakeThread) Get(context.Context, string, *zepgo.ThreadGetRequest, ...option.RequestOption) (*zepgo.MessageListResponse, error) {
	return f.getResp, f.getErr
}

func (f *fakeThread) GetUserContext(context.Context, string, *zepgo.ThreadGetUserContextRequest, ...option.RequestOption) (*zepgo.ThreadContextResponse, error) {
	return nil, nil
}

type fakeUser struct{}

func (fakeUser) Get(context.Context, string, ...option.RequestOption) (*zepgo.User, error) {
	return nil, nil
}

func (fakeUser) Add(context.Context, *zepgo.CreateUserRequest, ...option.RequestOption) (*zepgo.User, error) {
	return nil, nil
}

// newTestService builds a SessionService with fake zep clients for testing.
// agentName defaults to "Zee"; messagesHistoryLength defaults to 10.
func newTestService(msgs []*zepgo.Message, opts ...Option) *SessionService {
	s := &SessionService{
		agentName:             "Zee",
		messagesHistoryLength: 10,
		thread: &fakeThread{
			getResp: &zepgo.MessageListResponse{Messages: msgs},
		},
		user: fakeUser{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// historyEvents returns events that are not system events (preamble/postamble/etc.).
func historyEvents(events []*adksession.Event) []*adksession.Event {
	var out []*adksession.Event
	for _, e := range events {
		if e.Author != "system" {
			out = append(out, e)
		}
	}
	return out
}

// systemTexts returns the text content of all system events.
func systemTexts(events []*adksession.Event) []string {
	var out []string
	for _, e := range events {
		if e.Author != "system" {
			continue
		}
		if e.LLMResponse.Content != nil && len(e.LLMResponse.Content.Parts) > 0 {
			out = append(out, e.LLMResponse.Content.Parts[0].Text)
		}
	}
	return out
}

// eventText extracts the text content from an event.
func eventText(t *testing.T, e *adksession.Event) string {
	t.Helper()
	if e.LLMResponse.Content == nil || len(e.LLMResponse.Content.Parts) == 0 {
		t.Fatalf("event %q has no text content", e.Author)
	}
	return e.LLMResponse.Content.Parts[0].Text
}

// runBuildContext calls buildContext and fails the test on error.
func runBuildContext(t *testing.T, svc *SessionService, ctx context.Context) []*adksession.Event {
	t.Helper()
	events, _, err := svc.buildContext(ctx, "test-session", "")
	if err != nil {
		t.Fatalf("buildContext returned unexpected error: %v", err)
	}
	return events
}

func TestHeader_DefaultMode(t *testing.T) {
	msgs := []*zepgo.Message{
		{
			Role:    zepgo.RoleTypeUserRole,
			Content: "hello world",
			Name:    ptr("Ian"),
		},
	}
	svc := newTestService(msgs)
	events := runBuildContext(t, svc, context.Background())

	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	text := eventText(t, hist[0])
	if !strings.HasPrefix(text, "[Ian] ") {
		t.Errorf("expected prefix [Ian], got: %q", text)
	}
	if strings.Contains(text, "2026") || strings.Contains(text, ":") {
		t.Errorf("default mode must not include datetime, got: %q", text)
	}
}

func TestHeader_DefaultZoneUTC(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Alice"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(nil))
	events := runBuildContext(t, svc, context.Background())

	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	text := eventText(t, hist[0])
	if !strings.HasPrefix(text, "[2026-05-17 12:00 Alice] ") {
		t.Errorf("expected UTC datetime prefix, got: %q", text)
	}
}

func TestHeader_DefaultZoneUsesParsedTimestampZone(t *testing.T) {
	ts := "2026-05-17T12:00:00+07:00"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Alice"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(nil))
	events := runBuildContext(t, svc, context.Background())

	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	text := eventText(t, hist[0])
	if !strings.HasPrefix(text, "[2026-05-17 12:00 Alice] ") {
		t.Errorf("expected parsed timestamp zone to be preserved, got: %q", text)
	}
}

func TestHeader_StaticJakarta(t *testing.T) {
	// 05:57 UTC = 12:57 WIB (UTC+7)
	ts := "2026-05-17T05:57:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "aku baru pulang",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(StaticZone("Asia/Jakarta")))
	events := runBuildContext(t, svc, context.Background())

	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	text := eventText(t, hist[0])
	if !strings.HasPrefix(text, "[2026-05-17 12:57 Ian] ") {
		t.Errorf("expected Jakarta-localized prefix, got: %q", text)
	}
}

func TestHeader_FromContext_OK(t *testing.T) {
	ts := "2026-05-17T05:57:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(ZoneFromContext()))
	ctx := ContextWithTimezone(context.Background(), "Asia/Jakarta")
	events := runBuildContext(t, svc, ctx)

	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	text := eventText(t, hist[0])
	// 05:57 UTC → 12:57 Jakarta
	if !strings.HasPrefix(text, "[2026-05-17 12:57 Ian] ") {
		t.Errorf("expected Jakarta prefix from context, got: %q", text)
	}
}

func TestHeader_FromContext_Missing_Errors(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(ZoneFromContext()))
	// context has no timezone value
	_, _, err := svc.buildContext(context.Background(), "test-session", "")
	if err == nil {
		t.Fatal("expected error when timezone key absent, got nil")
	}
	if !strings.Contains(err.Error(), "absent or empty") {
		t.Errorf("error message unexpected: %v", err)
	}
}

func TestHeader_FromContext_InvalidTZ_Errors(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(ZoneFromContext()))
	ctx := ContextWithTimezone(context.Background(), "Not/AValidZone")
	_, _, err := svc.buildContext(ctx, "test-session", "")
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if !strings.Contains(err.Error(), "Not/AValidZone") {
		t.Errorf("error message should contain the invalid timezone, got: %v", err)
	}
}

func TestHeader_TimeHarnessOn_BadTimestamp_Errors(t *testing.T) {
	badTS := "not-a-timestamp"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(badTS),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(nil))
	_, _, err := svc.buildContext(context.Background(), "test-session", "")
	if err == nil {
		t.Fatal("expected error for unparseable CreatedAt with TimeHarness on, got nil")
	}
	if !strings.Contains(err.Error(), "unparseable CreatedAt") {
		t.Errorf("error message unexpected: %v", err)
	}
}

func TestHeader_TimeHarnessOff_BadTimestamp_NoError(t *testing.T) {
	badTS := "not-a-timestamp"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(badTS),
		},
	}
	svc := newTestService(msgs) // no TimeHarness
	events, _, err := svc.buildContext(context.Background(), "test-session", "")
	if err != nil {
		t.Fatalf("expected no error with TimeHarness off and bad timestamp, got: %v", err)
	}
	hist := historyEvents(events)
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
	// Should use [Name] format, ignoring the bad timestamp
	text := eventText(t, hist[0])
	if !strings.HasPrefix(text, "[Ian] ") {
		t.Errorf("expected [Ian] prefix, got: %q", text)
	}
}

func TestHeader_EmptyName_FallsBackToRole(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      nil, // no name — should fall back to role
			CreatedAt: ptr(ts),
		},
	}
	// Test both default mode and TimeHarness mode
	t.Run("default", func(t *testing.T) {
		svc := newTestService(msgs)
		events := runBuildContext(t, svc, context.Background())
		hist := historyEvents(events)
		if len(hist) != 1 {
			t.Fatalf("expected 1 history event, got %d", len(hist))
		}
		text := eventText(t, hist[0])
		if !strings.HasPrefix(text, "[user] ") {
			t.Errorf("expected [user] fallback prefix, got: %q", text)
		}
	})
	t.Run("timeharness", func(t *testing.T) {
		svc := newTestService(msgs, WithTimeHarness(nil))
		events := runBuildContext(t, svc, context.Background())
		hist := historyEvents(events)
		if len(hist) != 1 {
			t.Fatalf("expected 1 history event, got %d", len(hist))
		}
		text := eventText(t, hist[0])
		// Should be [2026-05-17 12:00 user]
		if !strings.Contains(text, " user] ") {
			t.Errorf("expected role fallback in datetime prefix, got: %q", text)
		}
	})
}

func TestEvents_EmptyHistory_NoPreambleNoPostamble(t *testing.T) {
	svc := newTestService(nil) // empty message list
	events := runBuildContext(t, svc, context.Background())

	for _, text := range systemTexts(events) {
		if strings.HasPrefix(text, "[MESSAGES_HISTORY_FORMAT]") {
			t.Error("expected no format_preamble event for empty history")
		}
		if strings.HasPrefix(text, "[CRITICAL_FORMAT_REMINDER]") {
			t.Error("expected no format_postamble event for empty history")
		}
	}
}

func TestEvents_CurrentTime_OnlyWhenHarnessOn(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}

	t.Run("harness_off_no_current_time", func(t *testing.T) {
		svc := newTestService(msgs)
		events := runBuildContext(t, svc, context.Background())
		for _, text := range systemTexts(events) {
			if strings.HasPrefix(text, "[CURRENT_TIME]") {
				t.Error("current_time event must not be emitted when TimeHarness is off")
			}
		}
	})

	t.Run("harness_on_has_current_time", func(t *testing.T) {
		svc := newTestService(msgs, WithTimeHarness(nil))
		events := runBuildContext(t, svc, context.Background())
		found := false
		for _, text := range systemTexts(events) {
			if strings.HasPrefix(text, "[CURRENT_TIME]") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected current_time event when TimeHarness is on")
		}
	})
}

func TestCurrentTime_WithLastTime_IncludesElapsed(t *testing.T) {
	svc := &SessionService{}
	lastTime := time.Now().Add(-90 * time.Minute)
	result := svc.buildCurrentTimeAnchor(time.UTC, lastTime)

	if !strings.Contains(result, "Time since previous message:") {
		t.Errorf("expected elapsed line, got: %q", result)
	}
	if !strings.Contains(result, "[CURRENT_TIME]") {
		t.Errorf("expected [CURRENT_TIME] tag, got: %q", result)
	}
}

func TestCurrentTime_NoLastTime_NoElapsed(t *testing.T) {
	svc := &SessionService{}
	result := svc.buildCurrentTimeAnchor(time.UTC, time.Time{})

	if strings.Contains(result, "Time since previous message:") {
		t.Errorf("expected no elapsed line for zero lastTime, got: %q", result)
	}
	if !strings.Contains(result, "Current date and time:") {
		t.Errorf("expected current date line, got: %q", result)
	}
}

func TestConstruction_InvalidTimezone_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid timezone, got none")
		}
	}()
	NewSessionService(nil, "agent", WithTimeHarness(StaticZone("Foo/Bar")))
}

func TestConstruction_ZeroZone_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero Zone, got none")
		}
	}()
	NewSessionService(nil, "agent", WithTimeHarness(&Zone{}))
}

func TestFormatElapsed_Buckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0 * time.Second, "0 seconds"},
		{30 * time.Second, "30 seconds"},
		{59 * time.Second, "59 seconds"},
		{60 * time.Second, "1 minutes"},
		{65 * time.Second, "1 minutes"},
		{59 * time.Minute, "59 minutes"},
		{60 * time.Minute, "1 hours 0 minutes"},
		{90 * time.Minute, "1 hours 30 minutes"},
		{23*time.Hour + 59*time.Minute, "23 hours 59 minutes"},
		{24 * time.Hour, "1 days 0 hours 0 minutes"},
		{50 * time.Hour, "2 days 2 hours 0 minutes"},
		{50*time.Hour + 17*time.Minute, "2 days 2 hours 17 minutes"},
	}
	for _, c := range cases {
		got := formatElapsed(c.d)
		if got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestCurrentTime_Anchor_ContainsFraming(t *testing.T) {
	svc := &SessionService{}

	t.Run("empty_thread", func(t *testing.T) {
		result := svc.buildCurrentTimeAnchor(time.UTC, time.Time{})
		if !strings.Contains(result, "You are time-aware") {
			t.Errorf("expected framing in empty-thread anchor, got: %q", result)
		}
		if !strings.Contains(result, "Current date and time:") {
			t.Errorf("expected current date line preserved, got: %q", result)
		}
		if !strings.Contains(result, "[CURRENT_TIME]") {
			t.Errorf("expected [CURRENT_TIME] tag preserved, got: %q", result)
		}
	})

	t.Run("non_empty_thread", func(t *testing.T) {
		lastTime := time.Now().Add(-30 * time.Minute)
		result := svc.buildCurrentTimeAnchor(time.UTC, lastTime)
		if !strings.Contains(result, "You are time-aware") {
			t.Errorf("expected framing in non-empty-thread anchor, got: %q", result)
		}
		if !strings.Contains(result, "Time since previous message:") {
			t.Errorf("expected elapsed line preserved, got: %q", result)
		}
		if !strings.Contains(result, "[CURRENT_TIME]") {
			t.Errorf("expected [CURRENT_TIME] tag preserved, got: %q", result)
		}
	})
}

// newOwnershipService builds a SessionService whose fake thread returns resp
// and err from Get. ownerID is the user the thread is reported to belong to;
// pass "" to omit the owner field (simulating a missing UserID in the response).
func newOwnershipService(t *testing.T, ownerID string, msgs []*zepgo.Message, getErr error, opts ...Option) *SessionService {
	t.Helper()
	var resp *zepgo.MessageListResponse
	if getErr == nil {
		resp = &zepgo.MessageListResponse{Messages: msgs}
		if ownerID != "" {
			resp.UserID = ptr(ownerID)
		}
	}
	s := &SessionService{
		agentName:             "Zee",
		messagesHistoryLength: 10,
		thread:                &fakeThread{getResp: resp, getErr: getErr},
		user:                  fakeUser{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func ownerGetRequest(userID string) *adksession.GetRequest {
	return &adksession.GetRequest{
		AppName:   "app",
		UserID:    userID,
		SessionID: "test-session",
	}
}

// sessionEvents collects a session's events into a slice for the historyEvents helper.
func sessionEvents(sess adksession.Session) []*adksession.Event {
	var out []*adksession.Event
	for e := range sess.Events().All() {
		out = append(out, e)
	}
	return out
}

func TestOwnership_OwnerMatches_Succeeds(t *testing.T) {
	msgs := []*zepgo.Message{
		{Role: zepgo.RoleTypeUserRole, Content: "hello", Name: ptr("Alice")},
	}
	svc := newOwnershipService(t, "alice", msgs, nil)

	resp, err := svc.Get(context.Background(), ownerGetRequest("alice"))
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	hist := historyEvents(sessionEvents(resp.Session))
	if len(hist) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(hist))
	}
}

func TestOwnership_OwnerMismatch_Rejected(t *testing.T) {
	msgs := []*zepgo.Message{
		{Role: zepgo.RoleTypeUserRole, Content: "secret", Name: ptr("Alice")},
	}
	svc := newOwnershipService(t, "alice", msgs, nil)

	resp, err := svc.Get(context.Background(), ownerGetRequest("bob"))
	if !errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("expected ErrSessionOwnerMismatch, got: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response on mismatch, got: %+v", resp)
	}
}

func TestOwnership_ThreadNotFound_PassesErrorThrough(t *testing.T) {
	notFound := &zepgo.NotFoundError{}
	svc := newOwnershipService(t, "", nil, notFound)

	_, err := svc.Get(context.Background(), ownerGetRequest("bob"))
	if err == nil {
		t.Fatal("expected error for missing thread, got nil")
	}
	if errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("not-found must not be reported as ownership mismatch: %v", err)
	}
}

func TestOwnership_EmptyOwnerInResponse_FailClosed(t *testing.T) {
	msgs := []*zepgo.Message{
		{Role: zepgo.RoleTypeUserRole, Content: "secret", Name: ptr("Alice")},
	}
	// ownerID "" → response carries no UserID. An authenticated request must be
	// rejected because ownership cannot be confirmed (fail-closed).
	svc := newOwnershipService(t, "", msgs, nil)

	_, err := svc.Get(context.Background(), ownerGetRequest("bob"))
	if !errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("expected fail-closed ErrSessionOwnerMismatch when owner unknown, got: %v", err)
	}
}

func TestOwnership_VerifyOnlyPath_StillRejected(t *testing.T) {
	msgs := []*zepgo.Message{
		{Role: zepgo.RoleTypeUserRole, Content: "secret", Name: ptr("Alice")},
	}
	// messagesHistoryLength == 0 → the verify-only Get path. The guard must run
	// before the early return.
	svc := newOwnershipService(t, "alice", msgs, nil, WithMessagesHistoryLength(0))

	_, err := svc.Get(context.Background(), ownerGetRequest("bob"))
	if !errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("expected ErrSessionOwnerMismatch on verify-only path, got: %v", err)
	}
}

func createRequest(userID string) *adksession.CreateRequest {
	return &adksession.CreateRequest{
		AppName:   "app",
		UserID:    userID,
		SessionID: "test-session",
	}
}

// TestOwnership_Create_ForeignThread_Rejected covers the AutoCreateSession path:
// the runner calls Create after Get returns ErrSessionOwnerMismatch. Create must
// reject without ever creating the thread.
func TestOwnership_Create_ForeignThread_Rejected(t *testing.T) {
	ft := &fakeThread{getResp: &zepgo.MessageListResponse{UserID: ptr("alice")}}
	svc := &SessionService{agentName: "Zee", messagesHistoryLength: 10, thread: ft, user: fakeUser{}}

	_, err := svc.Create(context.Background(), createRequest("bob"))
	if !errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("expected ErrSessionOwnerMismatch, got: %v", err)
	}
	if ft.createCalls != 0 {
		t.Errorf("thread.Create must not be called for a foreign thread, got %d calls", ft.createCalls)
	}
}

// TestOwnership_Create_NewThread_Succeeds is the legitimate AutoCreateSession
// path: the thread does not exist (NotFound), so Create proceeds.
func TestOwnership_Create_NewThread_Succeeds(t *testing.T) {
	ft := &fakeThread{getErr: &zepgo.NotFoundError{}}
	svc := &SessionService{agentName: "Zee", messagesHistoryLength: 10, thread: ft, user: fakeUser{}}

	resp, err := svc.Create(context.Background(), createRequest("bob"))
	if err != nil {
		t.Fatalf("Create returned unexpected error for new thread: %v", err)
	}
	if ft.createCalls != 1 {
		t.Errorf("expected thread.Create to be called once, got %d", ft.createCalls)
	}
	if resp.Session.UserID() != "bob" {
		t.Errorf("expected session bound to bob, got %q", resp.Session.UserID())
	}
}

// TestOwnership_Create_OwnedBySelf_Succeeds: re-creating one's own existing
// thread is allowed.
func TestOwnership_Create_OwnedBySelf_Succeeds(t *testing.T) {
	ft := &fakeThread{getResp: &zepgo.MessageListResponse{UserID: ptr("alice")}}
	svc := &SessionService{agentName: "Zee", messagesHistoryLength: 10, thread: ft, user: fakeUser{}}

	_, err := svc.Create(context.Background(), createRequest("alice"))
	if err != nil {
		t.Fatalf("Create returned unexpected error for own thread: %v", err)
	}
	if ft.createCalls != 1 {
		t.Errorf("expected thread.Create to be called once, got %d", ft.createCalls)
	}
}

func TestEvents_Order_PreambleHistoryPostambleCurrentTime(t *testing.T) {
	ts := "2026-05-17T05:57:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	svc := newTestService(msgs, WithTimeHarness(StaticZone("Asia/Jakarta")))
	events := runBuildContext(t, svc, context.Background())

	// Expected sequence: preamble, history, postamble, current_time
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	// 0: preamble
	if events[0].Author != "system" ||
		!strings.HasPrefix(eventText(t, events[0]), "[MESSAGES_HISTORY_FORMAT]") {
		t.Errorf("position 0 should be preamble, got author=%q text=%q",
			events[0].Author, eventText(t, events[0]))
	}
	// 1: history (non-system)
	if events[1].Author == "system" {
		t.Errorf("position 1 should be history (non-system), got system event: %q",
			eventText(t, events[1]))
	}
	// 2: postamble
	if events[2].Author != "system" ||
		!strings.HasPrefix(eventText(t, events[2]), "[CRITICAL_FORMAT_REMINDER]") {
		t.Errorf("position 2 should be postamble, got author=%q text=%q",
			events[2].Author, eventText(t, events[2]))
	}
	// 3: current_time
	if events[3].Author != "system" ||
		!strings.HasPrefix(eventText(t, events[3]), "[CURRENT_TIME]") {
		t.Errorf("position 3 should be current_time, got author=%q text=%q",
			events[3].Author, eventText(t, events[3]))
	}
}
