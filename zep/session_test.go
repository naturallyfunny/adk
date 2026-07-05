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
	"google.golang.org/genai"
)

// ptr returns a pointer to the given string.
func ptr(s string) *string { return &s }

type fakeThread struct {
	getResp     *zepgo.MessageListResponse
	getErr      error
	createCalls int
	addReq      *zepgo.AddThreadMessagesRequest
}

func (f *fakeThread) Create(context.Context, *zepgo.CreateThreadRequest, ...option.RequestOption) (*zepgo.Thread, error) {
	f.createCalls++
	return nil, nil
}

func (f *fakeThread) AddMessages(_ context.Context, _ string, req *zepgo.AddThreadMessagesRequest, _ ...option.RequestOption) (*zepgo.AddThreadMessagesResponse, error) {
	f.addReq = req
	return nil, nil
}

func (f *fakeThread) Get(context.Context, string, *zepgo.ThreadGetRequest, ...option.RequestOption) (*zepgo.MessageListResponse, error) {
	return f.getResp, f.getErr
}

type fakeUser struct{}

func (fakeUser) Get(context.Context, string, ...option.RequestOption) (*zepgo.User, error) {
	return nil, nil
}

func (fakeUser) Add(context.Context, *zepgo.CreateUserRequest, ...option.RequestOption) (*zepgo.User, error) {
	return nil, nil
}

