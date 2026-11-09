package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const documentTableDeliveryFixtureRef = "document-table-view:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func documentTableDeliveryFixtureDirective() string {
	return `<document_table_view ref="` + documentTableDeliveryFixtureRef + `" />`
}

func TestDocumentTableDirectiveExpandsOnlyStandalonePresentation(t *testing.T) {
	directive := documentTableDeliveryFixtureDirective()
	const markdown = "| Original measure | Original amount |\n|---|---|\n| A | 12.500 |\n\n<kb chunk_id=\"actual-source\"/>"
	var refs []string
	result, count, err := expandDocumentTableDirectives("Observed results:\n\n"+directive+"\n\nSource limitations remain.\n", func(ref string) (string, error) {
		refs = append(refs, ref)
		return markdown, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, []string{documentTableDeliveryFixtureRef}, refs)
	require.Equal(t, "Observed results:\n\n"+markdown+"\n\nSource limitations remain.\n", result)
}

func TestDocumentTableDirectivePreservesCodeQuotedAndUnrecognizedLiterals(t *testing.T) {
	directive := documentTableDeliveryFixtureDirective()
	for name, text := range map[string]string{
		"fenced_backticks":  "```xml\n" + directive + "\n```\n",
		"fenced_tildes":     "~~~~xml\n~~~\n" + directive + "\n~~~~\n",
		"inline_code":       "Use `" + directive + "` as an example.",
		"multiline_code":    "`A literal example\n" + directive + "\nends here`\n",
		"indented_code":     "    " + directive + "\n",
		"tab_indented_code": "\t" + directive + "\n",
		"block_quote":       "> " + directive + "\n",
		"quoted_string":     `"` + directive + `"`,
		"escaped_html":      "\\" + directive,
		"html_comment":      "<!-- a literal example\n" + directive + "\n-->\n",
		"html_pre":          "<pre>\n" + directive + "\n</pre>\n",
		"html_wrong_close":  "<pre>\n</prelude>\n" + directive + "\n</pre>\n", "html_code": "<CODE class=\"literal\">\n" + directive + "\n</CODE>\n",
		"html_textarea":      "<textarea>\n" + directive + "\n</textarea>\n",
		"plain_reference":    documentTableDeliveryFixtureRef,
		"unrecognized_tag":   `<document_table_views ref="` + documentTableDeliveryFixtureRef + `" />`,
		"embedded_example":   "Example: " + directive,
		"html_encoded_tag":   "&lt;document_table_view ref=\"example\" /&gt;",
		"inline_quoted_html": "The literal \"" + directive + "\" is an example.",
	} {
		t.Run(name, func(t *testing.T) {
			result, count, err := expandDocumentTableDirectives(text, func(string) (string, error) {
				t.Fatal("literal examples have no executable authority")
				return "", nil
			})
			require.NoError(t, err)
			require.Zero(t, count)
			require.Equal(t, text, result)
		})
	}
}

func TestDocumentTableDirectiveUnmatchedBacktickDoesNotHideExecutableMarker(t *testing.T) {
	directive := documentTableDeliveryFixtureDirective()
	result, count, err := expandDocumentTableDirectives("An unmatched ` is ordinary text.\n"+directive, func(string) (string, error) {
		return "Actual source table", nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, "An unmatched ` is ordinary text.\nActual source table", result)
}

func TestDocumentTableDirectiveRejectsUnknownMalformedAndMixedViewsAtomically(t *testing.T) {
	valid := documentTableDeliveryFixtureDirective()
	unknown := `<document_table_view ref="not-a-current-source" />`
	for _, candidate := range []string{
		unknown,
		valid + "\n" + unknown,
		`<document_table_view ref='` + documentTableDeliveryFixtureRef + `' />`,
		`<document_table_view ref="` + documentTableDeliveryFixtureRef + `" extra="untrusted" />`,
		"<document_table_view",
		"</document_table_view>",
	} {
		result, count, err := expandDocumentTableDirectives(candidate, func(ref string) (string, error) {
			if ref != documentTableDeliveryFixtureRef {
				return "", errors.New("unknown runtime reference")
			}
			return "Actual source table", nil
		})
		require.Error(t, err)
		require.Empty(t, result, "failed expansion must not expose raw directives or a partial candidate")
		require.Zero(t, count)
	}
}

func TestDocumentTableDirectiveEnforcesPresentationBudgetsAndNoRecursion(t *testing.T) {
	directive := documentTableDeliveryFixtureDirective()
	for _, markdown := range []string{"", strings.Repeat("x", documentTableDeliveryMaxBytes+1), directive} {
		result, _, err := expandDocumentTableDirectives(directive, func(string) (string, error) { return markdown, nil })
		require.Error(t, err)
		require.Empty(t, result)
	}
	_, _, err := expandDocumentTableDirectives(strings.Repeat(directive+"\n", documentTableDeliveryMaxViews+1), func(string) (string, error) { return "Actual source table", nil })
	require.Error(t, err)
	_, _, err = expandDocumentTableDirectives(strings.Repeat(directive+"\n", 2), func(string) (string, error) { return strings.Repeat("x", documentTableDeliveryMaxBytes/2), nil })
	require.Error(t, err)
}

func TestDocumentTableStreamingGuardPreservesTextAndHoldsSplitProtocol(t *testing.T) {
	for _, text := range []string{"Plain text continues normally.\nThe next line too.", "普通中文说明。\n下一行继续。", "Use `" + documentTableDeliveryFixtureDirective() + "` literally.", "> " + documentTableDeliveryFixtureDirective(), "    " + documentTableDeliveryFixtureDirective(), "A trailing <docu"} {
		var guard documentTableStreamGuard
		var output strings.Builder
		for i := 0; i < len(text); i++ {
			part, held := guard.Feed(text[i : i+1])
			require.False(t, held)
			output.WriteString(part)
		}
		output.WriteString(guard.Flush())
		require.Equal(t, text, output.String(), "ordinary text and literal examples retain exact UTF-8 bytes")
	}
	var guard documentTableStreamGuard
	var output strings.Builder
	for _, part := range []string{"Normal introduction.\n", "<docu", "ment_table_", "view ref=\"unknown\" />", "\nMore draft text."} {
		safe, _ := guard.Feed(part)
		output.WriteString(safe)
	}
	output.WriteString(guard.Flush())
	require.True(t, guard.held)
	require.Equal(t, "Normal introduction.\n", output.String())
	require.NotContains(t, output.String(), "<docu", "even a split technical prefix must wait for complete validation")
}

func TestInventedDocumentTableDirectiveNeverStreamsOrPersistsWithoutEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
		{ResponseType: types.ResponseTypeAnswer, Content: "A normal introduction.\n"},
		{ResponseType: types.ResponseTypeAnswer, Content: "<docu"},
		{ResponseType: types.ResponseTypeAnswer, Content: "ment_table_view ref=\"invented-ref\" />", Done: true, FinishReason: "stop"},
	}}}}
	e := sourceConsistencyEngine(t, model)
	var emitted []string
	e.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt.Data.(event.AgentFinalAnswerData).Content)
		return nil
	})
	state, err := e.executeLoop(t.Context(), &types.AgentState{}, "Report the requested result.", emptyMessages(), emptyTools(), "session", "message")
	require.NoError(t, err)
	require.Equal(t, answerReviewUnavailableFallback, state.FinalAnswer)
	require.NotContains(t, strings.Join(emitted, ""), "<docu")
	require.NotContains(t, state.FinalAnswer, "invented-ref")
	require.Empty(t, e.turnReview.UnverifiedCandidate)
	require.Len(t, model.calls, 1, "unknown views fail before any source critic call or extra model work")
}

