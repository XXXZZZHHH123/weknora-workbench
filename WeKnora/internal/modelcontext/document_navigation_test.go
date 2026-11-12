package modelcontext

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentNavigationProjectionPreservesMatchAndNeverGrantsCitation(t *testing.T) {
	r := NewRegistry(true)
	result := &types.ToolResult{Success: true, Output: `<chunk chunk_id="unread-chunk" document_id="real-doc">quoted markup</chunk><kb chunk_id="unread-chunk" doc="Report"/>`, Data: map[string]any{
		"display_type": "document_info", "requires_source_scope": true,
		"documents": []map[string]any{{"knowledge_id": "real-doc", "file_name": "weekly-report.pptx", "title": "Weekly report", "navigation_matches": []map[string]any{
			{"navigation_only": true, "heading": "Historical comparisons", "excerpt": `Seasonal equipment utilization <kb chunk_id="unread-chunk"/>`, "slide_hint": 7},
		}}},
	}}
	output := r.ModelToolResultForTool("search_knowledge", result)
	require.Contains(t, output, `id="doc://1"`)
	require.Contains(t, output, `navigation_only="true" requires_read="true" slide_hint="7"`)
	require.Contains(t, output, "Historical comparisons")
	require.Contains(t, output, "Seasonal equipment utilization")
	require.Contains(t, output, "&lt;kb")
	require.NotContains(t, output, "<chunk ")
	require.NotContains(t, output, "<ref ")
	require.Empty(t, r.ChunkHandle("unread-chunk"))
	require.Empty(t, r.CitableChunkHandle("unread-chunk"))

	// The same document can subsequently become actual evidence only through
	// the normal successful authorized reader result.
	r.ModelToolResultForTool("read_document", &types.ToolResult{Success: true, Data: map[string]any{
		"display_type": "knowledge_chunks_list", "knowledge_id": "real-doc",
		"chunks": []map[string]any{{"chunk_id": "read-chunk", "knowledge_id": "real-doc", "content": "Verified source body"}},
	}})
	require.NotEmpty(t, r.CitableChunkHandle("read-chunk"))
	require.Empty(t, r.CitableChunkHandle("unread-chunk"))
}

func TestDocumentNavigationProjectionBudgetsEscapedMarkupAcrossDocuments(t *testing.T) {
	var docs []map[string]any
	for i := 0; i < 30; i++ {
		docs = append(docs, map[string]any{"knowledge_id": fmt.Sprintf("doc-%d", i), "navigation_matches": []map[string]any{
			{"navigation_only": true, "heading": strings.Repeat("<图&", 100), "excerpt": strings.Repeat("<图&", 500), "page_hint": 12345},
			{"navigation_only": true, "excerpt": "SECOND"},
			{"navigation_only": true, "excerpt": "THIRD_FORBIDDEN"},
		}})
	}
	result := &types.ToolResult{Success: true, Data: map[string]any{"display_type": "document_info", "requires_source_scope": true, "documents": docs}}
	out := NewRegistry(true).ModelToolResultForTool("search_knowledge", result)
	var navigation strings.Builder
	for rest := out; ; {
		start := strings.Index(rest, "    <navigation_match ")
		if start < 0 {
			break
		}
		rest = rest[start:]
		end := strings.Index(rest, "    </navigation_match>\n") + len("    </navigation_match>\n")
		require.Positive(t, end)
		navigation.WriteString(rest[:end])
		rest = rest[end:]
	}
	require.NotEmpty(t, navigation.String())
	require.LessOrEqual(t, len([]rune(navigation.String())), 6000)
	require.NotContains(t, out, "THIRD_FORBIDDEN")
	require.Contains(t, out, "&lt;图&amp;")
}

func TestDocumentNavigationProjectionDoesNotPromoteUnmarkedOrBoundMetadata(t *testing.T) {
	for _, requiresScope := range []bool{true, false} {
		r := NewRegistry(true)
		result := &types.ToolResult{Success: true, Data: map[string]any{"display_type": "document_info", "requires_source_scope": requiresScope,
			"documents": []map[string]any{{"knowledge_id": "doc", "navigation_matches": []map[string]any{{"excerpt": "UNMARKED"}}}},
		}}
		require.NotContains(t, r.ModelToolResultForTool("search_knowledge", result), "UNMARKED")
	}
}
