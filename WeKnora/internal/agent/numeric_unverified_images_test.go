package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func numericUnverifiedChartFixture(t *testing.T, reviewer bool) (*AgentEngine, *types.AgentState, *renderChartTool, *types.ToolResult) {
	t.Helper()
	query := "Plot the actual observations and explain their changes."
	content, err := json.Marshal(map[string]any{"chart_issues": []string{}, "pending_deliverables": []string{"Explain the observed changes in the final answer."}, "required_x_type": "time", "assessment": "Actual source dates and observed readings match this chart."})
	require.NoError(t, err)
	model := &chartAxisReviewModel{response: &types.ChatResponse{Content: string(content), FinishReason: "stop"}}
	e := newTestEngine(t, model)
	e.config.AnswerReviewMode = "standard"
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: query}}
	e.turnReview.Query = query
	e.turnReview.Conversation = append([]chat.Message(nil), e.reviewConversation...)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}}
	ctx := chartContext(t)
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, &chartWriterFixture{}, "session", "message")
	if reviewer {
		tool.reviewPlan = e.chartPlanReviewer(query, state)
	}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(tool)
	tables, _ := chartSources(state.RoundSteps)
	require.Len(t, tables, 1)
	args := map[string]any{"operation": "render", "source_id": tables[0].ID, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{2}, "delivery_mode": "chart_with_analysis"}
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "chart-call", Name: renderChartToolName, Args: args, Result: result}}})
	return e, state, tool, result
}

func TestNumericUnverifiedImagesAcceptsActualReviewedRenderWithPendingProse(t *testing.T) {
	e, state, tool, _ := numericUnverifiedChartFixture(t, true)
	require.Empty(t, tool.receipts, "the image is not a complete-task chart-only approval")
	require.Len(t, tool.renderReceipts, 1)
	for _, candidate := range []string{
		"![observations](resource://abcdefghijklmnopqrstuv)\n\nChange: 93.",
		"![observations][chart]\n\n[chart]: resource://abcdefghijklmnopqrstuv\n\nChange: 93.",
		"[![observations](resource://abcdefghijklmnopqrstuv)](https://example.invalid/details)",
	} {
		require.True(t, e.numericUnverifiedImagesCurrent(candidate, state), candidate)
	}
}

func TestNumericUnverifiedTerminalDeliveryKeepsRealChartAndCitation(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
	e, state, _, _ := numericUnverifiedChartFixture(t, true)
	_, cited := citationDeliveryFixture(t, &mockChat{}, true)
	state.RoundSteps = append(state.RoundSteps, cited.RoundSteps...)
	numeric, _, _ := numericRecoveryFixture()
	state.RoundSteps = append(state.RoundSteps, numeric.RoundSteps...)
	messages := append([]chat.Message(nil), e.turnReview.Conversation...)
	for _, step := range state.RoundSteps {
		messages = e.appendToolResults(messages, step)
	}
	const image = "![observations](resource://abcdefghijklmnopqrstuv)"
	const citation = `<kb chunk_id="current-b" />`
	candidate := image + "\n\nThe observed change is 93 kg." + citation
	decision := e.checkCandidate(t.Context(), e.turnReview.Query, candidate, "", state, messages, false)
	require.Equal(t, candidateNumericFailure, decision.Disposition)
	require.Equal(t, []string{"93"}, decision.Unsupported)
	e.turnReview.Repairs = maxTurnCandidateRepairs
	e.commitTerminalResult(t.Context(), state, "session", numericFailureFallback, "", "failed", false)
	require.Contains(t, state.FinalAnswer, image)
	require.Contains(t, state.FinalAnswer, citation)
	require.Contains(t, state.FinalAnswer, "93〔数字待核对〕")
	require.NotNil(t, e.turnReview.NumericUnverifiedDelivery)
	before := state.FinalAnswer
	e.reviewConversationReady, e.reviewConversation = false, nil
	e.emitCompletionEvent(t.Context(), state, "session", "message", time.Now())
	require.Equal(t, strings.Replace(before, citation, "〔Actual source〕", 1), state.FinalAnswer)
	require.False(t, types.HasAgentAnswerMarkup(state.FinalAnswer))
	require.Len(t, state.KnowledgeRefs, 1)
	require.Equal(t, "current-b", state.KnowledgeRefs[0].ID)
}