func documentTableFinalDeliveryFixture(t *testing.T, model *mockChat, selectedRows []int) (*AgentEngine, *types.AgentState, string, string, []chat.Message) {
	t.Helper()
	e := sourceConsistencyEngine(t, model)
	e.taskPlan = explicitTextDeliveryPlan()
	e.toolRegistry = agenttools.NewToolRegistry()
	state := sourceConsistencyState()
	state.RoundSteps[0].ToolCalls[0].Result.Data = map[string]any{
		"knowledge_id": "actual-document", "document": map[string]any{"knowledge_id": "actual-document", "title": "Synthetic source table"},
		"chunks": []any{map[string]any{"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content_revision": 1,
			"content": "Unit: kg\n| Measure | Amount |\n| --- | --- |\n| First | 12.500 |\n| Second | 3 |\n", "content_complete": true}},
	}
	messages := e.appendToolResults([]chat.Message{{Role: "system", Content: "Use actual source evidence."}, {Role: "user", Content: "Report all selected source observations."}}, state.RoundSteps[0])
	tool := newDocumentTableCalculationTool(func() []types.AgentStep { return state.RoundSteps }, e.toolRegistry)
	e.toolRegistry.RegisterTool(tool)
	request := map[string]any{"operation": "select", "chunk_id": "actual-chunk", "table_index": 0, "row_indices": selectedRows, "selected_columns": []int{0, 1}}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	result, err := tool.Execute(t.Context(), raw)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "selected-table-call", Name: documentTableCalculationToolName, Args: request, Result: result}}})
	messages = e.appendToolResults(messages, state.RoundSteps[1])
	ref, ok := result.Data["view_ref"].(string)
	require.True(t, ok)
	snapshot, err := resolveDocumentTableView(e.toolRegistry, state.RoundSteps, ref)
	require.NoError(t, err)
	return e, state, ref, snapshot.Markdown, messages
}

