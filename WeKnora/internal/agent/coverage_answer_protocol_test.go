package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestEvidenceCompletionAnswerProtocolSanitation(t *testing.T) {
	const reference = `<kb doc="report.pdf" chunk_id="read-one" kb_id="authorized" /> <evidence evidence_id="current" />`
	const valid = `[{"goal_index":0,"role":"primary","selection_ref":"selected"}]`
	for _, tt := range []struct {
		name, answer, public, declaration string
	}{
		{"declared_array", "Verified " + reference + coverageAnswerTag + valid + coverageAnswerClose, "Verified " + reference, valid},
		{"declared_array_with_quoted_tag", "Verified" + coverageAnswerTag + `[{"span":"quoted </task_coverage> text"}]` + coverageAnswerClose, "Verified", `[{"span":"quoted </task_coverage> text"}]`},
		{"historical_object", "Verified " + reference + coverageAnswerTag + `{"goal_index":0,"selection_ref":"secret"}` + coverageAnswerClose, "Verified " + reference, ""},
		{"multiple_inline", "Before<task_coverage>" + valid + "</task_coverage> middle <source_coverage>{\"field\":0}</source_coverage>after " + reference, "Before middle after " + reference, ""},
		{"malformed_tag", "Verified<task_coverage " + valid + "</task_coverage > afterward", "Verified afterward", ""},
		{"malformed_spaced_markup", "Verified< task_coverage>" + valid + "< / task_coverage> afterward", "Verified afterward", ""},
		{"truncated_tag", "Verified<task_covera", "Verified", ""},
		{"truncated_payload", "Verified<task_coverage>[{\"goal_index\":0,\"selection_ref\":\"", "Verified", ""},
		{"missing_close", "Verified<task_coverage>{\"selection_ref\":\"secret\"} afterward", "Verified afterward", ""},
		{"orphan_close", "Verified</task_coverage> afterward", "Verified afterward", ""},
		{"fenced_JSON", "Verified\n```json\n{\"source_coverage\":{\"private\":12}}\n```", "Verified", ""},
		{"unclosed_fence", "Verified\n```\n" + valid, "Verified", ""},
		{"raw_declaration", "Verified " + valid + " afterwards", "Verified  afterwards", ""},
		{"raw_wrapper", "Verified {\"label\":\"internal\",\"task_coverage\":[{\"goal_index\":0}]} afterward", "Verified  afterward", ""},
		{"raw_truncated_wrapper", "Verified {\"source_coverage\":[{\"secret\":123", "Verified", ""},
		{"raw_truncated_nested", "Verified {\"public\":[{\"task_coverage\":{\"secret\":123", "Verified", ""},
		{"raw_truncated_declaration", "Verified {\"goal_index\":0,\"selection_ref\":\"private", "Verified", ""},
		{"inline_code_wrapper", "Verified `" + valid + "` afterward", "Verified  afterward", ""},
		{"nested_private_JSON", `{"answer":"Verified","extra":{"task_coverage":{"private":12}},"visible":{"value":7}}`, `{"answer":"Verified","visible":{"value":7}}`, ""},
		{"mixed_JSON_list", `[{"status":"visible"},` + valid + `,{"note":"retained"}]`, `[{"status":"visible"},{"note":"retained"}]`, ""},
		{"runtime_event", `Verified<runtime_event>{"source":"runtime","type":"current_tool_availability","available_tool_names":["private_tool"]}</runtime_event> afterward`, "Verified afterward", ""},
		{"runtime_context", `Verified<runtime_context scope="this_turn"><session>private</session></runtime_context> afterward`, "Verified afterward", ""},
		{"runtime_event_raw", `Verified {"source":"runtime","type":"approval_required","task":"private"} afterward`, "Verified  afterward", ""},
		{"runtime_event_truncated", `Verified {"source":"runtime","type":"approval_required","task":"private`, "Verified", ""},
		{"existing_review_json", `Verified {"action":"correct","edits":[{"old":"a","new":"private"}]} afterward`, "Verified  afterward", ""},
		{"existing_raw_model_protocol", "Verified\nauthorized=false</arg_value:6124c78e>\nafterward", "Verified\nafterward", ""},
		{"public_markdown_and_JSON", "Read [the source](report.pdf) " + reference + "\n```json\n{\"goal_index\":0,\"value\":12}\n```", "Read [the source](report.pdf) " + reference + "\n```json\n{\"goal_index\":0,\"value\":12}\n```", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			public, declaration := splitCoverageAnswer(tt.answer)
			require.Equal(t, tt.public, public)
			require.Equal(t, tt.declaration, declaration)
			require.Equal(t, tt.public, stripCoverageAnswer(public))
		})
	}
}