func TestNumericUnverifiedImagesRejectsUnboundOrChangedChart(t *testing.T) {
	for _, change := range []string{"no-receipt", "failed", "call-id", "task", "data", "output", "source"} {
		t.Run(change, func(t *testing.T) {
			e, state, tool, result := numericUnverifiedChartFixture(t, true)
			switch change {
			case "no-receipt":
				tool.renderReceipts = nil
			case "failed":
				result.Success = false
			case "call-id":
				state.RoundSteps[1].ToolCalls[0].ID = "different-call"
			case "task":
				e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Use another source instead."})
			case "data":
				result.Data["source_id"] = "different-source"
			case "output":
				result.Output += " changed"
			case "source":
				state.RoundSteps[0].ToolCalls[0].Result.Data["rows"] = []any{map[string]any{"date": "2025-01-01", "value": 111.0}}
			}
			require.False(t, e.numericUnverifiedImagesCurrent("![observations](resource://abcdefghijklmnopqrstuv)", state))
		})
	}
	e, state, _, _ := numericUnverifiedChartFixture(t, false)
	require.False(t, e.numericUnverifiedImagesCurrent("![observations](resource://abcdefghijklmnopqrstuv)", state), "successful rendering without a semantic receipt cannot qualify")
}

func TestNumericUnverifiedImagesChecksActualMarkdownDestinations(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	state := &types.AgentState{}
	for _, candidate := range []string{
		"![external](https://example.invalid/image.png)",
		"![unread](resource://abcdefghijklmnopqrstuv)",
		"![unread][missing]\n\n[missing]: resource://abcdefghijklmnopqrstuv",
		"![empty]()",
	} {
		require.False(t, e.numericUnverifiedImagesCurrent(candidate, state), candidate)
	}
	for _, candidate := range []string{
		"The visible value is 93.",
		"[ordinary link](https://example.invalid/image.png)",
		"`![example](https://example.invalid/image.png)`",
		"```markdown\n![example](resource://abcdefghijklmnopqrstuv)\n```",
		"\\![escaped](https://example.invalid/image.png)",
	} {
		require.True(t, e.numericUnverifiedImagesCurrent(candidate, state), candidate)
	}
}

func TestNumericUnverifiedImagesRejectsNativeHTMLImageBypasses(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	state := &types.AgentState{}
	for _, candidate := range []string{
		`Inline <img src="https://example.invalid/image.png"> image.`,
		"<IMG\n alt='observations'\n SRC='resource://abcdefghijklmnopqrstuv' />",
		"<div>\n<img src='https://example.invalid/image.png'>\n</div>",
		`<picture><source srcset="https://example.invalid/image.png"><img src="resource://abcdefghijklmnopqrstuv"></picture>`,
		`<svg><image href="https://example.invalid/image.png" /></svg>`,
	} {
		require.False(t, e.numericUnverifiedImagesCurrent(candidate, state), candidate)
	}
	for _, candidate := range []string{
		`Literal code: ` + "`<img src='https://example.invalid/image.png'>`",
		"```html\n<IMG SRC='https://example.invalid/image.png'>\n```",
		`<!-- <img src="https://example.invalid/image.png"> -->`,
		`Escaped &lt;img src="https://example.invalid/image.png"&gt;`,
		`Observed value.<kb chunk_id="current-b" />`,
	} {
		require.True(t, e.numericUnverifiedImagesCurrent(candidate, state), candidate)
	}
}

func TestNumericUnverifiedImagesRequiresNativeBoundReadPixels(t *testing.T) {
	e := newTestEngine(t, &mockChat{})
	const candidate = "![original](resource://AbCdEfGhIjKlMnOpQrStUv)"
	state := boundDocumentImageState()
	require.True(t, e.numericUnverifiedImagesCurrent(candidate, state))
	call := &state.RoundSteps[0].ToolCalls[0]
	call.Target = &types.ToolCallTarget{Name: call.Name, Args: call.Args}
	call.Name = "workbench_call_tool"
	require.True(t, e.numericUnverifiedImagesCurrent(candidate, state), "the runtime-resolved native tool keeps its read binding")
	for _, change := range []string{"hash", "pixels", "catalog", "chunk", "failed", "other-tool", "geometry-only"} {
		t.Run(change, func(t *testing.T) {
			state := boundDocumentImageState()
			call := &state.RoundSteps[0].ToolCalls[0]
			switch change {
			case "hash":
				call.Result.Data["image_evidence"].([]map[string]any)[0]["sha256"] = strings.Repeat("0", 64)
			case "pixels":
				call.Result.Images = nil
			case "catalog":
				call.Result.Data["image_catalog"] = nil
			case "chunk":
				call.Result.Data["image_evidence"].([]map[string]any)[0]["chunk_id"] = "unread-chunk"
			case "failed":
				call.Result.Success = false
			case "other-tool":
				call.Name = "untrusted_tool"
			case "geometry-only":
				state = boundGeometryOnlyDocumentState()
			}
			require.False(t, e.numericUnverifiedImagesCurrent(candidate, state))
		})
	}
	require.False(t, e.numericUnverifiedImagesCurrent(candidate+"\n\n![unknown](resource://abcdefghijklmnopqrstuv)", boundDocumentImageState()), "every actual image must have its own successful current execution")
}
