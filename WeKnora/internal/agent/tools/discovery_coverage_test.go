package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryCoverageExecutionScopeAndRepeatIsolation(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"dataset_id":"aaaaaaaaaaaaaaaaaaaaaaaa","series":[],"offset":0,"next_offset":null,"truncated":false,"total":0}`)
	})
	base.BindSourceQuestion("Explain the report's trend using available sources")
	query := func(search string, offset int) *types.ToolResult {
		arguments, _ := json.Marshal(map[string]any{"dataset_id": cacheDatasetA, "search": search, "offset": offset})
		args, _ := json.Marshal(map[string]any{"operation": "list_series", "arguments": json.RawMessage(arguments)})
		result, err := base.Execute(structuredCacheTestContext("call"), args)
		require.NoError(t, err)
		require.True(t, result.Success)
		return result
	}
	first := query("temperature", 0)
	coverage := first.Data["discovery_coverage"].(map[string]any)
	require.Equal(t, false, coverage["document_body_searched_by_this_operation"])
	require.Equal(t, false, coverage["semantic_absence_established"])
	require.Equal(t, false, coverage["unchanged_repeat"])
	require.Equal(t, false, coverage["truncated"])
	repeat := query("temperature", 0)
	require.Equal(t, true, repeat.Data["discovery_coverage"].(map[string]any)["unchanged_repeat"])
	require.Equal(t, first.Output, repeat.Output, "navigation observations do not rewrite canonical source output")
	for _, changed := range []*types.ToolResult{query("humidity", 0), query("temperature", 10)} {
		require.Equal(t, false, changed.Data["discovery_coverage"].(map[string]any)["unchanged_repeat"], "new query and cursor must not be treated as loops")
	}
	base.BindSourceQuestion("New turn")
	require.Equal(t, false, query("temperature", 0).Data["discovery_coverage"].(map[string]any)["unchanged_repeat"])
	denied, err := base.Execute(context.Background(), json.RawMessage(`{"operation":"list_series","arguments":{}}`))
	require.Error(t, err)
	require.Nil(t, denied, "unauthenticated requests cannot receive source metadata")
}

func TestPDFLocationRecoveryRequiresAuthorizedReadAndNeverInventsPage(t *testing.T) {
	tool, _ := newReadDocumentFixture(2)
	missing, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1","pages":[1,2]}`))
	require.NoError(t, err)
	require.False(t, missing.Success)
	location := missing.Data["location_recovery"].(map[string]any)
	require.Equal(t, "kb-1", location["knowledge_base_id"])
	require.Equal(t, "doc-1", location["knowledge_id"])
	require.Equal(t, false, location["published_table_location_checked_by_this_read"])
	require.NotContains(t, location, "page")
	require.NotContains(t, location, "table_id")
	require.Contains(t, location["notice"], "source_cells")
	read, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"doc-1"}`))
	require.NoError(t, err)
	require.True(t, read.Success)
	require.Contains(t, read.Data, "location_recovery", "successful text-only read still does not locate physical rows")
	denied, _ := tool.Execute(context.Background(), json.RawMessage(`{"id":"unknown"}`))
	require.NotContains(t, denied.Data, "location_recovery")
}

func TestPDFLocatedTextDoesNotSuggestUnnecessaryDiscovery(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	repo.ordered[0].Content = "<!-- PDF page 4 -->\nAlready located source text."
	for _, input := range []string{`{"id":"doc-1"}`, `{"id":"doc-1","page":4}`} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input))
		require.NoError(t, err)
		require.True(t, result.Success)
		require.NotContains(t, result.Data, "location_recovery")
	}
}
