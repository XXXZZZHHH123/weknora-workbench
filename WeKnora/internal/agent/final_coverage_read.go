package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	finalCoverageReadLimit        = 4
	finalCoverageReadBudget       = 10 * time.Second
	finalCoverageReadMinRemaining = 15 * time.Second
)

// This exception belongs only to practical source-budget synthesis. It never
// reopens the ordinary executor or changes source bindings, iteration limits,
// candidate repair allowances, or the shared final deadline.
func (e *AgentEngine) finalCoverageRead(ctx context.Context, query string, state *types.AgentState, sessionID, assistantMessageID string, messages []chat.Message, decision candidateDecision) (*types.AgentStep, error) {
	return e.boundedCoverageRead(ctx, query, state, sessionID, assistantMessageID, messages, decision, true)
}

func (e *AgentEngine) boundedCoverageRead(ctx context.Context, query string, state *types.AgentState, sessionID, assistantMessageID string, messages []chat.Message, decision candidateDecision, terminal bool) (*types.AgentStep, error) {
	deadline, bounded := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time)
	if outer, ok := ctx.Deadline(); ok && (!bounded || outer.Before(deadline)) {
		deadline = outer
	}
	if terminal && (!bounded || !e.standardAnswerBudgetEligible(ctx, state) || time.Until(deadline) < finalCoverageReadMinRemaining) {
		return nil, nil
	}
	coverage := e.documentCoverage(state)
	if !e.config.WorkbenchDefault || decision.Disposition != candidateNeedSource || decision.ReasonCode != "discovery_coverage_recovery" ||
		e.turnReview.FinalCoverageReadAttempted || e.turnReview.DocumentCoverageRecoveryUsed ||
		!e.withinIterationBudget(state.CurrentRound+1) {
		return nil, nil
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return nil, err
	}
	reader, err := e.toolRegistry.GetTool(agenttools.ToolReadDocument)
	if err != nil {
		return nil, nil
	}
	if _, native := reader.(*agenttools.ReadDocumentTool); !native {
		return nil, nil
	}
	known := e.finalCoverageKnownDocuments(state)
	var calls []types.LLMToolCall
	var navigation map[string][]map[string]any
	if coverage != nil {
		if len(coverage.Locations) > 0 {
			calls, known, navigation = coverage.targetedReads()
		} else {
			for _, row := range coverage.Bound {
				known[row.KnowledgeID] = true
			}
		}
	}
	if len(known) == 0 && len(calls) == 0 {
		return nil, nil
	}
	if review := e.turnReview.DiscoveryReview; coverage != nil || review != nil && review.ByDocument {
		e.turnReview.DocumentCoverageRecoveryUsed = true
	}
	e.turnReview.FinalCoverageReadAttempted = true
	status := "no_reads"
	defer func() {
		common.PipelineInfo(ctx, "Agent", "final_coverage_read", map[string]any{"status": status, "read_limit": finalCoverageReadLimit, "budget_ms": finalCoverageReadBudget.Milliseconds()})
	}()
	readCtx, cancel := context.WithTimeout(ctx, finalCoverageReadBudget)
	defer cancel()
	var reasoning string
	var signature string
	var reasoningMetadata types.ProviderMetadata
	remaining := finalCoverageReadLimit - len(calls)
	if remaining > 0 && len(known) > 0 {
		feedback, _ := json.Marshal(map[string]any{"question": query, "candidate": decision.Candidate, "review_feedback": e.answerReviewFeedback,
			"known_document_ids": known, "actual_document_coverage": coverage, "bound_navigation": navigation, "remaining_calls": remaining})
		input := append(append([]chat.Message(nil), messages...), chat.Message{Role: "system", Content: `There is one bounded batch to read missing source evidence. Use the full human task, original source observations and review feedback to choose up to remaining_calls read_document calls or none. Read only documents from known_document_ids. Known target pages already have physical-page calls reserved by the runtime. For unresolved pages use actual navigation within the bound document; prefer a verified physical page and otherwise use a literal heading or an observed continuation. Do not change source_scope, request images, guess IDs or request other tools. Use only actual returned evidence in the answer.
` + string(feedback)})
		disabled, parallel := false, true
		opts := &chat.ChatOptions{Temperature: 0, MaxCompletionTokens: 1024, ToolChoice: "auto", Thinking: &disabled, ParallelToolCalls: &parallel,
			Tools: e.modelContext.EncodeTools([]chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: reader.Name(), Description: reader.Description(), Parameters: reader.Parameters()}}})}
		planCtx := readCtx
		cancelPlan := func() {}
		if len(calls) > 0 {
			planCtx, cancelPlan = context.WithTimeout(readCtx, finalCoverageReadBudget/3)
		}
		result, callErr := e.streamLLMToEventBus(planCtx, input, opts, nil)
		planErr := planCtx.Err()
		cancelPlan()
		if result != nil && result.Usage != nil {
			state.TurnUsage.Accumulate(*result.Usage)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if callErr != nil || planErr != nil || result == nil || result.StreamError != "" ||
			(len(result.ToolCalls) > 0 && result.FinishReason != "tool_calls") ||
			len(result.ToolCalls) > remaining || !validFinalCoverageCalls(result.ToolCalls, known) {
			status = "unavailable_or_invalid_batch"
		} else {
			calls = append(calls, result.ToolCalls...)
			reasoning, signature, reasoningMetadata = result.ReasoningContent, result.ReasoningSignature, result.ReasoningMetadata
		}
	}
	if len(calls) == 0 || readCtx.Err() != nil {
		return nil, nil
	}

	if err := e.executionLeaseError(ctx); err != nil {
		return nil, err
	}
	state.CurrentRound++
	step := &types.AgentStep{Iteration: state.CurrentRound, ReasoningContent: reasoning,
		ReasoningSignature: signature, ReasoningMetadata: reasoningMetadata}
	response := &types.ChatResponse{ToolCalls: calls, FinishReason: "tool_calls"}
	// This is a terminal read batch, not an iteration-loop restart. The normal
	// pipeline retains per-call events, authorization, identities and failures.
	if e.runRoundTools(readCtx, state, response, step, sessionID, assistantMessageID) {
		status = "unexpected_suspension"
		return nil, nil
	}
	state.RoundSteps = append(state.RoundSteps, *step)
	status = "batch_completed"
	return step, nil
}

