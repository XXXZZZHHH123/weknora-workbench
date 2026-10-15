package tools

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentReadReuseHonorsCurrentBindingAndPhysicalPage(t *testing.T) {
	registry := NewToolRegistry()
	read := func(id string, page int) *types.ToolResult {
		return &types.ToolResult{Success: true, Data: map[string]any{"document_scope": map[string]any{"matched": true, "knowledge_id": id, "page": page}}}
	}
	require.True(t, IsCurrentDocumentRead(registry, read("doc-a", 7)), "legacy registries without binding preserve existing behavior")
	state := NewDocumentScopeState()
	registry.RegisterTool(&bindDocumentScopeTool{BaseTool: NewBaseTool(ToolBindDocumentScope, "", nil), state: state})
	require.False(t, IsCurrentDocumentRead(registry, read("doc-a", 7)), "unbound candidates cannot stand in for a validated read")
	state.sources = map[string]DocumentSourceSelection{"doc-a": {KnowledgeID: "doc-a", Page: 7}}
	require.True(t, IsCurrentDocumentRead(registry, read("doc-a", 7)))
	require.False(t, IsCurrentDocumentRead(registry, read("doc-a", 8)))
	require.False(t, IsCurrentDocumentRead(registry, read("doc-b", 7)))
	state.sources = map[string]DocumentSourceSelection{"doc-b": {KnowledgeID: "doc-b"}}
	require.False(t, IsCurrentDocumentRead(registry, read("doc-a", 7)), "a previously valid read cannot bypass a later rebind")
	require.True(t, IsCurrentDocumentRead(registry, read("doc-b", 8)))
	failed := read("doc-b", 8)
	failed.Success = false
	require.False(t, IsCurrentDocumentRead(registry, failed))
	require.False(t, IsCurrentDocumentRead(registry, &types.ToolResult{Success: true}))
}