func TestDocumentTableViewDeliveryUsesExpandedBodyAcrossOrdinaryAndExceptionalPaths(t *testing.T) {
	for _, path := range []string{"ordinary", "stalled", "final"} {
		t.Run(path, func(t *testing.T) {
			model := &mockChat{}
			e, state, ref, markdown, messages := documentTableFinalDeliveryFixture(t, model, []int{1, 2})
			candidate := `<document_table_view ref="` + ref + `" />`
			model.responses = []mockResponse{chatAnswer(candidate), sourceAcceptedVerdict()}
			if path == "stalled" {
				model.responses = []mockResponse{sourceAcceptedVerdict()}
			}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			var completed event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				completed = evt.Data.(event.AgentCompleteData)
				return nil
			})
			query := "Report all selected source observations."
			switch path {
			case "ordinary":
				_, err := e.executeLoop(t.Context(), state, query, messages, e.buildToolsForLLM(), "session", "message")
				require.NoError(t, err)
			case "stalled":
				e.finishStalledTurn(t.Context(), state, "session", &types.ChatResponse{Content: candidate, FinishReason: "stop"}, false, query, messages)
			case "final":
				require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), query, state, "session", messages))
			}
			require.Equal(t, markdown, state.FinalAnswer, "the outer finalizer must consume the checked expanded candidate")
			require.Equal(t, markdown, e.turnReview.Last.Candidate)
			require.Equal(t, candidateTextDigest(markdown), e.turnReview.Last.CandidateHash)
			require.True(t, e.candidateApprovalCurrent(markdown, state), "the actual task snapshot remains current after executeLoop releases its live human context")
			require.True(t, e.turnReview.Last.SourceReviewAttempted)
			require.Empty(t, e.turnReview.Last.Unsupported, "program rendering preserves the original source quantities")
			require.NotContains(t, state.FinalAnswer, "document_table_view")
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, markdown, visible)
			// Re-check the post-loop completion boundary used by final persistence.
			e.emitCompletionEvent(t.Context(), state, "session", "message", time.Now())
			require.Equal(t, markdown, completed.FinalAnswer)
			require.True(t, e.candidateApprovalCurrent(completed.FinalAnswer, state))
			stored, err := json.Marshal(types.Message{Content: completed.FinalAnswer})
			require.NoError(t, err)
			var replay types.Message
			require.NoError(t, json.Unmarshal(stored, &replay))
			require.Equal(t, markdown, replay.Content)
			for _, step := range completed.AgentSteps.([]types.AgentStep) {
				require.NotContains(t, step.Thought, "<document_table_view", "persisted user-visible steps cannot retain an executable private delivery marker")
			}
			critic := model.calls[len(model.calls)-1]
			var review map[string]any
			require.NoError(t, json.Unmarshal([]byte(critic[1].Content), &review))
			require.NotContains(t, review["candidate_answer"], "document_table_view")
			require.Contains(t, review["candidate_answer"], "12.500")
			require.Contains(t, review["candidate_answer"], "Second")
		})
	}
}

func TestDocumentTableViewUnknownChangedFailedOrUnboundSnapshotCannotBecomeFallback(t *testing.T) {
	for name, mutate := range map[string]func(*AgentEngine, *types.AgentState, *string){
		"unknown_ref": func(_ *AgentEngine, _ *types.AgentState, ref *string) { *ref = "unknown" },
		"changed_snapshot": func(_ *AgentEngine, state *types.AgentState, _ *string) {
			result := state.RoundSteps[1].ToolCalls[0].Result
			snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
			snapshot.Markdown += "Tampered quantity: 999."
			result.Data["view_snapshot"] = snapshot
		},
		"changed_original": func(_ *AgentEngine, state *types.AgentState, _ *string) {
			state.RoundSteps[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)["content_revision"] = 2
		},
		"failed_execution": func(_ *AgentEngine, state *types.AgentState, _ *string) {
			state.RoundSteps[1].ToolCalls[0].Result.Success = false
		},
		"missing_private_store": func(e *AgentEngine, state *types.AgentState, _ *string) {
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(newDocumentTableCalculationTool(func() []types.AgentStep { return state.RoundSteps }, e.toolRegistry))
		},
		"scope_not_current": func(e *AgentEngine, _ *types.AgentState, _ *string) {
			e.toolRegistry.RegisterTool(newCountingTool(agenttools.ToolBindDocumentScope))
		},
	} {
		t.Run(name, func(t *testing.T) {
			model := &mockChat{}
			e, state, ref, _, _ := documentTableFinalDeliveryFixture(t, model, []int{1, 2})
			mutate(e, state, &ref)
			decision := e.checkCandidate(t.Context(), "Report the selected source observations.", `<document_table_view ref="`+ref+`" />`, "", state, nil, false)
			require.Error(t, decision.Err)
			require.Equal(t, candidateProtocolFailure, decision.Disposition)
			require.Empty(t, decision.Candidate)
			require.False(t, decision.SourceReviewAttempted)
			require.Empty(t, e.turnReview.UnverifiedCandidate)
			require.Empty(t, model.calls, "invalid view identity never reaches semantic review as source evidence")
		})
	}
}

