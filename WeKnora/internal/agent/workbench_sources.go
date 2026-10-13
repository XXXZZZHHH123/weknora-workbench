package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Discover files before the first reasoning call. This is authorized source
// metadata, never an inferred answer or an unrestricted database connection.
// The normal execution boundary records the actual call and its failure too;
// an unavailable source must not look like an empty catalogue.
func (e *AgentEngine) discoverWorkbenchSources(ctx context.Context, state *types.AgentState, messages []chat.Message, query, sessionID, messageID string) []chat.Message {
	if !e.config.WorkbenchDefault || e.toolRegistry == nil {
		return messages
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" {
		return messages
	}
	// Bind every later knowledge tool call to the report/page named in this
	// turn. A failed or ambiguous lookup stays constrained rather than falling
	// through to a similarly titled chart from another report.
	constraint := agenttools.NewRequestedDocumentConstraint(query)
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolSearchKnowledge); err == nil {
		if search, ok := tool.(*agenttools.SearchKnowledgeTool); ok {
			search.WithDocumentConstraint(constraint)
		}
	}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolReadDocument); err == nil {
		if read, ok := tool.(*agenttools.ReadDocumentTool); ok {
			read.WithDocumentConstraint(constraint)
		}
	}
	requests := []types.LLMToolCall{}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			base.BindSourceQuestion(query)
		}
		catalogArgs, _ := json.Marshal(map[string]any{"operation": "list_datasets", "arguments": map[string]string{"question": query}})
		requests = append(requests,
			types.LLMToolCall{ID: fmt.Sprintf("source-catalog-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: string(catalogArgs)}},
			types.LLMToolCall{ID: fmt.Sprintf("source-schemas-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolStructuredData, Arguments: `{"operation":"describe_tools","arguments":{"operations":["list_series","query_series","analyze_table","inspect_source","get_result_evidence"]}}`}},
		)
	}
	if _, err := e.toolRegistry.GetTool(agenttools.ToolSearchKnowledge); err == nil {
		args, _ := json.Marshal(map[string]string{"query": query})
		requests = append(requests, types.LLMToolCall{ID: fmt.Sprintf("source-knowledge-%s", messageID), Function: types.FunctionCall{Name: agenttools.ToolSearchKnowledge, Arguments: string(args)}})
	}
	if len(requests) == 0 {
		return messages
	}
	step := types.AgentStep{Iteration: -1, Timestamp: time.Now()}
	response := &types.ChatResponse{ToolCalls: requests}
	// These calls only discover authorized sources and schema; their independent
	// providers can run concurrently without executing a numeric query or write.
	e.executeToolCallsParallel(ctx, response, &step, -1, sessionID, messageID)
	registered := map[string]string{}
	if tool, err := e.toolRegistry.GetTool(agenttools.ToolStructuredData); err == nil {
		if base, ok := tool.(*agenttools.StructuredDataTool); ok {
			for _, call := range step.ToolCalls {
				if call.Args["operation"] == "describe_tools" && call.Result != nil && call.Result.Success {
					for _, operation := range agenttools.NewStructuredReadOperationTools(base, call.Result.Data) {
						e.toolRegistry.RegisterTool(operation)
						// Registration is first-wins. A retained older contract
						// must not make a changed service schema disappear.
						current, err := e.toolRegistry.GetTool(operation.Name())
						if err == nil && string(current.Parameters()) == string(operation.Parameters()) && current.Description() == operation.Description() {
							registered[strings.TrimPrefix(operation.Name(), "structured_")] = operation.Name()
						}
					}
				}
			}
		}
	}
	state.RoundSteps = append(state.RoundSteps, step)
	return e.appendToolResults(messages, compactWorkbenchDiscovery(step, query, registered))
}

// Keep the actual authorized calls and their complete evidence in RoundSteps.
// Only the model-facing discovery view omits unrelated pending-sheet names and
// schemas already present in the function definitions. Query results, literal
// field candidates, source context and all coverage evidence remain untouched.
func compactWorkbenchDiscovery(step types.AgentStep, query string, registered map[string]string) types.AgentStep {
	modelStep := step
	modelStep.ToolCalls = append([]types.ToolCall(nil), step.ToolCalls...)
	for i, call := range modelStep.ToolCalls {
		if call.Name != agenttools.ToolStructuredData || call.Result == nil || !call.Result.Success || call.Result.Data == nil {
			continue
		}
		var compact map[string]any
		switch call.Args["operation"] {
		case "list_datasets":
			compact = compactDiscoveryCatalogue(call.Result.Data, query)
		case "describe_tools":
			compact = compactDiscoverySchemas(call.Result.Data, registered)
		}
		if compact == nil {
			continue
		}
		output, err := json.Marshal(compact)
		if err != nil {
			continue
		}
		result := *call.Result
		result.Data, result.Output = compact, string(output)
		modelStep.ToolCalls[i].Result = &result
	}
	return modelStep
}

func copyDiscoveryMap(original map[string]any) map[string]any {
	copy := make(map[string]any, len(original)+2)
	for key, value := range original {
		copy[key] = value
	}
	return copy
}

func compactDiscoveryCatalogue(data map[string]any, query string) map[string]any {
	datasets, ok := data["datasets"].([]any)
	if !ok {
		return nil
	}
	compact := copyDiscoveryMap(data)
	rows := make([]any, 0, len(datasets))
	changed := false
	pendingOmitted := false
	for _, key := range []string{"literal_fields", "literal_field_matches"} {
		if fields, ok := data[key].([]any); ok && len(fields) > 0 {
			compact["literal_fields_query_hint"] = "上述是实际目录中的原表字段匹配，dataset_id、series_id及表头/公式来源均来自真实数据。用户已明确口径时可直接用query_series查询，不必仅为重复取得相同ID再调用list_series。若存在同名候选且口径仍不明确，先澄清；候选截断时不能当作完整候选集。"
			changed = true
			break
		}
	}
	for _, value := range datasets {
		dataset, ok := value.(map[string]any)
		if !ok {
			rows = append(rows, value)
			continue
		}
		pending, ok := dataset["pending_sheets"].([]any)
		if !ok || len(pending) == 0 {
			rows = append(rows, value)
			continue
		}
		kept := make([]any, 0)
		for _, item := range pending {
			name, ok := item.(string)
			// Unknown shapes are retained: compaction must not discard an
			// actionable coverage record whose semantics it does not know.
			if !ok || name == "" || strings.Contains(query, name) {
				kept = append(kept, item)
			}
		}
		if len(kept) == len(pending) {
			rows = append(rows, value)
			continue
		}
		row := copyDiscoveryMap(dataset)
		row["pending_sheets"] = kept
		row["pending_sheet_count"] = len(pending)
		row["pending_sheets_omitted_count"] = len(pending) - len(kept)
		rows = append(rows, row)
		changed = true
		pendingOmitted = true
	}
	if !changed {
		return nil
	}
	compact["datasets"] = rows
	if pendingOmitted {
		compact["pending_sheets_notice"] = "仅保留当前问题明确提及的待核对工作表名称，其他名称为减少重复输入而省略；省略不代表不存在或已导入。pending_sheets也不代表同名字段不存在，须核对literal_fields；需要补齐目录时再用list_series或resolve_fields。明确指定未导入表时不能换表。"
	}
	return compact
}

func compactDiscoverySchemas(data map[string]any, registered map[string]string) map[string]any {
	directory, ok := data["tools"].([]any)
	if !ok || len(registered) == 0 {
		return nil
	}
	rows := make([]any, 0, len(directory))
	changed := false
	for _, value := range directory {
		entry, ok := value.(map[string]any)
		if !ok {
			rows = append(rows, value)
			continue
		}
		name, _ := entry["name"].(string)
		toolName, found := registered[name]
		if !found {
			rows = append(rows, value)
			continue
		}
		row := copyDiscoveryMap(entry)
		delete(row, "inputSchema")
		delete(row, "description")
		row["typed_tool"] = toolName
		row["schema_in_function_definition"] = true
		rows = append(rows, row)
		changed = true
	}
	if !changed {
		return nil
	}
	compact := copyDiscoveryMap(data)
	compact["tools"] = rows
	compact["schema_notice"] = "与原生工具定义一致的参数schema与说明不再重复；未注册或与保留定义不同的操作，其本次原schema保持完整。"
	return compact
}