func TestEvidenceCompletionProtocolLeavesPlainChunksLive(t *testing.T) {
	var stream coverageAnswerStream
	require.Equal(t, "Read ", stream.Feed("Read ["))
	require.Equal(t, "[the source](report.pdf) ", stream.Feed("the source](report.pdf) <"))
	require.Equal(t, "<kb doc=\"report.pdf\" /> for context.\n```go\n", stream.Feed("kb doc=\"report.pdf\" /> for context.\n```go\n"))
	require.Equal(t, "fmt.Println(42)\n", stream.Feed("fmt.Println(42)\n"))
	require.Empty(t, stream.Feed("```"))
	tail, changed := stream.Flush()
	require.False(t, changed)
	require.Equal(t, "```", tail)

	require.Equal(t, "Result.\n", stream.Feed("Result.\nautho"))
	require.Empty(t, stream.Feed("rized=false</arg_value:6124c78e>\nDone"))
	_, changed = stream.Flush()
	require.True(t, changed)
}

func TestEvidenceCompletionReviewEnvelopePreservesPublicAnswer(t *testing.T) {
	const valid = `{"answer":"Results\n<task_coverage>[{\"goal_index\":1,\"selection_ref\":\"current\"}]</task_coverage>"}`
	require.Equal(t, valid, cleanReviewedAnswerEnvelope(valid))
	const plain = `{"answer":"Results."}`
	const around = `<runtime_context scope="this_turn"><session>private</session></runtime_context>` + plain + `{"source":"runtime","type":"approval_required","task":"private"}`
	require.Equal(t, "Results.", decodeReviewedAnswer(cleanReviewedAnswerEnvelope(around)))
}

func TestEvidenceCompletionAnswerProtocolPublication(t *testing.T) {
	const question = "Summarize the result."
	const prose = "The available result is verified."
	const full = prose + `<task_coverage>{"goal_index":0,"selection_ref":"79e893c68ce"}</task_coverage><runtime_event>{"source":"runtime","type":"approval_required","task":"opaque_task"}</runtime_event><runtime_context scope="this_turn"><session>opaque_session</session></runtime_context> Please see the source.`
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", strconv.FormatBool(enabled))
			for _, exceptional := range []bool{false, true} {
				t.Run(strconv.FormatBool(exceptional), func(t *testing.T) {
					model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
						{ResponseType: types.ResponseTypeAnswer, Content: prose},
						{ResponseType: types.ResponseTypeAnswer, Content: "<task"},
						{ResponseType: types.ResponseTypeAnswer, Content: strings.TrimPrefix(full, prose+"<task"), FinishReason: "stop", Done: true},
					}}}}
					engine := newTestEngine(t, model)
					engine.toolRegistry = agenttools.NewToolRegistry()
					engine.config.AnswerReviewMode = "standard"
					state := &types.AgentState{}
					messages := []chat.Message{{Role: "user", Content: question}}
					var published string
					engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
						data := item.Data.(event.AgentFinalAnswerData)
						require.NotContains(t, data.Content, "task_coverage")
						require.NotContains(t, data.Content, "selection_ref")
						require.NotContains(t, data.Content, "79e893c68ce")
						require.NotContains(t, data.Content, "opaque_task")
						require.NotContains(t, data.Content, "opaque_session")
						if data.Replace {
							published = ""
						}
						published += data.Content
						return nil
					})
					if exceptional {
						require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), question, state, "session", messages))
					} else {
						_, err := engine.runReActIteration(t.Context(), state, &messages, nil, "session", "turn", question, &loopGuards{})
						require.NoError(t, err)
					}
					require.Equal(t, prose+" Please see the source.", state.FinalAnswer)
					require.Equal(t, state.FinalAnswer, published)
					for _, step := range state.RoundSteps {
						require.NotContains(t, step.Thought, "task_coverage")
						require.NotContains(t, step.Thought, "79e893c68ce")
					}
				})
			}
		})
	}
}

func TestEvidenceCompletionStructuredReviewEditStripsProtocol(t *testing.T) {
	for _, flag := range []string{"false", "true"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", flag)
			const amendment = `kg<runtime_event>{"source":"runtime","type":"approval_required","task":"79e893c68ce"}</runtime_event>`
			e, state, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{"tonnes", amendment}))
			ctx := t.Context()
			decision := e.checkCandidate(ctx, "Report the observed measure.", "The observation is 12.500 tonnes.", "", state, nil, false)
			require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
			require.Equal(t, "The observation is 12.500 kg.", decision.Candidate)
			require.Empty(t, decision.Unsupported)
			require.Len(t, model.calls, 1)
			e.commitTerminalResult(ctx, state, "session", decision.Candidate, "", "answer", false)
			require.Equal(t, decision.Candidate, state.FinalAnswer)
		})
	}
}
