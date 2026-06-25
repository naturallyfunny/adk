package zep

import (
	"context"

	"github.com/getzep/zep-go/v3"
	"github.com/getzep/zep-go/v3/client"
	"github.com/getzep/zep-go/v3/option"

	"google.golang.org/adk/memory"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// graphClient is the slice of zep graph functionality this service needs.
type graphClient interface {
	Search(ctx context.Context, request *zep.GraphSearchQuery, opts ...option.RequestOption) (*zep.GraphSearchResults, error)
}

// MemoryService implements memory.Service over a user-scoped Zep knowledge
// graph. It is independent of any session: SearchMemory is keyed by UserID, not
// by thread.
type MemoryService struct {
	graphClient graphClient
}

func NewMemoryService(c *client.Client) *MemoryService {
	s := &MemoryService{}
	if c != nil {
		s.graphClient = c.Graph
	}
	return s
}

// SearchMemory runs a user-scoped graph search and returns Zep's materialized
// context block as a single memory entry. The response is empty when nothing
// matches.
func (s *MemoryService) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	scope := zep.GraphSearchScopeAuto
	res, err := s.graphClient.Search(ctx, &zep.GraphSearchQuery{
		Query:  req.Query,
		UserID: &req.UserID,
		Scope:  &scope,
	})
	if err != nil {
		return nil, err
	}

	if res == nil || res.GetContext() == nil || *res.GetContext() == "" {
		return &memory.SearchResponse{}, nil
	}

	return &memory.SearchResponse{
		Memories: []memory.Entry{{
			Content: genai.NewContentFromText(*res.GetContext(), genai.Role("model")),
		}},
	}, nil
}

// AddSessionToMemory is a no-op: SessionService.AppendEvent already writes every
// turn to the Zep thread, which Zep ingests into the user graph automatically.
// It exists only to satisfy memory.Service.
func (s *MemoryService) AddSessionToMemory(ctx context.Context, sess adksession.Session) error {
	return nil
}

var _ memory.Service = (*MemoryService)(nil)
