package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceConsistencySkipsCapabilitySchemasAndKeepsDirectoryListing(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{
		{ID: "schemas", Name: agenttools.ToolStructuredData, Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"tools": []any{map[string]any{"name": "inspect_source", "description": strings.Repeat("schema ", 2000)}}}}},
		{ID: "expanded-capabilities", Name: expandTaskToolsName, Result: &types.ToolResult{Success: true, Data: map[string]any{"tools": strings.Repeat("capability ", 2000)}}},
		{ID: "directory", Name: "list_documents", Result: &types.ToolResult{Success: true, Data: map[string]any{"documents": []any{map[string]any{"knowledge_id": "doc-7", "title": "Field report"}}, "total_docs": 1}}},
		{ID: "schema-failure", Name: agenttools.ToolStructuredData, Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: false, Error: "schema service unavailable", Output: "schema request failed"}},
	}}}}

	evidence, scope := e.sourceConsistencyEvidence(state, nil)
	require.True(t, scope.ViewComplete)
	require.Len(t, evidence, 4, "successful schemas are omitted; source listing and failed schema access remain")
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "inspect_source")
	require.NotContains(t, string(encoded), "capability ")
	require.Contains(t, string(encoded), "Field report")
	require.Contains(t, string(encoded), "doc-7")
	require.Contains(t, string(encoded), "schema service unavailable")
	require.Contains(t, string(encoded), "schema request failed")
}

func TestSourceConsistencyDropsOutputOnlyForExactEncodedDataDuplicate(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	data := map[string]any{"documents": []any{map[string]any{"knowledge_id": "d-1", "title": "Research note"}}, "total": json.Number("9007199254740993")}
	outputSameWithWhitespace := " { \n \"documents\" : [ { \"knowledge_id\" : \"d-1\", \"title\" : \"Research note\" } ], \"total\" : 9007199254740993 } "
	cases := []struct {
		name      string
		data      map[string]any
		output    string
		success   bool
		wantField bool
	}{
		{name: "same stable encoding with whitespace", data: data, output: outputSameWithWhitespace, success: true, wantField: false},
		{name: "reordered keys are conservatively retained", data: data, output: `{"total":9007199254740993,"documents":[{"knowledge_id":"d-1","title":"Research note"}]}`, success: true, wantField: true},
		{name: "different explanatory output", data: data, output: "Directory loaded with one source entry.", success: true, wantField: true},
		{name: "valid different data", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740994}`, success: true, wantField: true},
		{name: "malformed output", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740993} trailing explanation`, success: true, wantField: true},
		{name: "duplicate key is retained", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740993,"total":9007199254740993}`, success: true, wantField: true},
		{name: "multiple JSON objects", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740993}{"extra":true}`, success: true, wantField: true},
		{name: "large integer remains exact", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740992}`, success: true, wantField: true},
		{name: "failed result is never deduplicated", data: data, output: `{"documents":[{"knowledge_id":"d-1","title":"Research note"}],"total":9007199254740993}`, success: false, wantField: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := types.ToolCall{ID: "result", Name: "list_documents", Result: &types.ToolResult{Success: tc.success, Data: tc.data, Output: tc.output}}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			evidence, scope := e.sourceConsistencyEvidence(state, nil)
			require.True(t, scope.ViewComplete)
			require.Len(t, evidence, 2)
			payload := evidence[1].Content.(map[string]any)
			_, present := payload["output"]
			require.Equal(t, tc.wantField, present)
			require.Equal(t, tc.data, payload["data"])
		})
	}
}

func TestSourceConsistencyStillReportsBudgetAndOmissionsAfterProjection(t *testing.T) {
	e := sourceConsistencyEngine(t, &mockChat{})
	calls := make([]types.ToolCall, 80)
	for i := range calls {
		calls[i] = types.ToolCall{ID: fmt.Sprintf("doc-%02d", i), Name: "list_documents", Result: &types.ToolResult{
			Success: true,
			Data:    map[string]any{"documents": []any{map[string]any{"knowledge_id": fmt.Sprintf("knowledge-%02d", i), "description": strings.Repeat("source listing ", 220)}}},
			Output:  "directory result",
		}}
	}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: calls}}}
	evidence, scope := e.sourceConsistencyEvidence(state, nil)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 24000)
	require.False(t, scope.ViewComplete)
	require.NotEmpty(t, scope.OmittedResults)
	require.True(t, scope.IncludedResults > 0)
	require.Contains(t, string(encoded), "omitted_paths")
	for _, entry := range evidence {
		if !entry.ViewComplete {
			require.NotEmpty(t, entry.OmittedPaths)
		}
	}
}
