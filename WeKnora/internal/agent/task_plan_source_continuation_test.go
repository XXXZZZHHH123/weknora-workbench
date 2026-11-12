package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceContinuationEngine(t *testing.T, mode string, domains []string) (*AgentEngine, context.Context, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat("c", 64))
		_, _ = io.WriteString(w, `{"status":"ok","tables":[]}`)
	})
	base, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	base.(*agenttools.StructuredDataTool).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-current", TenantID: 7}})
	installed, err := registry.InstallStructuredManifest(structuredCodecManifest())
	require.NoError(t, err)
	require.True(t, installed.Success)
	registry.RegisterTool(newCountingTool(agenttools.ToolWebSearch))
	registry.RegisterTool(newCountingTool(agenttools.ToolWebFetch))
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: mode, SourceDomains: domains}}
	engine.taskProjection = &taskToolProjection{selected: map[string]bool{}, requiresSourceEvidence: mode != "none"}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	return engine, ctx, calls
}

func executeSourceContinuationCall(t *testing.T, engine *AgentEngine, ctx context.Context, name, arguments string) types.ToolCall {
	t.Helper()
	step := &types.AgentStep{}
	suspension := engine.executeSingleToolCall(ctx, types.LLMToolCall{ID: "actual-source-call", Function: types.FunctionCall{Name: name, Arguments: arguments}}, 0, step, 0, 1, "s", "m")
	require.Nil(t, suspension)
	require.Len(t, step.ToolCalls, 1)
	return step.ToolCalls[0]
}

func TestTaskSourceContinuationSuccessfulReadRetainsCurrentProtocolOnly(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		domains []string
	}{
		{"discover", []string{"knowledge"}},
		{"recover", []string{"knowledge"}},
		{"none", []string{}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			engine, ctx, calls := sourceContinuationEngine(t, tc.mode, tc.domains)
			beforePlan, err := json.Marshal(engine.taskPlan)
			require.NoError(t, err)
			beforePreparation := engine.plannedSourceDiscoverySelection()
			require.NotContains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
			require.Zero(t, calls.Load(), "advertising protocols never executes a source lookup")
			call := executeSourceContinuationCall(t, engine, ctx, "structured_list_pdf_tables", `{}`)
			require.True(t, call.Result.Success, call.Result.Error)
			require.Equal(t, int64(1), calls.Load())
			names := listToolNames(engine.buildToolsForLLM())
			require.Contains(t, names, "structured_read_pdf_table", "the model's successful source choice must keep the next registered read schema visible")
			require.Contains(t, names, "structured_query_pdf_table")
			require.Contains(t, names, agenttools.ToolStructuredCapabilities)
			require.NotContains(t, names, agenttools.ToolStructuredData, "the deferred compatibility envelope stays hidden")
			require.NotContains(t, names, agenttools.ToolWebSearch)
			require.NotContains(t, names, agenttools.ToolWebFetch)
			afterPlan, err := json.Marshal(engine.taskPlan)
			require.NoError(t, err)
			require.Equal(t, string(beforePlan), string(afterPlan), "continuation changes schema visibility, not the model's original preparation plan")
			require.Equal(t, beforePreparation, engine.plannedSourceDiscoverySelection())
			require.Equal(t, int64(1), calls.Load(), "keeping schemas must not prefetch additional data")
		})
	}
}

func TestTaskSourceContinuationRequiresExecutedAuthorizedSourceRead(t *testing.T) {
	for _, name := range []string{"selected but not executed", "execution denied", "scope denied", "unknown tool", "truncated", "protocol only", "session state only", "deferred envelope"} {
		t.Run(name, func(t *testing.T) {
			engine, ctx, calls := sourceContinuationEngine(t, "discover", []string{"knowledge"})
			tool, arguments, wantSuccess := "structured_list_pdf_tables", `{}`, false
			wantRequests := int64(0)
			switch name {
			case "selected but not executed":
				engine.taskProjection.selected[tool] = true
			case "execution denied":
				engine.toolRegistry.SetExecutionGuard(func(context.Context, types.Tool) error { return errors.New("fixture authorization denied") })
			case "scope denied":
				arguments = `{"knowledge_base_id":"kb-foreign"}`
			case "unknown tool":
				tool = "structured_missing_operation"
			case "truncated":
				arguments = `{"knowledge_base_id":"kb-cur`
			case "protocol only":
				tool, wantSuccess = agenttools.ToolStructuredCapabilities, true
			case "session state only":
				tool, wantSuccess, wantRequests = "structured_resolve_fields", true, 1
			case "deferred envelope":
				tool, arguments, wantSuccess, wantRequests = agenttools.ToolStructuredData, `{"operation":"list_pdf_tables","arguments":{}}`, true, 1
			}
			if name != "selected but not executed" {
				call := executeSourceContinuationCall(t, engine, ctx, tool, arguments)
				require.Equal(t, wantSuccess, call.Result.Success, call.Result.Error)
			}
			require.Equal(t, wantRequests, calls.Load())
			require.NotContains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
		})
	}
}

type sourceContinuationForeignTool struct {
	agenttools.BaseTool
	descriptor agenttools.StructuredOperationDescriptor
}

func (tool *sourceContinuationForeignTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Data: map[string]any{"_structured_execution": tool.descriptor}}, nil
}

