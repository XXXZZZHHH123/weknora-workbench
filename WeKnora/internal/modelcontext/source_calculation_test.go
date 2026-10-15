package modelcontext

import (
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSourceCalculationDecodesOnlyOwnedChunkReferences(t *testing.T) {
	registry := NewRegistry(true)
	registry.RegisterChunk(ChunkReference{ChunkID: "actual-source-chunk", KnowledgeID: "authorized-document", KnowledgeBaseID: "authorized-kb", DocumentTitle: "report"})
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_source_values", Arguments: `{"calculations":[{"expression":"x","inputs":{"x":{"value":"12","unit":"吨","chunk_id":"c1","quote":"c1为原文，数值12吨"}},"result_unit":""}]}`}}}
	registry.DecodeToolCalls(calls)
	require.Contains(t, calls[0].Function.Arguments, `"chunk_id":"actual-source-chunk"`)
	require.Contains(t, calls[0].Function.Arguments, `"quote":"c1为原文，数值12吨"`, "literal source quote must not be rewritten")
	calls = []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_source_values", Arguments: `{"calculations":[{"inputs":{"x":{"chunk_id":"c999"}}}]}`}}}
	registry.DecodeToolCalls(calls)
	require.NotEmpty(t, calls[0].UnresolvedHandles, "unknown source handle fails before tool execution")
	calls = []types.LLMToolCall{{Function: types.FunctionCall{Name: "external_calculator", Arguments: `{"chunk_id":"c1"}`}}}
	registry.DecodeToolCalls(calls)
	require.Equal(t, `{"chunk_id":"c1"}`, calls[0].Function.Arguments, "a same-named field on an external tool stays opaque")
}

func TestDocumentTableCalculationDecodesOnlyAuthorizedSourceHandle(t *testing.T) {
	registry := NewRegistry(true)
	registry.RegisterChunk(ChunkReference{ChunkID: "actual-source-chunk", KnowledgeID: "authorized-document", KnowledgeBaseID: "authorized-kb", DocumentTitle: "report"})
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_document_table", Arguments: `{"operation":"inspect","chunk_id":"c1"}`}}}
	registry.DecodeToolCalls(calls)
	require.Contains(t, calls[0].Function.Arguments, `"chunk_id":"actual-source-chunk"`)
	calls = []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_document_table", Arguments: `{"operation":"calculate","chunk_id":"c999"}`}}}
	registry.DecodeToolCalls(calls)
	require.NotEmpty(t, calls[0].UnresolvedHandles)
}
