package main

import (
	"context"
	"fmt"
	"time"

	"go.naturallyfunny.dev/adk/session"
	adksession "google.golang.org/adk/session"
)

type ctxKey string

const TimezoneKey ctxKey = "timezone"

func main() {
	// Assume baseSvc is an existing session implementation (like Zep).
	// Timezone is passed directly to the base service (e.g. zep.WithTimeHarnessFromContext(TimezoneKey))
	// rather than bridged through the decorator.
	var baseSvc adksession.Service = &mockService{}

	// Wrap with persistence controls.
	svc := session.Wrap(baseSvc,
		session.WithoutUserMessagePersistence(),
	)

	// Context carrying the user's timezone (typically set by HTTP middleware)
	ctx := context.WithValue(context.Background(), TimezoneKey, "Asia/Jakarta")

	resp, _ := svc.Get(ctx, &adksession.GetRequest{
		SessionID: "session-789",
		UserID:    "user-abc",
	})

	fmt.Printf("Session is active: %s\n", resp.Session.ID())
}

// Mock service for demonstration purposes
type mockService struct{ adksession.Service }

func (m *mockService) Get(_ context.Context, _ *adksession.GetRequest) (*adksession.GetResponse, error) {
	return &adksession.GetResponse{Session: &mockSession{}}, nil
}

type mockSession struct{ adksession.Session }

func (m *mockSession) ID() string                { return "mock-id" }
func (m *mockSession) State() adksession.State   { return nil }
func (m *mockSession) Events() adksession.Events { return nil }
func (m *mockSession) LastUpdateTime() time.Time { return time.Time{} }
