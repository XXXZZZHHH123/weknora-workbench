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
	for _, mode := range []string{"true", "false"} {
		for _, basis := range []string{"unplanned", "catalog_snapshot", "general_concept"} {
			t.Run(mode+"/"+basis, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", mode)
				e := newTestEngine(t, &qa4Stage2CModel{t: t})
				e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
				e.toolRegistry = qa4ReplayRegistry()
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
				decision := e.checkCandidate(t.Context(), query, draft, "", state, nil, false)
				if basis != "unplanned" {
					require.Equal(t, candidateAccepted, decision.Disposition)
					return
				}
				require.Equal(t, candidateRevise, decision.Disposition)
				var complete event.AgentCompleteData
				e.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
					complete = item.Data.(event.AgentCompleteData)
					return nil
				})
				state.IsComplete, state.FinalAnswer = true, draft // Restored candidate bypasses the normal commit.
				e.emitCompletionEvent(t.Context(), state, "run", "message", time.Now())
				require.NotNil(t, complete.Incomplete)
				if types.VerifiedPublicationEnabled() && !relaxedTerminalEvidenceEnabled() {
					require.True(t, complete.HasPartialAnswer())
					require.Contains(t, complete.FinalAnswer, "Equipment.xlsx")
					require.NotEqual(t, draft, complete.FinalAnswer)
				} else {
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
