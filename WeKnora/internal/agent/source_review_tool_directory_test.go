package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewIncludesBoundedNonEvidenceToolDirectory(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The requested material remains unverified.","next_action":"collect_evidence"}`)}}
	engine := sourceConsistencyEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	for i := 0; i < 40; i++ {
		name := "capability_" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('A'+i/26))
		tool := &countingTool{BaseTool: agenttools.NewBaseTool(name, strings.Repeat("capability description ", 40), json.RawMessage(`{"type":"object"}`))}
		engine.toolRegistry.RegisterTool(tool)
	}

	_, err := engine.answerNeedsRepair(t.Context(), "What evidence does the field report contain?", "The report confirms the result.", "", sourceConsistencyState())
	require.NoError(t, err)
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	directory, ok := input["available_tool_directory"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "currently_model_visible_registered_tools", directory["scope"])
	require.Equal(t, false, directory["directory_complete"], "deferred and hidden registry capabilities are outside this view")
	require.Equal(t, false, directory["directory_is_source_fact"])
	require.Equal(t, false, directory["complete_within_scope"], "the exposed list was capped")
	tools, ok := directory["available_tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 32)
	first, ok := tools[0].(string)
	require.True(t, ok)
	require.Equal(t, "capability_xaA", first)
	require.NotContains(t, model.calls[0][1].Content, strings.Repeat("capability description ", 2))
	require.NotContains(t, input, "execution_evidence")
	require.Contains(t, model.calls[0][0].Content, "does not prove the original source lacks the requested evidence")
	require.Contains(t, model.calls[0][0].Content, structuredAvailabilityInstructions)
}

func TestSourceReviewToolDirectoryMarksUnavailableRegistryScope(t *testing.T) {
	var engine *AgentEngine
	directory := engine.sourceReviewToolDirectory()
	require.Equal(t, false, directory["directory_complete"])
	require.Equal(t, false, directory["complete_within_scope"])
	require.Empty(t, directory["available_tools"])

	engine = &AgentEngine{}
	engine.toolRegistry = agenttools.NewToolRegistry()
	directory = engine.sourceReviewToolDirectory()
	require.Equal(t, true, directory["complete_within_scope"], "the empty current model-visible set is fully represented")
	require.Empty(t, directory["available_tools"])
}
