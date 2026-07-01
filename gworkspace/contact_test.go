package gworkspace

import (
	"context"
	"testing"

	"go.naturallyfunny.dev/gworkspace"
)

// Compile-time check to verify *gworkspace.Contacts satisfies ContactClient.
var _ ContactClient = (*gworkspace.Contacts)(nil)

type contactStub struct{}

func (contactStub) GetContacts(ctx context.Context, ownerID string, q gworkspace.ContactQuery) ([]gworkspace.Contact, error) {
	return nil, nil
}

func (contactStub) AddContact(ctx context.Context, ownerID string, in gworkspace.ContactInput) (gworkspace.Contact, error) {
	return gworkspace.Contact{}, nil
}

func TestContactToolsNilClient(t *testing.T) {
	if _, err := ContactTools(nil); err == nil {
		t.Fatal("ContactTools(nil): want error, got nil")
	}
}

func TestContactToolsNames(t *testing.T) {
	tools, err := ContactTools(contactStub{})
	if err != nil {
		t.Fatalf("ContactTools: %v", err)
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	want := []string{"get_contacts", "add_contact"}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