func replaceSourceContinuationTool(t *testing.T, registry *agenttools.ToolRegistry, name string, descriptor agenttools.StructuredOperationDescriptor, deferred bool) {
	t.Helper()
	keep := []string{}
	for _, definition := range registry.GetFunctionDefinitions() {
		if definition.Name != name {
			keep = append(keep, definition.Name)
		}
	}
	registry.RestrictTools(keep)
	tool := &sourceContinuationForeignTool{BaseTool: agenttools.NewBaseTool(name, "External fixture", json.RawMessage(`{"type":"object","properties":{}}`)), descriptor: descriptor}
	if deferred {
		registry.RegisterDeferredTool(tool)
	} else {
		registry.RegisterTool(tool)
	}
}

func TestTaskSourceContinuationRejectsForeignIdentityAndDoesNotExposeForeignDependencies(t *testing.T) {
	for _, foreignRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign followup", true: "foreign initiating read"}[foreignRead], func(t *testing.T) {
			engine, ctx, calls := sourceContinuationEngine(t, "discover", []string{"knowledge"})
			name := "structured_query_pdf_table"
			if foreignRead {
				name = "structured_list_pdf_tables"
			}
			descriptor, _, ok := engine.toolRegistry.StructuredInvocation(name, json.RawMessage(`{}`))
			require.True(t, ok)
			replaceSourceContinuationTool(t, engine.toolRegistry, name, descriptor, false)
			call := executeSourceContinuationCall(t, engine, ctx, "structured_list_pdf_tables", `{}`)
			require.True(t, call.Result.Success, call.Result.Error)
			names := listToolNames(engine.buildToolsForLLM())
			require.NotContains(t, names, "structured_query_pdf_table", "a forged descriptor or same-name registration does not establish a native protocol dependency")
			if foreignRead {
				require.Zero(t, calls.Load())
				require.NotContains(t, names, "structured_read_pdf_table")
			} else {
				require.Equal(t, int64(1), calls.Load())
				require.Contains(t, names, "structured_read_pdf_table")
			}
		})
	}
}

func TestTaskSourceContinuationUsesCurrentManifestAndDefinitions(t *testing.T) {
	t.Run("changed descriptor before collection", func(t *testing.T) {
		engine, ctx, calls := sourceContinuationEngine(t, "discover", []string{"knowledge"})
		call := engine.runToolCall(ctx, types.LLMToolCall{ID: "executed", Function: types.FunctionCall{Name: "structured_list_pdf_tables", Arguments: `{}`}}, 0, 0, 1, "s", "m")
		require.True(t, call.Result.Success)
		manifest := structuredCodecManifest()
		manifest["manifest_sha256"] = strings.Repeat("d", 64)
		_, err := engine.toolRegistry.InstallStructuredManifest(manifest)
		require.NoError(t, err)
		engine.appendToolOutcomes(ctx, &types.AgentStep{}, []types.ToolCall{call}, 0, "s")
		require.NotContains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
		require.Equal(t, int64(1), calls.Load())
	})
	t.Run("removed and deferred definitions stay unavailable", func(t *testing.T) {
		engine, ctx, calls := sourceContinuationEngine(t, "discover", []string{"knowledge"})
		call := executeSourceContinuationCall(t, engine, ctx, "structured_list_pdf_tables", `{}`)
		require.True(t, call.Result.Success)
		require.Contains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
		manifest := structuredCodecManifest()
		manifest["manifest_sha256"] = strings.Repeat("d", 64)
		kept := []any{}
		for _, entry := range manifest["tools"].([]any) {
			if entry.(map[string]any)["name"] != "query_pdf_table" {
				kept = append(kept, entry)
			}
		}
		manifest["tools"] = kept
		_, err := engine.toolRegistry.InstallStructuredManifest(manifest)
		require.NoError(t, err)
		replaceSourceContinuationTool(t, engine.toolRegistry, "structured_read_pdf_table", agenttools.StructuredOperationDescriptor{}, true)
		names := listToolNames(engine.buildToolsForLLM())
		require.NotContains(t, names, "structured_query_pdf_table")
		require.NotContains(t, names, "structured_read_pdf_table")
		require.Contains(t, names, "structured_list_pdf_tables")
		require.NotContains(t, names, agenttools.ToolWebSearch)
		require.Equal(t, int64(1), calls.Load(), "refreshing installed schemas and projecting them does not read source data")
	})
}

func TestTaskSourceContinuationParallelCompletionAndTruncation(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "not executed"}[truncated], func(t *testing.T) {
			engine, ctx, calls := sourceContinuationEngine(t, "discover", []string{"knowledge"})
			engine.config.ParallelToolCalls = true
			response := &types.ChatResponse{FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{
				{ID: "one", Function: types.FunctionCall{Name: "structured_list_pdf_tables", Arguments: `{}`}},
				{ID: "two", Function: types.FunctionCall{Name: "structured_read_pdf_table", Arguments: `{}`}},
			}}
			if truncated {
				response.FinishReason = "length"
			}
			step := &types.AgentStep{}
			require.Nil(t, engine.executeToolCalls(ctx, response, step, 0, "s", "m"))
			require.Len(t, step.ToolCalls, 2)
			for _, call := range step.ToolCalls {
				require.Equal(t, !truncated, call.Result.Success)
			}
			if truncated {
				require.Zero(t, calls.Load())
				require.NotContains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
			} else {
				require.Equal(t, int64(2), calls.Load())
				require.Contains(t, listToolNames(engine.buildToolsForLLM()), "structured_query_pdf_table")
			}
		})
	}
}