// newTestService builds a SessionService with fake zep clients for testing.
// msgHistoryLength defaults to 10.
func newTestService(msgs []*zepgo.Message, opts ...Option) *SessionService {
	s := &SessionService{
		msgHistoryLength: 10,
		threadClient: &fakeThread{
			getResp: &zepgo.MessageListResponse{Messages: msgs},
		},
		userClient: fakeUser{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// historyEvents returns events that are not system events.
func historyEvents(events []*adksession.Event) []*adksession.Event {
	var out []*adksession.Event
	for _, e := range events {
		if e.Author != "system" {
			out = append(out, e)
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

// newState returns a fresh state for test use.
func newState() *state { return &state{m: make(map[string]any)} }

// runBuildContext calls buildContext with a fresh state and fails the test on error.
func runBuildContext(t *testing.T, svc *SessionService, ctx context.Context) []*adksession.Event {
	t.Helper()
	events, _, err := svc.buildContext(ctx, "test-session", "", newState())
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
	svc := newTestService(msgs, WithTimeHarness(StaticTZ("Asia/Jakarta")))
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
	svc := newTestService(msgs, WithTimeHarness(TZFromContext()))
	ctx := WithTimezone(context.Background(), "Asia/Jakarta")
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
	svc := newTestService(msgs, WithTimeHarness(TZFromContext()))
	// context has no timezone value
	_, _, err := svc.buildContext(context.Background(), "test-session", "", newState())
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
	svc := newTestService(msgs, WithTimeHarness(TZFromContext()))
	ctx := WithTimezone(context.Background(), "Not/AValidZone")
	_, _, err := svc.buildContext(ctx, "test-session", "", newState())
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
	_, _, err := svc.buildContext(context.Background(), "test-session", "", newState())
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
	events, _, err := svc.buildContext(context.Background(), "test-session", "", newState())
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

func TestState_MessageFormatInstruction_NotSetWhenNoHistory(t *testing.T) {
	svc := newTestService(nil, WithInstruction("app:fmt"))
	state := newState()
	_, _, err := svc.buildContext(context.Background(), "test-session", "", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := state.Get("app:fmt"); err == nil {
		t.Error("expected message format key to not be set when history is empty")
	}
}

func TestState_MessageFormatInstruction_SetWhenHistoryPresent(t *testing.T) {
	msgs := []*zepgo.Message{
		{Role: zepgo.RoleTypeUserRole, Content: "hello", Name: ptr("Ian")},
	}
	svc := newTestService(msgs, WithInstruction("app:fmt"))
	state := newState()
	_, _, err := svc.buildContext(context.Background(), "test-session", "", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, err := state.Get("app:fmt")
	if err != nil {
		t.Fatal("expected app:fmt key to be set, got error:", err)
	}
	text, _ := val.(string)
	if !strings.HasPrefix(text, "[MESSAGES_HISTORY_FORMAT]") {
		t.Errorf("expected [MESSAGES_HISTORY_FORMAT] prefix, got: %q", text)
	}
}

func TestState_TimeAwarenessInstruction_OnlyWhenHarnessAndKeySet(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}

	t.Run("harness_off_nothing_written", func(t *testing.T) {
		svc := newTestService(msgs) // no time harness
		state := newState()
		if _, _, err := svc.buildContext(context.Background(), "test-session", "", state); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(state.m) != 0 {
			t.Errorf("expected no state entries without time harness, got: %v", state.m)
		}
	})

	t.Run("harness_on_no_key_nothing_written", func(t *testing.T) {
		svc := newTestService(msgs, WithTimeHarness(nil)) // no WithInstruction key
		state := newState()
		if _, _, err := svc.buildContext(context.Background(), "test-session", "", state); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(state.m) != 0 {
			t.Errorf("expected no state entries when instruction key not set, got: %v", state.m)
		}
	})

	t.Run("harness_on_key_set", func(t *testing.T) {
		svc := newTestService(msgs, WithInstruction("temp:time"), WithTimeHarness(nil))
		state := newState()
		if _, _, err := svc.buildContext(context.Background(), "test-session", "", state); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		val, err := state.Get("temp:time")
		if err != nil {
			t.Fatal("expected temp:time key to be set, got error:", err)
		}
		text, _ := val.(string)
		if !strings.Contains(text, "[CURRENT_TIME]") {
			t.Errorf("expected [CURRENT_TIME] section in combined instruction, got: %q", text)
		}
	})
}

func TestEvents_EmptyHistory_NoEventsReturned(t *testing.T) {
	svc := newTestService(nil)
	events := runBuildContext(t, svc, context.Background())
	if len(events) != 0 {
		t.Errorf("expected no events for empty history, got %d", len(events))
	}
}

func TestEvents_OnlyHistoryEventsReturned(t *testing.T) {
	ts := "2026-05-17T12:00:00Z"
	msgs := []*zepgo.Message{
		{
			Role:      zepgo.RoleTypeUserRole,
			Content:   "hello",
			Name:      ptr("Ian"),
			CreatedAt: ptr(ts),
		},
	}
	// Even with all options, buildContext returns only history events.
	svc := newTestService(msgs,
		WithInstruction("app:ctx"),
		WithTimeHarness(nil),
	)
	events := runBuildContext(t, svc, context.Background())
	if len(events) != 1 {
		t.Fatalf("expected 1 history event (no system events), got %d", len(events))
	}
	if events[0].Author == "system" {
		t.Error("expected no system events in returned event list")
	}
}

func TestCurrentTime_WithLastTime_IncludesElapsed(t *testing.T) {
	svc := &SessionService{}
	lastTime := time.Now().Add(-90 * time.Minute)
	result := svc.buildTimeAwarenessInstruction(time.UTC, lastTime)

	if !strings.Contains(result, "Time since previous message:") {
		t.Errorf("expected elapsed line, got: %q", result)
	}
	if !strings.Contains(result, "[CURRENT_TIME]") {
		t.Errorf("expected [CURRENT_TIME] tag, got: %q", result)
	}
}

func TestCurrentTime_NoLastTime_NoElapsed(t *testing.T) {
	svc := &SessionService{}
	result := svc.buildTimeAwarenessInstruction(time.UTC, time.Time{})

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
	NewSessionService(nil, WithTimeHarness(StaticTZ("Foo/Bar")))
}

func TestConstruction_ZeroZone_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero TZResolver, got none")
		}
	}()
	NewSessionService(nil, WithTimeHarness(&TZResolver{}))
}

func TestConstruction_NilSpeaker_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil speaker, got none")
		}
	}()
	NewSessionService(nil, WithSpeakerResolver(nil))
}

func TestConstruction_ZeroSpeaker_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for zero SpeakerResolver, got none")
		}
	}()
	NewSessionService(nil, WithSpeakerResolver(&SpeakerResolver{}))
}

// userTextEvent builds an inbound user-role event carrying text, mirroring what
// the ADK runner produces for a human (or delegating agent) turn.
func userTextEvent(text string) *adksession.Event {
	evt := adksession.NewEvent("inv")
	evt.Author = "user"
	evt.Content = genai.NewContentFromText(text, genai.RoleUser)
	return evt
}

// addedName returns the Name of the single message captured by AppendEvent.
func addedName(t *testing.T, ft *fakeThread) string {
	t.Helper()
	if ft.addReq == nil || len(ft.addReq.Messages) != 1 {
		t.Fatalf("expected one message added, got %+v", ft.addReq)
	}
	return derefOrEmpty(ft.addReq.Messages[0].Name)
}

