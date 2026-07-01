package gworkspace

import (
	"context"
	"testing"

	"go.naturallyfunny.dev/gworkspace"
)

// Compile-time check to verify *gworkspace.Gmail satisfies GmailClient.
var _ GmailClient = (*gworkspace.Gmail)(nil)

type gmailStub struct{}

func (gmailStub) ReadMessages(ctx context.Context, ownerID string, q gworkspace.MessageQuery) ([]gworkspace.Message, error) {
	return nil, nil
}

func (gmailStub) SendEmail(ctx context.Context, ownerID, to, subject, body string) error {
	return nil
}

func TestGmailToolsNilClient(t *testing.T) {
	if _, err := GmailTools(nil); err == nil {
		t.Fatal("GmailTools(nil): want error, got nil")
	}
}

func TestGmailToolsNames(t *testing.T) {
	tools, err := GmailTools(gmailStub{})
	if err != nil {
		t.Fatalf("GmailTools: %v", err)
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	want := []string{"read_messages", "send_email"}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
