package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"go.naturallyfunny.dev/spotify"
)

func TestToolsNilClient(t *testing.T) {
	if _, err := Tools(nil); err == nil {
		t.Fatal("Tools(nil): want error, got nil")
	}
}

func TestToolsNames(t *testing.T) {
	tools, err := Tools(spotify.New(&fakeStore{}, testAuth()))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	want := []string{
		"search_tracks", "my_playlists", "playlist_tracks",
		"now_playing", "my_devices",
		"play", "pause", "skip_next", "skip_previous", "set_volume",
	}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestForAgent(t *testing.T) {
	passthrough := errors.New("some other failure")
	tests := []struct {
		name string
		err  error
		want string // substring the translated error must contain
	}{
		{"not connected", spotify.ErrNotConnected, "hasn't connected"},
		{"no device", spotify.ErrNoActiveDevice, "no active Spotify device"},
		{"premium", spotify.ErrPremiumRequired, "Premium"},
		{"rate limited", spotify.ErrRateLimited, "rate-limiting"},
		{"passthrough", passthrough, "some other failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forAgent(tt.err)
			if got == nil || !strings.Contains(got.Error(), tt.want) {
				t.Errorf("forAgent(%v) = %v, want containing %q", tt.err, got, tt.want)
			}
		})
	}
	if forAgent(nil) != nil {
		t.Error("forAgent(nil): want nil")
	}
}

func TestToPlaybackView(t *testing.T) {
	if got := toPlaybackView(nil); got.Playing || got.Track != nil || got.Device != nil {
		t.Errorf("toPlaybackView(nil) = %+v, want not playing with no track/device", got)
	}

	pb := &spotify.Playback{
		Track:       &spotify.Track{ID: "t1", Name: "Song", Artists: []string{"A"}},
		Device:      spotify.Device{ID: "d1", Name: "Phone"},
		IsPlaying:   true,
		ProgressMs:  4200,
		ContextURI:  "spotify:playlist:p1",
		ContextType: "playlist",
	}
	got := toPlaybackView(pb)
	if !got.Playing || got.ProgressMs != 4200 || got.ContextType != "playlist" {
		t.Errorf("toPlaybackView populated = %+v", got)
	}
	if got.Track == nil || got.Track.Name != "Song" {
		t.Errorf("track not mapped: %+v", got.Track)
	}
	if got.Device == nil || got.Device.Name != "Phone" {
		t.Errorf("device not mapped: %+v", got.Device)
	}
}

// TestSearchTracksFlow drives search_tracks end to end against a fake Spotify,
// proving the tool maps results and threads the invocation's UserID through to
// the token store.
func TestSearchTracksFlow(t *testing.T) {
	store := &fakeStore{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", writeToken)
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"tracks":{"items":[
			{"id":"t1","name":"Song One","uri":"spotify:track:t1",
			 "external_urls":{"spotify":"https://open.spotify.com/track/t1"},
			 "artists":[{"name":"Artist A"}]}
		]}}`)
	})
	client, ctx := newTestClient(t, store, mux)

	result, err := runTool(t, client, ctx, "user-123", "search_tracks", map[string]any{"query": "song one"})
	if err != nil {
		t.Fatalf("search_tracks: %v", err)
	}
	tracks, ok := result["tracks"].([]any)
	if !ok || len(tracks) != 1 {
		t.Fatalf("tracks = %#v, want one entry", result["tracks"])
	}
	if store.gotUserID != "user-123" {
		t.Errorf("token store queried for %q, want %q", store.gotUserID, "user-123")
	}
}

// TestSetVolumePremiumError proves a Spotify 403 surfaces to the agent as the
// translated Premium guidance, not a raw HTTP error.
func TestSetVolumePremiumError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", writeToken)
	mux.HandleFunc("/v1/me/player/volume", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, `{"error":{"status":403,"message":"Player command failed: Premium required","reason":"PREMIUM_REQUIRED"}}`)
	})
	client, ctx := newTestClient(t, &fakeStore{}, mux)

	_, err := runTool(t, client, ctx, "user-1", "set_volume", map[string]any{"percent": 50})
	if err == nil {
		t.Fatal("set_volume: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Premium") {
		t.Errorf("error = %q, want Premium guidance", err)
	}
}

// --- helpers ---

func runTool(t *testing.T, client *spotify.Client, ctx context.Context, userID, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	tools, err := Tools(client)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, tl := range tools {
		if tl.Name() == name {
			runner, ok := tl.(runnableTool)
			if !ok {
				t.Fatalf("tool %q is not runnable", name)
			}
			return runner.Run(&testContext{Context: ctx, userID: userID}, args)
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil, nil
}

// runnableTool mirrors the unexported interface the ADK uses to invoke a tool,
// letting the tests call a tool the way the framework does.
type runnableTool interface {
	Run(ctx adktool.Context, args any) (map[string]any, error)
}

func newTestClient(t *testing.T, store spotify.TokenStore, mux http.Handler) (*spotify.Client, context.Context) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	// Route every Spotify request — token refresh and API — at the test server,
	// dispatched by path, so the real client runs against our fixtures.
	httpClient := &http.Client{Transport: rewriteTransport{target: target}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)
	return spotify.New(store, testAuth()), ctx
}

func testAuth() *spotifyauth.Authenticator {
	return spotifyauth.New(
		spotifyauth.WithClientID("test-id"),
		spotifyauth.WithClientSecret("test-secret"),
	)
}

type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func writeToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, `{"access_token":"test-access","token_type":"Bearer","expires_in":3600}`)
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

type fakeStore struct {
	gotUserID string
}

func (s *fakeStore) GetRefreshToken(_ context.Context, userID string) (string, error) {
	s.gotUserID = userID
	return "refresh-token", nil
}

func (s *fakeStore) SaveRefreshToken(_ context.Context, _, _ string) error { return nil }

// testContext is a minimal adktool.Context for exercising tool handlers. It
// embeds the harness context so oauth2 picks up the test HTTP client, and
// reports a configurable UserID.
type testContext struct {
	context.Context
	userID string
}

func (c *testContext) UserID() string                       { return c.userID }
func (c *testContext) FunctionCallID() string               { return "test-function-call-id" }
func (c *testContext) AgentName() string                    { return "test-agent" }
func (c *testContext) AppName() string                      { return "test-app" }
func (c *testContext) Branch() string                       { return "test-branch" }
func (c *testContext) SessionID() string                    { return "test-session-id" }
func (c *testContext) InvocationID() string                 { return "test-invocation-id" }
func (c *testContext) UserContent() *genai.Content          { return nil }
func (c *testContext) ReadonlyState() session.ReadonlyState { return nil }
func (c *testContext) State() session.State                 { return nil }
func (c *testContext) Artifacts() agent.Artifacts           { return nil }
func (c *testContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (c *testContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (c *testContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c *testContext) RequestConfirmation(string, any) error                { return nil }

var _ adktool.Context = (*testContext)(nil)
