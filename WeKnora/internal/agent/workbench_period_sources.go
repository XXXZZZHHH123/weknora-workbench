package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

var workbenchExplicitPeriod = regexp.MustCompile(`\b([0-9]{4})\s*/\s*([0-9]{4}|[0-9]{2})\b`)

const workbenchPeriodCallPrefix = "source-period-"
const maxWorkbenchPeriodSources = 8

// Year labels are text from this question, never an inferred business-year
// calendar. The shortened variant is only another spelling to search in cells.
func workbenchPeriodLabels(query string) []string {
	var result []string
	seen := map[string]bool{}
	for _, indices := range workbenchExplicitPeriod.FindAllStringSubmatchIndex(query, 4) {
		// A slash calendar date such as 2022/09/12 is not a year-pair label.
		if strings.HasPrefix(strings.TrimLeft(query[indices[1]:], " \t"), "/") {
			continue
		}
		left, right := query[indices[2]:indices[3]], query[indices[4]:indices[5]]
		for _, label := range []string{left + "/" + right, left[2:] + "/" + right[len(right)-2:]} {
			if !seen[label] {
				seen[label] = true
				result = append(result, label)
			}
		}
	}
	return result
}

func periodSourceKey(dataset, sheet string) string { return dataset + "\x00" + sheet }

// A successful series directory proves the dataset and sheet exist in this
// caller's authorized source service. Inspect that sheet's explicit year label
// now, so a whole-workbook truncation cannot hide an unparsed annual block.
// Calls stay in the same step and execution boundary as model-requested tools.
func (e *AgentEngine) inspectWorkbenchPeriodSources(ctx context.Context, state *types.AgentState, step *types.AgentStep, query, sessionID, messageID string) {
	if e.config == nil || !e.config.WorkbenchDefault || e.toolRegistry == nil || state == nil || step == nil {
		return
	}
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() || ctx.Err() != nil {
		return
	}
	labels := workbenchPeriodLabels(query)
	if len(labels) == 0 {
		return
	}
	toolName := "structured_inspect_source"
	if _, err := e.toolRegistry.GetTool(toolName); err != nil {
		toolName = agenttools.ToolStructuredData
		if _, err := e.toolRegistry.GetTool(toolName); err != nil {
			return
		}
	}
	seen := map[string]bool{}
	for _, previous := range state.RoundSteps {
		for _, call := range previous.ToolCalls {
			if strings.HasPrefix(call.ID, workbenchPeriodCallPrefix) {
				args := call.Args
				if call.Name == agenttools.ToolStructuredData {
					args, _ = args["arguments"].(map[string]any)
				}
				dataset, _ := args["dataset_id"].(string)
				sheet, _ := args["sheet"].(string)
				seen[periodSourceKey(dataset, sheet)] = true
			}
		}
	}
	// Copy the current calls: supplemental executions append to this same step.
	listed := append([]types.ToolCall(nil), step.ToolCalls...)
	for _, call := range listed {
		isDirectory := call.Name == "structured_list_series" ||
			(call.Name == agenttools.ToolStructuredData && call.Args["operation"] == "list_series")
		if !isDirectory || call.Result == nil || !call.Result.Success {
			continue
		}
		dataset, _ := call.Result.Data["dataset_id"].(string)
		series, _ := call.Result.Data["series"].([]any)
		if dataset == "" {
			continue
		}
		for _, value := range series {
			item, ok := value.(map[string]any)
			if !ok {
				continue
			}
			sheet, _ := item["sheet"].(string)
			key := periodSourceKey(dataset, sheet)
			if sheet == "" || seen[key] || len(seen) >= maxWorkbenchPeriodSources || ctx.Err() != nil {
				continue
			}
			seen[key] = true // At most one attempt, even if source inspection fails.
			args := map[string]any{"dataset_id": dataset, "sheet": sheet, "searches": labels,
				"max_cells": 400, "max_matches": 10, "padding_rows": 3, "padding_cols": 3}
			var payload any = args
			if toolName == agenttools.ToolStructuredData {
				payload = map[string]any{"operation": "inspect_source", "arguments": args}
			}
			raw, _ := json.Marshal(payload)
			tc := types.LLMToolCall{ID: fmt.Sprintf("%s%s-%d", workbenchPeriodCallPrefix, messageID, len(seen)), Function: types.FunctionCall{Name: toolName, Arguments: string(raw)}}
			e.executeSingleToolCall(ctx, tc, len(step.ToolCalls), step, state.CurrentRound, state.CurrentRound+1, sessionID, messageID)
		}
	}
}
