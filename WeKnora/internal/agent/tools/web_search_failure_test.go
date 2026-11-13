package tools

import (
	"context"
	"encoding/json"
	"fmt"
	websearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"testing"
)

type failedSearchService struct{ interfaces.WebSearchService }

func (failedSearchService) Search(context.Context, string, *types.WebSearchConfig, string) ([]*types.WebSearchResult, error) {
	return nil, fmt.Errorf("provider: %w", &websearch.RequestFailure{Code: "http_403", Attempts: 1, Err: fmt.Errorf("HTTP 403")})
}
func TestAgentWebSearchFailureDoesNotClaimSourceAbsence(t *testing.T) {
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	tool := NewWebSearchTool(failedSearchService{}, 5, "configured-provider")
	result, err := tool.Execute(ctx, json.RawMessage(`{"query":"public source"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "http_403", result.Data["reason_code"])
	require.Equal(t, false, result.Data["coverage_established"])
	require.Contains(t, result.Output, "web_fetch")
	require.Contains(t, result.Output, "do not invent URLs")
}
