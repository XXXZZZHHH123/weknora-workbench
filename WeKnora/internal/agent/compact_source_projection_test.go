package agent

import (
	"encoding/json"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchSourceCalculationAdvertisesCompactProtocolAtEveryModelBoundary(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault = true
	e.toolRegistry = agenttools.NewToolRegistry()
	tool := newSourceCalculationTool(func() []types.AgentStep { return nil }, e.toolRegistry)
	e.toolRegistry.RegisterTool(tool)
	definitions := e.toolRegistry.GetModelFunctionDefinitions()
	before, err := json.Marshal(definitions)
	require.NoError(t, err)
	execution := e.buildToolsForLLM()
	planning := e.taskModelToolDefinitions(definitions)
	require.Len(t, execution, 1)
	require.Len(t, planning, 1)
	require.JSONEq(t, string(planning[0].Parameters), string(execution[0].Function.Parameters))
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	require.NoError(t, json.Unmarshal(planning[0].Parameters, &schema))
	require.Contains(t, schema.Properties, "operation")
	require.Contains(t, schema.Properties, "refs")
	require.NotContains(t, schema.Properties, "source_quotes")
	require.Contains(t, schema.Required, "operation")
	require.NotContains(t, string(planning[0].Parameters), `"quote"`)
	after, err := json.Marshal(definitions)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "model projection cannot mutate the registered compatibility schema")
}

func TestCompactSourceProjectionRequiresActualWorkbenchExecutor(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(newSourceCalculationTool(func() []types.AgentStep { return nil }))
	definitions := e.toolRegistry.GetModelFunctionDefinitions()
	require.Equal(t, definitions, e.compactWorkbenchToolDefinitions(definitions), "legacy entries keep their transport")
	e.config.WorkbenchDefault = true
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(newCountingTool(sourceCalculationToolName))
	definitions = e.toolRegistry.GetModelFunctionDefinitions()
	require.Equal(t, definitions, e.compactWorkbenchToolDefinitions(definitions), "an unrelated same-name capability cannot acquire the source protocol")
}

func TestSourceNumberCatalogDoesNotAuthorizeCalculatedStatistics(t *testing.T) {
	catalog := `{"kind":"source_calculation_catalog","status":"ok","offset":37,"total":919,"refs":[{"ref":"sv:example:52","value":"123.75","context":"Original text 123.75"}]}`
	messages := []chat.Message{
		{Role: "tool", Name: sourceCalculationToolName, Content: catalog},
		{Role: "tool", Content: `{"kind":"structured_analysis","status":"ok","rows":[[44]]}`},
	}
	for _, candidate := range []string{"Statistic 37", "Statistic 919", "Statistic 52", "Statistic 123.75"} {
		require.NotEmpty(t, ungroundedStructuredNumbers(candidate, messages), candidate)
	}
	require.Empty(t, ungroundedStructuredNumbers("Statistic 44", messages))
	require.False(t, structuredResultHasEvidence(catalog), "a navigation catalog is not an executed calculation")
	require.False(t, hasStructuredHistoryEvidence(messages[:1]))
}
