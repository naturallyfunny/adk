package main

import (
	"context"
	"fmt"
	"os"

	"github.com/getzep/zep-go/v3/client"
	"github.com/getzep/zep-go/v3/option"
	"go.naturallyfunny.dev/adk/zep"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"
)

func main() {
	// Initialize Zep Client
	zepClient := client.NewClient(
		option.WithAPIKey(os.Getenv("ZEP_API_KEY")),
	)

	// Set up the Session Service
	svc := zep.NewSessionService(
		zepClient,
		"my-agent",
		zep.WithMessagesHistoryLength(10),
		zep.WithKnowledgeContext(nil),
	)

	// Initialize the ADK Runner
	// We enable AutoCreateSession so the runner handles thread creation for us.
	rnr, err := runner.New(runner.Config{
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	userID := "user-456"

	// In this pattern, SessionID = UserID for deterministic one-thread-per-user apps.
	sessionID := userID

	// User's message
	msg := genai.NewContentFromText("Hello! What do you know about me?", "user")

	// The Runner handles:
	// 1. Get(sessionID) -> Fails if new
	// 2. Create(sessionID, userID) -> ensureUser + Thread.Create (Handled by our Zep service)
	// 3. AppendEvent -> Thread.AddMessages
	// 4. Run -> Get LLM response
	events := rnr.Run(ctx, userID, sessionID, msg, agent.RunConfig{})

	fmt.Println("Agent response:")
	for event, err := range events {
		if err != nil {
			panic(err)
		}
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				fmt.Print(part.Text)
			}
		}
	}
	fmt.Println()
}
