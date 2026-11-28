package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestAf2EndpointUsesDateFromTheCitedValueRow(t *testing.T) {
	endpoint := coverageEvidence{Kind: "last_valid", Operation: "inspect_source", SourceTitle: "Register.xlsx",
		Sheet: "Operations", Metric: "Volume", SeriesID: "volume", Date: "2021-06-03", DateCell: "A15", Cell: "B15",
		Raw: json.Number("47.25"), CitationID: "reading"}
	const citation = `<evidence evidence_id="reading"/>`
	for _, tc := range []struct {
		name, draft string
		want        bool
	}{
		{"inline", "Register.xlsx Volume on 2021-06-03 was 47.25 at B15. " + citation, true},
		{"table scope", "Last observation 2021-06-03:\n\n| Volume | 47.25 | B15 " + citation + " |", true},
		{"list scope", "Last observation 2021-06-03:\n- Volume 47.25 at B15. " + citation, true},
		{"wrong date in table", "Last observation 2021-06-03:\n\n| 2021-06-02 | Volume | 47.25 | B15 " + citation + " |", false},
		{"wrong date cell", "Register.xlsx Volume on 2021-06-03 was 47.25 at B16. " + citation, false},
		{"wrong value", "Register.xlsx Volume on 2021-06-03 was 48.25 at B15. " + citation, false},
		{"date in unrelated paragraph", "Other.xlsx was read on 2021-06-03.\n\nVolume 47.25 at B15. " + citation, false},
		{"date without observation", "Register.xlsx / Operations 2021-06-03. " + citation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paragraphs []coverageDraftParagraph
			for _, text := range strings.Split(tc.draft, "\n\n") {
				paragraphs = append(paragraphs, newCoverageDraftParagraph(text))
			}
			require.Equal(t, tc.want, coverageDraftDisplaysEndpoint(paragraphs, endpoint))
		})
	}
}

func TestAf2CatalogOnlyDoesNotStandInForRequestedReadings(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
	for _, mode := range []string{"true", "false"} {
		for _, test := range []struct {
			basis, publication, relaxed, finalRead string
		}{
			{"unplanned", "false", "true", "false"},
			{"unplanned", "true", "true", "false"},
			{"unplanned", "true", "false", "false"},
			{"unplanned", "true", "true", "true"},
			{"catalog_snapshot", "true", "true", "true"},
			{"general_concept", "true", "true", "true"},
		} {
			t.Run(strings.Join([]string{mode, test.basis, test.publication, test.relaxed, test.finalRead}, "/"), func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", mode)
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", test.publication)
				t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", test.relaxed)
				t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", test.finalRead)
				e := newTestEngine(t, &qa4Stage2CModel{t: t})
				e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
				e.toolRegistry = qa4ReplayRegistry()
				basis := test.basis
				query := "What value is shown for the selected interval?"
				draft := "The Equipment.xlsx catalog lists Operating hours. The reading has yet to be obtained."
				if basis != "unplanned" {
					query, draft = "Which worksheets does the catalog list for Equipment.xlsx?", "The Equipment.xlsx catalog lists Operating hours."
					if basis == "general_concept" {
						query, draft = "What does elapsed time mean?", "Elapsed time is the duration between two points in time."
					}
					e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: query,
						StructuredTaskGoal: &StructuredTaskGoal{SourceTime: basis, Required: []string{"optional"}}}}}
				}
				call := types.ToolCall{ID: "directory", Name: "structured_list_datasets", Args: map[string]any{"knowledge_base_id": "allowed-library"}, Result: &types.ToolResult{Success: true, Data: map[string]any{
					"_structured_execution": map[string]any{"canonical_operation": "list_datasets", "effect_class": "read", "scope_mode": "directory"},
					"source_context":        map[string]any{"knowledge_base_id": "allowed-library"},
					"datasets":              []any{map[string]any{"dataset_id": "register", "source_file": "Equipment.xlsx", "sheets": []any{"Operating hours"}}},
				}}}
				state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
				ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
				decision := e.checkCandidate(ctx, query, draft, "", state, nil, false)
				var complete event.AgentCompleteData
				e.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
					complete = item.Data.(event.AgentCompleteData)
					return nil
				})
				if basis != "unplanned" {
					require.Equal(t, candidateAccepted, decision.Disposition)
					e.commitTerminalResult(ctx, state, "run", draft, "", "answer", false)
					e.emitCompletionEvent(ctx, state, "run", "message", time.Now())
					require.Equal(t, draft, complete.FinalAnswer, "non-numerical requests can be fulfilled by catalog metadata or a concept explanation")
					require.Nil(t, complete.Incomplete)
					return
				}
				require.Equal(t, candidateRevise, decision.Disposition)
				state.IsComplete, state.FinalAnswer = true, draft // Restored candidate bypasses the normal commit.
				e.emitCompletionEvent(ctx, state, "run", "message", time.Now())
				require.NotNil(t, complete.Incomplete)
				switch {
				case test.finalRead == "true":
					require.NotEmpty(t, complete.FinalAnswer)
					require.Contains(t, complete.FinalAnswer, query)
					require.Contains(t, complete.FinalAnswer, "Source listing completed")
					require.Contains(t, complete.FinalAnswer, "supplied no reading for the requested scope")
					require.NotContains(t, complete.FinalAnswer, "Operating hours")
					require.NotContains(t, complete.FinalAnswer, "<evidence")
					require.NotContains(t, complete.FinalAnswer, "<kb")
					require.False(t, terminalCandidateFallback(complete.FinalAnswer))
					require.True(t, complete.HasPartialAnswer(), "the incomplete event exposes the status text")
				case test.publication == "true" && test.relaxed == "false":
					require.True(t, complete.HasPartialAnswer())
					require.Contains(t, complete.FinalAnswer, "Equipment.xlsx")
					require.NotEqual(t, draft, complete.FinalAnswer)
				default:
					require.Empty(t, complete.FinalAnswer)
					require.False(t, complete.HasPartialAnswer(), "directory navigation is not a reading")
				}
			})
		}
	}
}

func TestAf2WholeResultCitationDoesNotSelectEveryEndpoint(t *testing.T) {
	selected := coverageEvidence{Kind: "last_valid", SourceDomain: "structured", Operation: "analyze_table",
		SourceTitle: "Register.xlsx", Sheet: "Operations", SeriesID: "volume", Column: "B", Metric: "Volume", Date: "2021-06-03",
		DateCell: "A15", Cell: "B15", Raw: json.Number("47.25"), CitationID: "reading", LastValidStatus: "observed", LastValidDirection: "before_range"}
	other := selected
	other.SeriesID, other.Column, other.Metric, other.Cell, other.Raw = "count", "C", "Count", "C15", json.Number("12")
	paragraph := newCoverageDraftParagraph(`Register.xlsx Volume on 2021-06-03 was 47.25 at B15. <evidence evidence_id="reading"/>`)
	paragraphs := []coverageDraftParagraph{paragraph}
	review := coverageDraftReview{record: coverageRecord{Evidence: []coverageEvidence{other, selected}}}
	require.True(t, coverageDraftSelectsEndpoint(paragraphs, review, selected))
	require.False(t, coverageDraftSelectsEndpoint(paragraphs, review, other))
}
