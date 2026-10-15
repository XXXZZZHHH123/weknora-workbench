package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type structuredConcurrencyTool struct {
	tools.BaseTool
	run func(context.Context, string)
}

func (tool *structuredConcurrencyTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var input struct {
		Operation string `json:"operation"`
	}
	_ = json.Unmarshal(raw, &input)
	tool.run(ctx, input.Operation)
	return &types.ToolResult{Success: true, Output: `{}`}, nil
}

func TestStructuredDiscoveryReadsOverlapAndResolutionRemainsABarrier(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = tools.NewToolRegistry()
	var mu sync.Mutex
	order := []string{}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, name)
	}
	engine.toolRegistry.RegisterTool(&structuredConcurrencyTool{
		BaseTool: tools.NewBaseTool(tools.ToolStructuredData, "", json.RawMessage(`{"type":"object"}`)),
		run: func(ctx context.Context, operation string) {
			if operation != "resolve_fields" {
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			record(operation)
		},
	})
	engine.toolRegistry.RegisterTool(&orderedTestTool{
		BaseTool: tools.NewBaseTool(tools.ToolReadDocument, "", json.RawMessage(`{"type":"object"}`)),
		run: func(context.Context) *types.ToolResult {
			record("read_after_resolution")
			return &types.ToolResult{Success: true}
		},
	})
	response := &types.ChatResponse{}
	for _, operation := range []string{"describe_tools", "list_datasets", "resolve_fields"} {
		response.ToolCalls = append(response.ToolCalls, types.LLMToolCall{ID: operation, Function: types.FunctionCall{
			Name: tools.ToolStructuredData, Arguments: `{"operation":"` + operation + `","arguments":{}}`,
		}})
	}
	response.ToolCalls = append(response.ToolCalls, types.LLMToolCall{ID: "after", Function: types.FunctionCall{Name: tools.ToolReadDocument, Arguments: `{}`}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	step := &types.AgentStep{}
	go func() {
		defer close(done)
		engine.executeToolCallsParallel(ctx, response, step, 0, "session", "message")
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("structured discovery reads ran sequentially")
		}
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("structured reads did not finish")
	}
	require.ElementsMatch(t, []string{"describe_tools", "list_datasets"}, order[:2])
	require.Equal(t, []string{"resolve_fields", "read_after_resolution"}, order[2:])
	require.Len(t, step.ToolCalls, 4)
	for i, call := range step.ToolCalls {
		require.Equal(t, response.ToolCalls[i].ID, call.ID, "model order survives concurrent execution")
	}
}
