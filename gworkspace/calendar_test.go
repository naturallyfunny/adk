package gworkspace

import (
	"context"
	"testing"

	"go.naturallyfunny.dev/gworkspace"
)

// Compile-time check to verify *gworkspace.Calendar satisfies CalendarClient.
var _ CalendarClient = (*gworkspace.Calendar)(nil)

type calendarStub struct{}

func (calendarStub) GetEvents(ctx context.Context, ownerID string, q gworkspace.EventQuery) ([]gworkspace.Event, error) {
	return nil, nil
}

func (calendarStub) AddEvent(ctx context.Context, ownerID string, in gworkspace.EventInput) (gworkspace.Event, error) {
	return gworkspace.Event{}, nil
}

func TestCalendarToolsNilClient(t *testing.T) {
	if _, err := CalendarTools(nil); err == nil {
		t.Fatal("CalendarTools(nil): want error, got nil")
	}
}

func TestCalendarToolsNames(t *testing.T) {
	tools, err := CalendarTools(calendarStub{})
	if err != nil {
		t.Fatalf("CalendarTools: %v", err)
	}
	got := make(map[string]bool, len(tools))
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	want := []string{"get_events", "add_event"}
	if len(tools) != len(want) {
		t.Errorf("tool count = %d, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
