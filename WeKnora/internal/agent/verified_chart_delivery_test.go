package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const verifiedChartQuery = "Draw only the observed readings over their actual dates."

type verifiedChartFixtureOptions struct {
	wrapped, analysis, incomplete, omittedTaskImage, noReviewer bool
}

type verifiedChartCandidateFixture struct {
	engine   *AgentEngine
	model    *chartReviewModel
	state    *types.AgentState
	tool     *renderChartTool
	result   *types.ToolResult
	ctx      context.Context
	messages []chat.Message
	args     map[string]any
}

type secondVerifiedChartWriter struct {
	chartWriterFixture
}

func (w *secondVerifiedChartWriter) SaveChart(ctx context.Context, request chart.SaveRequest) (*chart.Delivery, error) {
	delivery, err := w.chartWriterFixture.SaveChart(ctx, request)
	if err != nil {
		return nil, err
	}
	delivery.Handle, delivery.Index = "resource://bbbbbbbbbbbbbbbbbbbbbb", 3
	return delivery, nil
}

// Use the actual render tool and independent reviewer so only the server's
// full-task semantic verdict can create the private receipt used by this path.
func newVerifiedChartCandidateFixture(t *testing.T, options verifiedChartFixtureOptions) *verifiedChartCandidateFixture {
	t.Helper()
	ctx := chartContext(t)
	verdict := map[string]any{"accepted": true, "complete_delivery": !options.incomplete, "required_x_type": "time", "assessment": "The actual dates, measured readings, missing observation and source scope satisfy the complete chart-only task.", "feedback": ""}
	encoded, err := json.Marshal(verdict)
	require.NoError(t, err)
	model := &chartReviewModel{response: string(encoded)}
	e := newTestEngine(t, model)
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
	e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "chart", Criteria: verifiedChartQuery}}}
	messages := []chat.Message{{Role: "system", Content: "Use the actual current source observations."}, {Role: "user", Content: verifiedChartQuery}}
	if options.omittedTaskImage {
		messages[1].Images = []string{"https://example.invalid/actual-task-reference.png"}
	}
	e.reviewConversationReady = true
	e.reviewConversation = conversationThroughLastHuman(messages)
	query := queryChartFixture()
	query.Result.Data["actual_first_date"] = "2025-01-01"
	queryOutput, err := json.Marshal(query.Result.Data)
	require.NoError(t, err)
	query.Result.Output = string(queryOutput)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{query}}}}
	require.True(t, e.needsSourceConsistencyCheck(state), "rejected candidates must exercise the actual source critic")
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return state.RoundSteps }, &chartWriterFixture{}, "session", "message")
	if !options.noReviewer {
		tool.reviewPlan = e.chartPlanReviewer(verifiedChartQuery, state)
	}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(tool)
	tables, _ := chartSources(state.RoundSteps)
	require.Len(t, tables, 1)
	mode := "chart_only"
	if options.analysis {
		mode = "chart_with_analysis"
	}
	args := map[string]any{"operation": "render", "source_id": tables[0].ID, "chart_type": "line", "x_type": "time", "x_column": 0, "y_columns": []int{2}, "delivery_mode": mode}
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	call := types.ToolCall{ID: "chart-call", Name: renderChartToolName, Args: args, Result: result}
	if options.wrapped {
		call.Name, call.Args = "workbench_call_tool", map[string]any{"tool": renderChartToolName, "arguments": args}
		call.Target = &types.ToolCallTarget{Name: renderChartToolName, Args: args}
	}
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	for _, step := range state.RoundSteps {
		messages = e.appendToolResults(messages, step)
	}
	return &verifiedChartCandidateFixture{engine: e, model: model, state: state, tool: tool, result: result, ctx: ctx, messages: messages, args: args}
}

