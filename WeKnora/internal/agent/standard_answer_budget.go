package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const standardAnswerToolRounds = 8
const standardAnswerActiveBudget = 60 * time.Second

const (
	standardAnswerModelCallBudget = 25 * time.Second
	standardAnswerFinalBudget     = 35 * time.Second
	standardAnswerDecisionTokens  = 4096
)

var errStandardAnswerModelBudget = errors.New("standard answer model-stage budget exhausted")

type standardAnswerDecisionKey struct{}
type standardAnswerFinalDeadlineKey struct{}

func standardPreliminaryAnswerNotice(ctx context.Context) string {
	if strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh") {
		return "以下是基于本轮已读资料的初步结果，尚不代表完整覆盖全部相关内容。"
	}
	return "These are preliminary findings from the sources read so far; they may not cover all relevant material."
}

func withStandardPreliminaryNotice(ctx context.Context, answer string) string {
	notice := standardPreliminaryAnswerNotice(ctx) + "\n\n"
	if strings.HasPrefix(answer, notice) {
		return answer
	}
	return notice + answer
}

func (e *AgentEngine) standardPreliminaryProse() bool {
	if e.taskPlan == nil || len(e.taskPlan.DeliveryRequirements) == 0 {
		return false
	}
	for _, requirement := range e.taskPlan.DeliveryRequirements {
		if requirement.Kind != "text" || requirement.Format != "prose" {
			return false
		}
	}
	return true
}

const standardAnswerBudgetNotice = `<preliminary_answer>
本轮只读文字取证已达到实用模式的阶段预算，工具调用已完整结束。只根据已取得的真实证据给出有用的初步结果，准确说明本轮已读内容尚未覆盖的要求；不能宣称全部记录、分组或任务已完整覆盖，不猜缺失值，不把未读取说成不存在。保留真实引用，只概括可直接核对的核心结论，不附加逐项分析。不得再请求工具、伪造执行或以未经核验的数字补齐答案。
</preliminary_answer>`

// This is an execution-protocol allowlist, not an intent classifier. Unknown
// tools, MCP proxies and source confirmation/session mutations retain their
// ordinary execution lifecycle. Binding an authorized document is source setup.
func (e *AgentEngine) standardAnswerSourceTool(name string, args map[string]any) bool {
	if e.toolRegistry == nil {
		return false
	}
	if _, err := e.toolRegistry.GetTool(name); err != nil {
		return false
	}
	if taskSourceProtocolDomain(name) == "structured" {
		if args == nil {
			args = map[string]any{}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return false
		}
		descriptor, _, actual := e.toolRegistry.StructuredInvocation(name, raw)
		return actual && (descriptor.EffectClass == "read" || descriptor.EffectClass == "protocol")
	}
	switch name {
	case agenttools.ToolSearchKnowledge, agenttools.ToolReadDocument, agenttools.ToolListDocuments,
		agenttools.ToolBindDocumentScope, agenttools.ToolWikiSearch, agenttools.ToolWikiReadPage,
		agenttools.ToolQueryKnowledgeGraph, agenttools.ToolDataSchema, agenttools.ToolDataAnalysis, agenttools.ToolDatabaseQuery,
		agenttools.ToolCommodityResearch,
		agenttools.ToolWebSearch, agenttools.ToolWebFetch,
		sourceCalculationToolName, documentTableCalculationToolName, documentTableAnalysisToolName:
		return true
	}
	return false
}

// The same conservative scope controls the tool barrier and model stages.
// Nothing here interprets the question or grants a tool additional authority.
func (e *AgentEngine) standardAnswerBudgetEligible(ctx context.Context, state *types.AgentState) bool {
	if e.config == nil || e.config.AnswerReviewMode != "standard" || e.taskPlan == nil ||
		state == nil || state.IsComplete || state.Suspended != nil || state.SuspendedStep != nil ||
		len(e.taskPlan.DeliveryRequirements) == 0 || e.holdCollaborationAnswer {
		return false
	}
	for _, delivery := range e.taskPlan.DeliveryRequirements {
		if delivery.Kind != "text" {
			return false
		}
	}
	needsSources := len(e.taskPlan.Evidence) > 0
	for _, name := range e.taskPlan.ToolNames {
		if !e.standardAnswerSourceTool(name, nil) {
			return false
		}
		needsSources = true
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || call.Result.Suspend != nil || len(call.Result.OutputFiles) > 0 ||
				call.Target != nil && call.Target.ServiceName != "structured_data" ||
				!e.standardAnswerSourceTool(call.Name, call.Args) {
				return false
			}
			needsSources = needsSources || call.Result.Success
		}
	}
	if !needsSources {
		return false // General knowledge/creative turns keep their ordinary streaming lifecycle.
	}
	if e.collaboration != nil && e.collaborationOwnsFailure(ctx) {
		return false // Any delegated work or unreadable ownership fails closed.
	}
	return true
}