func TestAppendEvent_UserName_DefaultsToUserID(t *testing.T) {
	ft := &fakeThread{}
	s := &SessionService{threadClient: ft, userClient: fakeUser{}}
	sess := &session{id: "sess", userID: "alice"}

	if err := s.AppendEvent(context.Background(), sess, userTextEvent("hi")); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if got := addedName(t, ft); got != "alice" {
		t.Fatalf("name = %q, want %q", got, "alice")
	}
}

func TestAppendEvent_UserName_StaticSpeaker(t *testing.T) {
	ft := &fakeThread{}
	s := &SessionService{threadClient: ft, userClient: fakeUser{}}
	WithSpeakerResolver(StaticSpeaker(Speaker{Name: "human"}))(s)
	sess := &session{id: "sess", userID: "alice"}

	if err := s.AppendEvent(context.Background(), sess, userTextEvent("hi")); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if got := addedName(t, ft); got != "human" {
		t.Fatalf("name = %q, want %q", got, "human")
	}
}

func TestAppendEvent_UserName_SpeakerFromContext(t *testing.T) {
	ft := &fakeThread{}
	s := &SessionService{threadClient: ft, userClient: fakeUser{}}
	WithSpeakerResolver(SpeakerFromContext())(s)
	sess := &session{id: "sess", userID: "alice"}

	ctx := WithSpeaker(context.Background(), Speaker{Name: "ava"})
	if err := s.AppendEvent(ctx, sess, userTextEvent("hi")); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if got := addedName(t, ft); got != "ava" {
		t.Fatalf("name = %q, want %q", got, "ava")
	}
}

func TestAppendEvent_UserName_SpeakerFromContext_Missing_Errors(t *testing.T) {
	ft := &fakeThread{}
	s := &SessionService{threadClient: ft, userClient: fakeUser{}}
	WithSpeakerResolver(SpeakerFromContext())(s)
	sess := &session{id: "sess", userID: "alice"}

	if err := s.AppendEvent(context.Background(), sess, userTextEvent("hi")); err == nil {
		t.Fatal("expected error for missing speaker context, got nil")
	}
}

func TestAppendEvent_TextlessEvent_NotPersisted(t *testing.T) {
	nilContent := adksession.NewEvent("inv")
	nilContent.Author = "assistant"
	cases := map[string]*adksession.Event{
		"nil content":     nilContent,
		"empty string":    userTextEvent(""),
		"whitespace only": userTextEvent("  \n\t "),
	}
	for name, evt := range cases {
		t.Run(name, func(t *testing.T) {
			ft := &fakeThread{}
			s := &SessionService{threadClient: ft, userClient: fakeUser{}}
			sess := &session{id: "sess", userID: "alice"}
			if err := s.AppendEvent(context.Background(), sess, evt); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
			if ft.addReq != nil {
				t.Fatalf("expected no message persisted, got %+v", ft.addReq)
			}
		})
	}
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
		result := svc.buildTimeAwarenessInstruction(time.UTC, time.Time{})
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
		result := svc.buildTimeAwarenessInstruction(time.UTC, lastTime)
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
		msgHistoryLength: 10,
		threadClient:     &fakeThread{getResp: resp, getErr: getErr},
		userClient:       fakeUser{},
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
	// msgHistoryLength == 0 → the verify-only Get path. The guard must run
	// before the early return.
	svc := newOwnershipService(t, "alice", msgs, nil, WithMessageHistoryLength(0))

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
	svc := &SessionService{msgHistoryLength: 10, threadClient: ft, userClient: fakeUser{}}

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
	svc := &SessionService{msgHistoryLength: 10, threadClient: ft, userClient: fakeUser{}}

	resp, err := svc.Create(context.Background(), createRequest("bob"))
	if err != nil {
		t.Fatalf("Create returned unexpected error for new threadClient: %v", err)
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
	svc := &SessionService{msgHistoryLength: 10, threadClient: ft, userClient: fakeUser{}}

	_, err := svc.Create(context.Background(), createRequest("alice"))
	if err != nil {
		t.Fatalf("Create returned unexpected error for own threadClient: %v", err)
	}
	if ft.createCalls != 1 {
		t.Errorf("expected thread.Create to be called once, got %d", ft.createCalls)
	}
}