func (f *verifiedChartCandidateFixture) rejectProse(t *testing.T, candidate string, reviewCalls ...int) candidateDecision {
	t.Helper()
	wantCalls := 1
	if len(reviewCalls) != 0 {
		wantCalls = reviewCalls[0]
	}
	for range wantCalls {
		f.model.responses = append(f.model.responses, sourceConsistencyVerdict(`{"complete":false,"feedback":"Review the exact chart and the additional candidate claims before delivering them.","next_action":"revise_answer"}`))
	}
	before := len(f.model.calls)
	decision := f.engine.checkCandidate(f.ctx, verifiedChartQuery, candidate, "", f.state, f.messages, false)
	require.NoError(t, decision.Err, "the fixture supplies the complete bounded source-review response sequence")
	require.Equal(t, candidateRevise, decision.Disposition)
	require.Equal(t, candidate, decision.Candidate, "a skipped narrowing must preserve the full candidate for the source critic")
	require.Len(t, f.model.calls, before+wantCalls, "unqualified chart-plus-prose must still reach the original source critic")
	require.GreaterOrEqual(t, callWithFormatFrom(&f.model.mockChat, f.engine.withDeliveryCheckSchema(sourceConsistencySchema), before), before)
	// The actual reviewer receives the runtime's normal model encoding. Resource
	// handles and unknown inline references must not change that codec contract.
	rawInput, err := json.Marshal(map[string]string{"candidate_answer": candidate})
	require.NoError(t, err)
	encoded := f.engine.modelContext.EncodeMessages([]chat.Message{{Role: "user", Content: string(rawInput)}})
	var expected map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded[0].Content), &expected))
	for callIndex := before; callIndex < before+wantCalls; callIndex++ {
		require.Equal(t, expected["candidate_answer"], candidateReviewInput(t, &f.model.mockChat, callIndex, "candidate_answer")["candidate_answer"])
	}
	if wantCalls == 2 {
		reminded := false
		for _, message := range f.model.calls[before+1] {
			reminded = reminded || message.Role == "user" && strings.HasPrefix(message.Content, "Your verdict rejects the candidate, but you have not inspected this review's omitted actual results.")
		}
		require.True(t, reminded, "an unresolved fake citation retains the bounded evidence-inspection reminder before rejection")
	}
	return decision
}

func TestVerifiedChartCandidateNarrowingRebindsExactDeliveredCandidate(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "resolved_proxy"}[wrapped], func(t *testing.T) {
			fixture := newVerifiedChartCandidateFixture(t, verifiedChartFixtureOptions{wrapped: wrapped})
			for _, separator := range []string{"\n\n", "\r\n\r\n"} {
				candidate := fixture.result.Output + separator + "This extra description has not been reviewed."
				require.False(t, fixture.engine.isVerifiedChartDelivery(verifiedChartQuery, candidate, fixture.state, fixture.messages), "the original prose is never certified")
				decision := fixture.engine.checkCandidate(fixture.ctx, verifiedChartQuery, candidate, "", fixture.state, fixture.messages, false)
				require.Equal(t, candidateAccepted, decision.Disposition)
				require.Equal(t, fixture.result.Output, decision.Candidate)
				require.Equal(t, candidateTextDigest(fixture.result.Output), decision.CandidateHash)
				require.NotEqual(t, candidateTextDigest(candidate), decision.CandidateHash)
				require.True(t, fixture.engine.isVerifiedChartDelivery(verifiedChartQuery, decision.Candidate, fixture.state, fixture.messages))
				require.True(t, fixture.engine.candidateApprovalCurrent(decision.Candidate, fixture.state))
				require.False(t, fixture.engine.candidateApprovalCurrent(candidate, fixture.state))
				require.Empty(t, fixture.model.calls, "narrowing neither approves the prose nor calls another critic")
			}
		})
	}
}

func TestVerifiedChartCandidateNormalFinishPublishesOnlySavedMarkdown(t *testing.T) {
	fixture := newVerifiedChartCandidateFixture(t, verifiedChartFixtureOptions{wrapped: true})
	candidate := fixture.result.Output + "\n\nAn unreviewed extra conclusion should not be published."
	encoded := fixture.engine.modelContext.EncodeMessages([]chat.Message{{Role: "assistant", Content: candidate}})[0].Content
	fixture.model.responses = []mockResponse{chatAnswer(encoded)}
	recorder := &terminalAnswerRecorder{}
	recorder.attach(fixture.engine.eventBus)
	var completed event.AgentCompleteData
	var approved bool
	fixture.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		completed = evt.Data.(event.AgentCompleteData)
		approved = fixture.engine.candidateApprovalCurrent(fixture.state.FinalAnswer, fixture.state)
		return nil
	})
	_, err := fixture.engine.executeLoop(fixture.ctx, fixture.state, verifiedChartQuery, fixture.messages, emptyTools(), "session", "message")
	require.NoError(t, err)
	require.True(t, approved)
	require.True(t, fixture.state.IsComplete)
	require.Equal(t, fixture.result.Output, fixture.state.FinalAnswer)
	require.Equal(t, fixture.result.Output, completed.FinalAnswer)
	require.Equal(t, fixture.result.Output, fixture.engine.turnReview.Last.Candidate)
	require.Equal(t, candidateTextDigest(fixture.result.Output), fixture.engine.turnReview.Last.CandidateHash)
	require.Len(t, fixture.model.calls, 1, "only answer generation runs after the actual pre-render semantic check")
	require.Equal(t, -1, callWithFormat(&fixture.model.mockChat, sourceConsistencySchema))
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, fixture.result.Output, visible, "the discarded prose must not leak into the published final-answer stream")
	stored, err := json.Marshal(types.Message{Content: fixture.state.FinalAnswer})
	require.NoError(t, err)
	var replay types.Message
	require.NoError(t, json.Unmarshal(stored, &replay))
	require.Equal(t, fixture.result.Output, replay.Content)
}

