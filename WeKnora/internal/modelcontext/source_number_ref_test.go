package modelcontext

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceCalculationCompactReferencesKeepLiteralFieldsAndDecodeOnlySourceIDs(t *testing.T) {
	registry := NewRegistry(true)
	registry.RegisterChunk(ChunkReference{ChunkID: "source-chunk", KnowledgeID: "source-document"})
	calls := []types.LLMToolCall{{Function: types.FunctionCall{
		Name: "calculate_source_values", Arguments: `{"operation":"inspect","chunk_id":"chunk://1","offset":0,"limit":12}`,
	}}}
	registry.DecodeToolCalls(calls)
	require.Empty(t, calls[0].UnresolvedHandles)
	require.JSONEq(t, `{"operation":"inspect","chunk_id":"source-chunk","offset":0,"limit":12}`, calls[0].Function.Arguments)
	compact := `{"operation":"calculate","refs":{"a":{"ref":"sv:example:17","unit":"chunk://1"}},"calculations":[{"expression":"a-1","result_unit":""}]}`
	calls = []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_source_values", Arguments: compact}}}
	registry.DecodeToolCalls(calls)
	require.Empty(t, calls[0].UnresolvedHandles, "literal units cannot become a source selector")
	require.JSONEq(t, compact, calls[0].Function.Arguments)
	require.Equal(t, compact, calls[0].ModelArguments, "preserve the provider payload for diagnostics")
}