func TestDocumentTableViewStillRequiresWholeTaskCoverageAndFreshSteerReview(t *testing.T) {
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict(), sourceConsistencyVerdict(`{"complete":false,"feedback":"The newly requested complete source scope contains another observed row not included in this view.","next_action":"revise_answer"}`)}}
	e, state, ref, markdown, _ := documentTableFinalDeliveryFixture(t, model, []int{1})
	candidate := `<document_table_view ref="` + ref + `" />`
	first := e.checkCandidate(t.Context(), "Report the first selected observation.", candidate, "", state, nil, false)
	require.NoError(t, first.Err)
	require.Equal(t, candidateAccepted, first.Disposition)
	e.reviewConversationReady = true
	e.reviewConversation = []chat.Message{{Role: "user", Content: "Report the first selected observation."}}
	e.SetSteerSink(&fakeSteerSink{queued: []map[string]any{steerEntry("expand-table-scope", "Include every actual source observation.")}})
	messages := append([]chat.Message(nil), e.reviewConversation...)
	require.Equal(t, 1, e.drainSteerMessages(t.Context(), state, &messages, "session", "message"))
	require.False(t, e.candidateApprovalCurrent(markdown, state), "steering invalidates the old approval, not the source cells")
	second := e.checkCandidate(t.Context(), "Report the first selected observation.", candidate, "", state, messages, false)
	require.NoError(t, second.Err)
	require.Equal(t, markdown, second.Candidate, "still-current source evidence may be re-used after steering")
	require.Equal(t, candidateRevise, second.Disposition)
	require.NotEqual(t, first.TaskHash, second.TaskHash)
	require.Len(t, model.calls, 2, "a view does not reuse the previous task's semantic approval")
	require.Contains(t, fmt.Sprint(model.calls[1]), "Include every actual source observation.")
}

func TestDocumentTableTraceRedactionPreservesLiteralExamples(t *testing.T) {
	directive := documentTableDeliveryFixtureDirective()
	for _, literal := range []string{"```xml\n" + directive + "\n```", "`" + directive + "`", "> " + directive, "<!--\n" + directive + "\n-->", "<pre>\n" + directive + "\n</pre>"} {
		require.Equal(t, literal, redactDocumentTableTrace(literal))
	}
	redacted := redactDocumentTableTrace("A source-based draft.\n" + directive + "\nUnfinished scope.")
	require.NotContains(t, redacted, "document_table_view")
	require.NotContains(t, redacted, documentTableDeliveryFixtureRef)
	require.Contains(t, redacted, "尚未展开")
	require.Contains(t, redacted, "Unfinished scope.")
}

func TestDocumentTableCancellationPersistsUndeliveredDiagnosticAndPreservesCodeExamples(t *testing.T) {
	for _, literalExample := range []bool{false, true} {
		t.Run(fmt.Sprintf("literal_%v", literalExample), func(t *testing.T) {
			candidate := documentTableDeliveryFixtureDirective()
			if literalExample {
				candidate = "```xml\n" + candidate + "\n```"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			model := &cancelAtReviewCallChat{mockChat: mockChat{responses: []mockResponse{chatAnswer(candidate)}}, cancel: cancel, cancelAt: 1}
			e := newTestEngine(t, model)
			e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
			var completed event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				completed = evt.Data.(event.AgentCompleteData)
				return nil
			})
			state, err := e.executeLoop(ctx, &types.AgentState{}, "Report the requested result.", emptyMessages(), emptyTools(), "session", "message")
			require.NoError(t, err)
			require.Empty(t, state.FinalAnswer)
			require.False(t, state.IsComplete)
			steps, ok := completed.AgentSteps.([]types.AgentStep)
			require.True(t, ok)
			require.Len(t, steps, 1)
			if literalExample {
				require.Equal(t, candidate, steps[0].Thought, "a literal example is never an executable runtime directive")
			} else {
				require.NotContains(t, steps[0].Thought, "document_table_view")
				require.Contains(t, steps[0].Thought, "未交付")
			}
			require.Len(t, model.calls, 1, "cancellation redacts protocol traces without starting another model call")
		})
	}
}