func TestVerifiedChartCandidateNarrowingRequiresCompletePrivateSingleChartContract(t *testing.T) {
	for _, test := range []struct {
		name    string
		options verifiedChartFixtureOptions
		mutate  func(*verifiedChartCandidateFixture)
	}{
		{name: "chart_with_analysis", options: verifiedChartFixtureOptions{analysis: true}},
		{name: "incomplete_delivery", options: verifiedChartFixtureOptions{incomplete: true}},
		{name: "omitted_task_view", options: verifiedChartFixtureOptions{omittedTaskImage: true}},
		{name: "no_semantic_receipt", options: verifiedChartFixtureOptions{noReviewer: true}},
		{name: "public_accepted_is_not_receipt", options: verifiedChartFixtureOptions{noReviewer: true}, mutate: func(f *verifiedChartCandidateFixture) {
			f.result.Data["semantic_validation"] = map[string]any{"accepted": true, "complete_delivery": true, "full_task_view": true}
		}},
		{name: "mixed_delivery", mutate: func(f *verifiedChartCandidateFixture) {
			f.engine.taskPlan.DeliveryRequirements = append(f.engine.taskPlan.DeliveryRequirements, taskDeliveryRequirement{Kind: "text", Criteria: "Also provide the requested explanation."})
		}},
		{name: "multiple_chart_requirements", mutate: func(f *verifiedChartCandidateFixture) {
			f.engine.taskPlan.DeliveryRequirements = append(f.engine.taskPlan.DeliveryRequirements, taskDeliveryRequirement{Kind: "chart", Criteria: "Also provide a second view."})
		}},
		{name: "missing_plan", mutate: func(f *verifiedChartCandidateFixture) { f.engine.taskPlan = nil }},
		{name: "stale_user_task", mutate: func(f *verifiedChartCandidateFixture) {
			f.engine.reviewConversation = append(f.engine.reviewConversation, chat.Message{Role: "user", Content: "Use a different source scope."})
		}},
		{name: "changed_runtime_source_scope", mutate: func(f *verifiedChartCandidateFixture) {
			f.engine.config.KnowledgeIDs = []string{"different-authorized-source"}
		}},
		{name: "changed_underlying_source_rows", mutate: func(f *verifiedChartCandidateFixture) {
			f.state.RoundSteps[0].ToolCalls[0].Result.Data["rows"].([]any)[0].(map[string]any)["value"] = 18.0
		}},
		{name: "changed_underlying_source_version", mutate: func(f *verifiedChartCandidateFixture) {
			f.state.RoundSteps[0].ToolCalls[0].Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("cd", 32)
		}},
		{name: "stale_result", mutate: func(f *verifiedChartCandidateFixture) { f.result.Data["row_count"] = 999 }},
		{name: "changed_actual_output", mutate: func(f *verifiedChartCandidateFixture) { f.result.Output += " changed" }},
		{name: "different_call_id", mutate: func(f *verifiedChartCandidateFixture) {
			f.state.RoundSteps[1].ToolCalls[0].ID = "different-current-call"
		}},
		{name: "failed_current_render", mutate: func(f *verifiedChartCandidateFixture) { f.result.Success = false }},
		{name: "wrong_resolved_execution", options: verifiedChartFixtureOptions{wrapped: true}, mutate: func(f *verifiedChartCandidateFixture) {
			f.state.RoundSteps[1].ToolCalls[0].Target.Name = "another_actual_tool"
		}},
		{name: "wrong_resolved_operation", options: verifiedChartFixtureOptions{wrapped: true}, mutate: func(f *verifiedChartCandidateFixture) {
			f.state.RoundSteps[1].ToolCalls[0].Target.Args = map[string]any{"operation": "inspect"}
		}},
		{name: "no_private_receipts", mutate: func(f *verifiedChartCandidateFixture) { f.tool.receipts = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVerifiedChartCandidateFixture(t, test.options)
			candidate := fixture.result.Output + "\n\nThis extra description requires source review."
			if test.mutate != nil {
				test.mutate(fixture)
			}
			fixture.rejectProse(t, candidate)
		})
	}
}

