package zep

import (
	"context"
	"errors"
	"testing"

	zepgo "github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/option"

	"google.golang.org/adk/memory"
)

type fakeGraph struct {
	gotReq *zepgo.GraphSearchQuery
	resp   *zepgo.GraphSearchResults
	err    error
}

func (f *fakeGraph) Search(_ context.Context, req *zepgo.GraphSearchQuery, _ ...option.RequestOption) (*zepgo.GraphSearchResults, error) {
	f.gotReq = req
	return f.resp, f.err
}

func TestSearchMemory_ContextBlock(t *testing.T) {
	f := &fakeGraph{resp: &zepgo.GraphSearchResults{Context: ptr("Ardian lives in Jakarta.")}}
	s := &MemoryService{graphClient: f}

	resp, err := s.SearchMemory(context.Background(), &memory.SearchRequest{
		Query:  "where does the user live",
		UserID: "user-456",
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}

	// Query and UserID are forwarded as a user-scoped graph search.
	if f.gotReq.Query != "where does the user live" {
		t.Errorf("Query = %q, want forwarded query", f.gotReq.Query)
	}
	if f.gotReq.UserID == nil || *f.gotReq.UserID != "user-456" {
		t.Errorf("UserID = %v, want user-456", f.gotReq.UserID)
	}
	if f.gotReq.Scope == nil || *f.gotReq.Scope != zepgo.GraphSearchScopeAuto {
		t.Errorf("Scope = %v, want auto", f.gotReq.Scope)
	}

	// The materialized context block becomes a single memory entry.
	if len(resp.Memories) != 1 {
		t.Fatalf("len(Memories) = %d, want 1", len(resp.Memories))
	}
	got := resp.Memories[0].Content.Parts[0].Text
	if got != "Ardian lives in Jakarta." {
		t.Errorf("entry text = %q, want context block", got)
	}
}

func TestSearchMemory_Empty(t *testing.T) {
	cases := map[string]*zepgo.GraphSearchResults{
		"nil result":    nil,
		"nil context":   {Context: nil},
		"empty context": {Context: ptr("")},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			s := &MemoryService{graphClient: &fakeGraph{resp: resp}}
			got, err := s.SearchMemory(context.Background(), &memory.SearchRequest{UserID: "u"})
			if err != nil {
				t.Fatalf("SearchMemory: %v", err)
			}
			if len(got.Memories) != 0 {
				t.Errorf("len(Memories) = %d, want 0", len(got.Memories))
			}
		})
	}
}

func TestSearchMemory_Error(t *testing.T) {
	s := &MemoryService{graphClient: &fakeGraph{err: errors.New("boom")}}
	if _, err := s.SearchMemory(context.Background(), &memory.SearchRequest{UserID: "u"}); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAddSessionToMemory_NoOp(t *testing.T) {
	s := NewMemoryService(nil)
	if err := s.AddSessionToMemory(context.Background(), nil); err != nil {
		t.Errorf("AddSessionToMemory = %v, want nil", err)
	}
}
