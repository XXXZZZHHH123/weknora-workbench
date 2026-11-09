package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func encodedCalculationArguments(t *testing.T, chunkID, value, quote string, number bool) string {
	t.Helper()
	input := map[string]any{"chunk_id": chunkID, "value": value, "quote": quote, "unit": "kg"}
	if number {
		input["value"] = json.Number(value)
	}
	entries, err := json.Marshal([]any{map[string]any{"expression": "x", "result_unit": "", "inputs": map[string]any{"x": input}}})
	require.NoError(t, err)
	wrapped, err := json.Marshal(map[string]any{"calculations": string(entries)})
	require.NoError(t, err)
	return string(wrapped)
}

func TestToolArgumentsNormalizeBeforeFinalHandleDecodeAndExactSourceChecks(t *testing.T) {
	const value = "9007199254740993.125000000000000001"
	const quote = "Observed " + value + " kg"
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceCalculationFixture(quote))
	engine.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-chunk", KnowledgeID: "actual-document"})
	raw := encodedCalculationArguments(t, "chunk://1", value, quote, true)
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: sourceCalculationToolName, Arguments: raw}}}
	engine.modelContext.DecodeToolCalls(calls)
	require.Contains(t, calls[0].Function.Arguments, "chunk://1", "an encoded container stays literal until its declared schema is normalized")
	require.NotContains(t, calls[0].Function.Arguments, "weknora://chunk/actual-chunk", "generic prose decoding must not rewrite quoted source data")
	result := engine.runToolCall(t.Context(), calls[0], 0, 0, 1, "session", "message")
	require.True(t, result.Result.Success, result.Result.Error)
	row := sourceCalculationFirstResult(t, result.Result)
	input := row["inputs"].(map[string]any)["x"].(map[string]any)
	require.Equal(t, value, input["value"])
	require.Equal(t, "actual-chunk", input["chunk_id"])
	require.Equal(t, quote, input["quote"])
	require.Equal(t, raw, calls[0].ModelArguments, "normalization cannot change the recorded provider call")
}

func TestToolArgumentsNormalizationCannotAuthorizeUnknownOrStaleSource(t *testing.T) {
	for _, test := range []struct{ name, handle, actualID, content, want string }{
		{"unknown handle", "chunk://999", "", "Observed 7 kg", "unresolved model handles"},
		{"not read", "chunk://1", "not-read", "Observed 7 kg", "chunk_id不是本轮成功读取"},
		{"different evidence", "chunk://1", "actual-chunk", "Observed 8 kg", "quote不是该分块的精确原文"},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := newTestEngine(t, &mockChat{})
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.toolRegistry.RegisterTool(sourceCalculationFixture(test.content))
			if test.actualID != "" {
				engine.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: test.actualID, KnowledgeID: "actual-document"})
			}
			raw := encodedCalculationArguments(t, test.handle, "7", "Observed 7 kg", true)
			calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: sourceCalculationToolName, Arguments: raw}}}
			engine.modelContext.DecodeToolCalls(calls)
			result := engine.runToolCall(t.Context(), calls[0], 0, 0, 1, "session", "message")
			require.False(t, result.Result.Success)
			require.Contains(t, result.Result.Error, test.want)
			require.NotContains(t, result.Result.Data, "calculations")
		})
	}
}

func TestToolArgumentFailureReturnsSpecificFieldAndCannotFillMissingParameters(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceCalculationFixture("Observed 7 kg"))
	raw := encodedCalculationArguments(t, "actual-chunk", "7", "Observed 7 kg", false)
	// The outer object is valid; the complete encoded plan lacks a required field.
	raw = strings.Replace(raw, `,\"result_unit\":\"\"`, "", 1)
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: sourceCalculationToolName, Arguments: raw}}}
	engine.modelContext.DecodeToolCalls(calls)
	result := engine.runToolCall(t.Context(), calls[0], 0, 0, 1, "session", "message")
	require.False(t, result.Result.Success)
	require.Equal(t, "required_argument", result.Result.Data["reason_code"])
	require.Equal(t, "/calculations/0/result_unit", result.Result.Data["field_path"])
	require.Equal(t, raw, result.Args["_raw"])
}
