package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

const toolStopSynthesisBudget = 60 * time.Second

type toolStopSynthesisDeadlineKey struct{}
type toolStopReasonKey struct{}
type boundedChartAnswerKey struct{}

type boundedChartAnswer struct {
	markdown string
	evidence string
}

func (e *AgentEngine) boundedChartDelivery(query string, messages []chat.Message, state *types.AgentState) *boundedChartAnswer {
	var blocks []string
	var evidence []map[string]any
	seen := map[string]bool{}
	for _, result := range e.currentRenderedCharts(query, messages, state) {
		rows, ok := result.data["selected_rows"].([][]string)
		columns, _ := result.data["selected_columns"].([]chart.Column)
		if !ok || len(rows) == 0 || len(columns) < 2 || seen[result.handle] {
			continue
		}
		if _, ok := types.ParseResourcePath(result.handle); !ok {
			continue
		}
		seen[result.handle] = true
		block := "![图表](" + result.handle + ")"
		caption, _ := result.data["coverage_caption"].(string)
		if caption == "" && result.data["source_truncated"] == true {
			caption = "图中仅包含本次查询返回的部分记录。"
		} else if caption == "" {
			caption = "图中范围以本次查询返回的记录为准。"
		}
		blocks = append(blocks, block+"\n\n"+caption)
		labels := make([]map[string]string, len(columns))
		for i, column := range columns {
			labels[i] = map[string]string{"name": column.Label(), "unit": chartExplicitUnit(column)}
		}
		evidence = append(evidence, map[string]any{"columns": labels, "rows": rows, "coverage": caption})
	}
	if len(blocks) == 0 {
		return nil
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return nil
	}
	return &boundedChartAnswer{markdown: strings.Join(blocks, "\n\n"), evidence: string(payload)}
}

// Stop unproductive result preparation without discarding already executed
// evidence. This is one terminal, tool-free synthesis under the ordinary
// numeric, citation and source checks, never approval of an unchecked draft.
// Pure document stalls retain their existing no-extra-model-call boundary.
func (e *AgentEngine) finishResultWorkflowStall(ctx context.Context, state *types.AgentState, query, sessionID string, messages []chat.Message, reasons ...string) (bool, error) {
	if !hasResultWorkflowEvidence(state.RoundSteps) {
		return false, nil
	}
	common.PipelineInfo(ctx, "Agent", "result_workflow_stalled", map[string]any{"round": state.CurrentRound})
	reason := "Repeated result preparation did not produce new evidence."
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return true, e.finishBoundedResultAnswer(ctx, query, state, sessionID, messages, reason, standardAnswerFinalBudget, sourceProgressStalledFallback)
}

// Both result-workflow stalls and exhausted iterations use this single no-tool
// synthesis and ordinary candidate validation. Keep the parent context live so
// a child deadline can still publish the existing bounded failure response.
func (e *AgentEngine) finishBoundedResultAnswer(
	ctx context.Context, query string, state *types.AgentState, sessionID string, messages []chat.Message,
	reason string, budget time.Duration, fallback string, chartAnswer ...*boundedChartAnswer,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, toolStopReasonKey{}, reason)
	deadline := time.Now().Add(budget)
	if existing, ok := ctx.Value(toolStopSynthesisDeadlineKey{}).(time.Time); ok && existing.Before(deadline) {
		deadline = existing
	}
	finalCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	options := finalAnswerOptions{stopReason: reason}
	if len(chartAnswer) > 0 {
		options.chartAnswer = chartAnswer[0]
		if options.chartAnswer != nil {
			finalCtx = context.WithValue(finalCtx, boundedChartAnswerKey{}, options.chartAnswer)
		}
	}
	if err := e.streamFinalAnswerToEventBus(finalCtx, query, state, sessionID, messages, options); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if leaseErr := e.executionLeaseError(ctx); leaseErr != nil {
			return leaseErr
		}
		if options.chartAnswer != nil {
			common.PipelineWarn(ctx, "Agent", "chart_text_synthesis_incomplete", map[string]any{"error": err.Error()})
			e.commitTerminalResult(ctx, state, sessionID, options.chartAnswer.markdown+"\n\n文字分析未能完成。", "", "chart_partial", false)
			return nil
		}
		e.commitTerminalResult(ctx, state, sessionID, fallback, "", "failed", false)
		return fmt.Errorf("failed to synthesize final answer after tool execution stopped: %w", err)
	}
	if strings.TrimSpace(state.FinalAnswer) == "" {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.commitTerminalResult(ctx, state, sessionID, fallback, "", "failed", false)
		return fmt.Errorf("final synthesis ended without a committed answer")
	}
	state.IsComplete = true
	return nil
}

func hasResultWorkflowEvidence(steps []types.AgentStep) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			if len(executedDocumentTableResults(call)) > 0 {
				return true
			}
			operation, native := agenttools.StructuredOperationFromCall(call)
			if !native || !structuredProgressHasResults(operation) {
				continue
			}
			contract, err := structuredresult.ValidatePayload(call.Result.Data)
			if err == nil && contract != nil {
				return true
			}
		}
	}
	return false
}
