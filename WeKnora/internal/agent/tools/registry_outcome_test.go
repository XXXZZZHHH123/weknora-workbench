package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type outcomeTool struct {
	BaseTool
	result *types.ToolResult
	err    error
}

func (t *outcomeTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return t.result, t.err
}

func TestRegistryPreservesFailureWithoutEncouragingToolHopping(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterTool(&outcomeTool{
		BaseTool: NewBaseTool("denied", "", json.RawMessage(`{"type":"object"}`)),
		result:   &types.ToolResult{Success: false, Error: "permission denied"},
	})
	result, err := registry.ExecuteTool(context.Background(), "denied", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "permission denied", result.Error)
}

func TestRegistryRedirectsRetiredSkillTools(t *testing.T) {
	registry := NewToolRegistry()
	for _, name := range []string{LegacyToolExecuteSkillScript, LegacyToolReadSkill, LegacyToolReadSandboxFile} {
		result, err := registry.ExecuteTool(context.Background(), name, json.RawMessage(`{}`))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, RetiredToolReplacement(name), result.Error)
		require.NotContains(t, result.Error, "tool not found")
	}
	result, err := registry.ExecuteTool(context.Background(), "not_a_tool", json.RawMessage(`{}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "tool not found")
}

func TestRegistryNormalizesMissingAndContradictoryResults(t *testing.T) {
	for _, outcome := range []*outcomeTool{
		{result: nil},
		{result: &types.ToolResult{Success: true}, err: errors.New("transport failed")},
	} {
		outcome.BaseTool = NewBaseTool("test", "", json.RawMessage(`{"type":"object"}`))
		registry := NewToolRegistry()
		registry.RegisterTool(outcome)
		result, _ := registry.ExecuteTool(context.Background(), "test", json.RawMessage(`{}`))
		require.NotNil(t, result)
		require.False(t, result.Success)
		require.NotEmpty(t, result.Error)
	}
}

type panickingTool struct{ BaseTool }

func (t *panickingTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	var items []int
	_ = items[len(items)] // index out of range: a runtime panic like a buggy tool would raise
	return nil, nil
}

func TestRegistryRecoversToolPanic(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterTool(&panickingTool{BaseTool: NewBaseTool("crashy", "", json.RawMessage(`{"type":"object"}`))})

	result, err := registry.ExecuteTool(context.Background(), "crashy", json.RawMessage(`{}`))
	require.Error(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, "tool crashy failed with an internal error", result.Error)
	require.NotContains(t, result.Error, "index out of range")
}

func TestRegistryExplicitlyMarksPreflightRefusals(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterTool(&outcomeTool{
		BaseTool: NewBaseTool("requires_id", "", json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)),
		result:   &types.ToolResult{Success: true},
	})
	invalid, err := registry.ExecuteTool(t.Context(), "requires_id", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, invalid.PreflightRejected)
	require.False(t, invalid.Success)
	registry.SetExecutionGuard(func(context.Context, types.Tool) error { return errors.New("synthetic authorization refusal") })
	denied, err := registry.ExecuteTool(t.Context(), "requires_id", json.RawMessage(`{"id":"source"}`))
	require.Error(t, err)
	require.True(t, denied.PreflightRejected)
	registry.SetExecutionGuard(nil)
	executed, err := registry.ExecuteTool(t.Context(), "requires_id", json.RawMessage(`{"id":"source"}`))
	require.NoError(t, err)
	require.True(t, executed.Success)
	require.False(t, executed.PreflightRejected)
	registry.RegisterTool(&outcomeTool{BaseTool: NewBaseTool("business_failure", "", json.RawMessage(`{"type":"object"}`)), result: &types.ToolResult{Success: false, Error: "synthetic backend unavailable"}})
	failed, err := registry.ExecuteTool(t.Context(), "business_failure", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, failed.PreflightRejected, "an executed business failure consumes a round")
	unknown, err := registry.ExecuteTool(t.Context(), "unknown", json.RawMessage(`{}`))
	require.Error(t, err)
	require.True(t, unknown.PreflightRejected)
}

func TestDocumentSourcePlanRefusalMarksOnlyPreflight(t *testing.T) {
	reader, _ := newReadDocumentFixture(2)
	binding := &bindDocumentScopeTool{state: NewDocumentScopeState(), service: reader.knowledgeService, targets: reader.searchTargets}
	for _, raw := range []string{`{"sources":"malformed"}`, `{"selection_reason":"synthetic plan","sources":[{"knowledge_id":"not-authorized","expected_title":"Other source"}]}`} {
		result, err := binding.Execute(t.Context(), json.RawMessage(raw))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.True(t, result.PreflightRejected)
	}
	result, err := binding.Execute(t.Context(), json.RawMessage(`{"selection_reason":"Select verified metadata","sources":[{"knowledge_id":"doc-1","expected_title":"Engine Manual"}]}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.False(t, result.PreflightRejected, "successful source setup still consumes an executed round")
}

func TestStructuredPreflightClassificationPreservesExecutedFailures(t *testing.T) {
	for _, test := range []struct {
		code, stage      string
		started, refused bool
	}{
		{"parameter_validation_failed", "schema", false, true},
		{"scope_not_authorized", "scope", false, true},
		{"manifest_changed", "manifest", false, true},
		{"manifest_changed", "manifest", true, false},
		{"manifest_unavailable", "manifest", false, false},
		{"manifest_execution_unverified", "manifest", true, false},
	} {
		result := structuredProtocolFailure(test.code, "Synthetic protocol outcome", test.stage, test.started)
		require.Equal(t, test.refused, result.PreflightRejected, test.code)
	}
	tool := &structuredReadOperationTool{base: &StructuredDataTool{}}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"undeclared":"argument"}`))
	require.Error(t, err)
	require.True(t, result.PreflightRejected)
	result, err = (&StructuredDataTool{}).Execute(t.Context(), json.RawMessage(`{}`))
	require.Error(t, err)
	require.True(t, result.PreflightRejected, "missing verified caller is an authority refusal")
}