// Check only at a completed tool barrier. The smaller ordinary iteration
// boundary keeps precedence, and preparation calls never consume tool rounds.
func (e *AgentEngine) standardAnswerBudgetReason(ctx context.Context, state *types.AgentState, started time.Time) string {
	if !e.standardAnswerBudgetEligible(ctx, state) || !e.withinIterationBudget(state.CurrentRound+1) {
		return ""
	}
	rounds := 0
	for _, step := range state.RoundSteps {
		if step.Iteration >= 0 && len(step.ToolCalls) > 0 {
			rounds++
		}
	}
	if rounds == 0 || rounds < standardAnswerToolRounds && time.Since(started) < standardAnswerActiveBudget {
		return ""
	}
	if rounds >= standardAnswerToolRounds {
		return "tool_rounds"
	}
	return "active_time"
}

func (e *AgentEngine) standardAnswerModelTimeRemaining(ctx context.Context, state *types.AgentState) (time.Duration, bool) {
	if !e.standardAnswerBudgetEligible(ctx, state) {
		return 0, false
	}
	return time.Until(agentExecutionStart(ctx).Add(standardAnswerActiveBudget)), true
}

// Only a model generation gets this deadline; the tool's execution context
// remains untouched. A cancelled/partial generation can never become a call.
func (e *AgentEngine) standardAnswerModelContext(ctx context.Context, state *types.AgentState) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return ctx, func() {}, err
	}
	remaining, eligible := e.standardAnswerModelTimeRemaining(ctx, state)
	if !eligible {
		return ctx, func() {}, nil
	}
	if remaining <= 0 {
		return ctx, func() {}, errStandardAnswerModelBudget
	}
	bounded, cancel := context.WithTimeoutCause(ctx, min(remaining, standardAnswerModelCallBudget), errStandardAnswerModelBudget)
	return context.WithValue(bounded, standardAnswerDecisionKey{}, true), cancel, nil
}

func (e *AgentEngine) standardAnswerDecisionCompletionBudget(ctx context.Context, budget int) int {
	if bounded, _ := ctx.Value(standardAnswerDecisionKey{}).(bool); bounded {
		limit := standardAnswerDecisionTokens
		if e.config.MaxCompletionTokens > 0 {
			limit = e.config.MaxCompletionTokens
		}
		// The sandbox file-write floor is unnecessary in a verified source-only
		// stage; explicit output choices still respect model/context ceilings.
		return min(budget, limit)
	}
	return budget // An explicit larger caller budget remains intact.
}

// Final generation, compaction and candidate repair share one deadline. It
// starts after the last complete tool barrier, never by cancelling a tool.
func (e *AgentEngine) standardAnswerFinalContext(ctx context.Context, state *types.AgentState) (context.Context, context.CancelFunc) {
	if !e.standardAnswerBudgetEligible(ctx, state) {
		return ctx, func() {}
	}
	deadline, exists := ctx.Value(standardAnswerFinalDeadlineKey{}).(time.Time)
	if !exists {
		deadline = time.Now().Add(standardAnswerFinalBudget)
		ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, deadline)
	}
	return context.WithDeadline(ctx, deadline)
}

func (e *AgentEngine) finishStandardAnswerBudget(
	ctx context.Context, state *types.AgentState, query, sessionID, messageID string,
	messages *[]chat.Message, guards *loopGuards,
	forcedReason ...string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if len(forcedReason) > 0 {
		if err := e.executionLeaseError(ctx); err != nil {
			return true, err
		}
	}
	reason := e.standardAnswerBudgetReason(ctx, state, agentExecutionStart(ctx))
	if len(forcedReason) > 0 && e.standardAnswerBudgetEligible(ctx, state) {
		reason = forcedReason[0]
	}
	if reason == "" {
		return false, nil
	}
	// A late human correction or background notice belongs to the next model
	// boundary; it cannot be completed under the old advisory plan's budget.
	injected := e.drainSteerMessages(ctx, state, messages, sessionID, messageID)
	injected += e.injectBackground(ctx, state, messages, true)
	if injected > 0 {
		e.clearTaskToolProjection(messages)
		e.taskPlan = nil
		e.resetDocumentTurnProgress(guards, query, state)
		return false, nil
	}
	if err := e.executionLeaseError(ctx); err != nil {
		return true, err
	}
	common.PipelineInfo(ctx, "Agent", "standard_answer_budget", map[string]any{
		"reason": reason, "tool_round_limit": standardAnswerToolRounds, "active_seconds": standardAnswerActiveBudget.Seconds(),
	})
	if e.eventBus != nil {
		_ = e.eventBus.Emit(ctx, event.Event{ID: generateEventID("preliminary"), Type: event.EventAgentProgress, SessionID: sessionID,
			Data: event.AgentProgressData{Content: standardPreliminaryAnswerNotice(ctx), Iteration: state.CurrentRound}})
	}
	err := e.streamFinalAnswerToEventBus(ctx, query, state, sessionID, restoreForFinalAnswer(*messages, guards), finalAnswerOptions{preliminary: true})
	return true, err
}