func TestVerifiedChartCandidateNarrowingNeverChoosesSubsetOfMultipleRenders(t *testing.T) {
	fixture := newVerifiedChartCandidateFixture(t, verifiedChartFixtureOptions{})
	writer := &secondVerifiedChartWriter{}
	fixture.tool.writer = writer
	ctx := agenttools.WithToolExecContext(fixture.ctx, &agenttools.ToolExecContext{SessionID: "session", AssistantMessageID: "message", ToolCallID: "second-chart-call"})
	second := chartRun(t, fixture.tool, ctx, fixture.args)
	require.True(t, second.Success, second.Error)
	require.Len(t, writer.requests, 1)
	require.NotEmpty(t, writer.requests[0].PNG)
	require.NotEqual(t, fixture.result.Output, second.Output, "the two successful renders save distinct real artifacts")
	require.Len(t, fixture.tool.receipts, 2, "the second artifact must not overwrite the first private receipt")
	step := types.AgentStep{ToolCalls: []types.ToolCall{{ID: "second-chart-call", Name: renderChartToolName, Args: fixture.args, Result: second}}}
	fixture.state.RoundSteps = append(fixture.state.RoundSteps, step)
	fixture.messages = fixture.engine.appendToolResults(fixture.messages, step)
	candidate := second.Output + "\n\nThis description cannot select one result from several renders."
	// Two complete chart results exceed the compact generic evidence view.
	// They retain their exact companion/reader paths; the critic therefore
	// issues its existing one-time inspection reminder before a valid rejection.
	snapshot := fixture.engine.buildSourceReviewSnapshot(fixture.ctx, candidate, fixture.state, fixture.messages)
	require.False(t, snapshot.input["evidence_scope"].(reviewEvidenceScope).ViewComplete)
	require.NotNil(t, snapshot.reader)
	fixture.rejectProse(t, candidate, 2)
}

func TestVerifiedChartCandidateNarrowingPreservesMarkdownAndAttachmentBoundaries(t *testing.T) {
	for name, compose := range map[string]func(string) string{
		"indented_code": func(output string) string { return "    " + output + "\n\nAdditional description." },
		"tab_code":      func(output string) string { return "\t" + output + "\n\nAdditional description." },
		"inline_code":   func(output string) string { return "`" + output + "`\n\nAdditional description." },
		"code_fence":    func(output string) string { return "```\n" + output + "\n```\n\nAdditional description." },
		"html_comment":  func(output string) string { return "<!--\n" + output + "\n-->\n\nAdditional description." },
		"quote":         func(output string) string { return "> " + output + "\n\nAdditional description." },
		"link_wrapper": func(output string) string {
			return "[" + output + "](https://example.invalid)\n\nAdditional description."
		},
		"leading_text": func(output string) string {
			return "Description before the image.\n" + output + "\n\nAdditional description."
		},
		"same_line_text":     func(output string) string { return output + " Additional description." },
		"missing_blank_line": func(output string) string { return output + "\nAdditional description." },
		"duplicate_image":    func(output string) string { return output + "\n\n" + output },
		"second_image":       func(output string) string { return output + "\n\n![Another](resource://bbbbbbbbbbbbbbbbbbbbbb)" },
		"file_attachment":    func(output string) string { return output + "\n\n[File](resource://bbbbbbbbbbbbbbbbbbbbbb)" },
		"bare_resource":      func(output string) string { return output + "\n\nAdditional resource://bbbbbbbbbbbbbbbbbbbbbb" },
		"bare_alias":         func(output string) string { return output + "\n\nAdditional res://extra" },
		"inline_citation":    func(output string) string { return output + "\n\nA statement.<kb chunk_id=\"actual-source\" />" },
		"link":               func(output string) string { return output + "\n\nRead [support](https://example.invalid)." },
		"reference_link": func(output string) string {
			return output + "\n\nRead [support][source].\n[source]: https://example.invalid"
		},
		"bare_url":        func(output string) string { return output + "\n\nRead https://example.invalid" },
		"www_link":        func(output string) string { return output + "\n\nRead www.example.invalid" },
		"escaped_link":    func(output string) string { return output + "\n\n\\[escaped](resource://bbbbbbbbbbbbbbbbbbbbbb)" },
		"structured_tail": func(output string) string { return output + "\n\n# Additional analysis" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newVerifiedChartCandidateFixture(t, verifiedChartFixtureOptions{})
			if name == "inline_citation" {
				// Unknown citation evidence is incomplete. The original reviewer
				// asks once to inspect omitted evidence, then returns its valid
				// rejection; a one-response mock would be a protocol failure.
				fixture.rejectProse(t, compose(fixture.result.Output), 2)
			} else {
				fixture.rejectProse(t, compose(fixture.result.Output))
			}
		})
	}
}

func TestVerifiedChartCandidatePureMarkdownFastPathRemainsUnchanged(t *testing.T) {
	fixture := newVerifiedChartCandidateFixture(t, verifiedChartFixtureOptions{})
	decision := fixture.engine.checkCandidate(fixture.ctx, verifiedChartQuery, fixture.result.Output, "", fixture.state, fixture.messages, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, fixture.result.Output, decision.Candidate)
	require.Empty(t, fixture.model.calls)
	require.False(t, strings.Contains(decision.Candidate, "Additional description"))
}