// Read known target pages directly. A document with no known page can be
// searched within its verified binding using the actual navigation.
func (v *documentCoverageView) targetedReads() ([]types.LLMToolCall, map[string]bool, map[string][]map[string]any) {
	var calls []types.LLMToolCall
	unknown := map[string]bool{}
	navigation := map[string][]map[string]any{}
	pagesByDocument := map[string][]int{}
	continued := map[string][]int{}
	var documents []string
	for _, location := range v.Locations {
		row := location.row
		if row == nil {
			continue
		}
		id := row.KnowledgeID
		if location.Page == 0 {
			unknown[id] = true
			navigation[id] = row.navigation
			continue
		}
		if (slices.Contains(row.readPages, location.Page) && location.LastRead == "page_read_complete") ||
			(row.UserPages != nil && !slices.Contains(row.UserPages, location.Page)) {
			continue
		}
		if len(calls) < finalCoverageReadLimit {
			for _, result := range row.PageResults {
				if coverageInt(result["page"]) == location.Page && coverageInt(result["next_offset"]) > 0 &&
					!slices.Contains(continued[id], location.Page) {
					calls = append(calls, physicalCoverageReadCall(len(calls), agenttools.ReadDocumentInput{
						ID: id, Page: location.Page, Offset: coverageInt(result["next_offset"]),
					}))
					continued[id] = append(continued[id], location.Page)
					break
				}
			}
		}
		if slices.Contains(continued[id], location.Page) {
			continue
		}
		if _, seen := pagesByDocument[id]; !seen {
			documents = append(documents, id)
		}
		if !slices.Contains(pagesByDocument[id], location.Page) {
			pagesByDocument[id] = append(pagesByDocument[id], location.Page)
		}
	}
	for _, id := range documents {
		pages := pagesByDocument[id]
		for len(pages) > 0 && len(calls) < finalCoverageReadLimit {
			count := min(len(pages), agenttools.MaxPhysicalPagesPerRead)
			input := agenttools.ReadDocumentInput{ID: id, Pages: pages[:count]}
			if count == 1 {
				input.Page, input.Pages = pages[0], nil
			}
			calls = append(calls, physicalCoverageReadCall(len(calls), input))
			pages = pages[count:]
		}
	}
	return calls, unknown, navigation
}

func physicalCoverageReadCall(index int, input agenttools.ReadDocumentInput) types.LLMToolCall {
	args, _ := json.Marshal(input)
	return types.LLMToolCall{
		ID: fmt.Sprintf("coverage-page-read-%d", index+1), Type: "function",
		Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: string(args)},
	}
}

// Require a successful native observation from THIS turn, not arbitrary model
// text, old registry handles, failed calls or an advisory plan's declared IDs.
func (e *AgentEngine) finalCoverageKnownDocuments(state *types.AgentState) map[string]bool {
	known := map[string]bool{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			registered, err := e.toolRegistry.GetTool(call.ExecutionName())
			if err != nil {
				continue
			}
			data := call.Result.Data
			var rows []map[string]any
			switch registered.(type) {
			case *agenttools.ReadDocumentTool:
				rows = []map[string]any{data}
			case *agenttools.SearchKnowledgeTool:
				rows = append(sourceNavigationRows(data["documents"]), sourceNavigationRows(data["results"])...)
			case *agenttools.ListDocumentsTool:
				rows = sourceNavigationRows(data["documents"])
			default:
				if call.ExecutionName() == agenttools.ToolBindDocumentScope && data["source_plan_validated"] == true {
					rows = sourceNavigationRows(data["documents"])
				}
			}
			for _, row := range rows {
				if id := sourceNavigationString(row, "knowledge_id"); id != "" {
					known[id] = true
				}
			}
		}
	}
	return known
}

// All-or-nothing validation prevents silently executing a truncated or expanded
// model plan. The read tool rechecks actual access and the CURRENT binding.
func validFinalCoverageCalls(calls []types.LLMToolCall, known map[string]bool) bool {
	if len(calls) > finalCoverageReadLimit {
		return false
	}
	for _, call := range calls {
		var args map[string]json.RawMessage
		var input agenttools.ReadDocumentInput
		if call.Function.Name != agenttools.ToolReadDocument || len(call.UnresolvedHandles) > 0 ||
			json.Unmarshal([]byte(call.Function.Arguments), &args) != nil ||
			json.Unmarshal([]byte(call.Function.Arguments), &input) != nil || !known[input.ID] {
			return false
		}
		// Even an empty source_scope is not allowed to replace the current plan.
		for key := range args {
			if strings.EqualFold(key, "source_scope") {
				return false
			}
		}
		if len(input.Images) > 0 || input.ImageMode != "" {
			return false
		}
	}
	return true
}
