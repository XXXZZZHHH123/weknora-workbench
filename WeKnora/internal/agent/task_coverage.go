package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Coverage is a model interpretation of the human task, never an execution
// status or a source grant. RequestBasis is checked against human messages;
// actual source identities are supplied only by successful tool observations.
type taskCoverageTarget struct {
	GoalIndex    int    `json:"goal_index"`
	RequestBasis string `json:"request_basis"`
	Criteria     string `json:"criteria"`
	SourceScope  string `json:"source_scope"`
}

func validateTaskCoverageTargets(plan *taskEvidencePlan) error {
	if len(plan.CoverageTargets) > 8 {
		return fmt.Errorf("coverage targets exceed protocol budget")
	}
	for _, target := range plan.CoverageTargets {
		if target.GoalIndex < 0 || target.GoalIndex >= len(plan.Goals) {
			return fmt.Errorf("coverage target requires an existing goal index")
		}
		for _, text := range []string{target.RequestBasis, target.Criteria, target.SourceScope} {
			if strings.TrimSpace(text) == "" || len(text) > 2048 || utf8.RuneCountInString(text) > 512 {
				return fmt.Errorf("coverage target requires bounded request basis, criteria and source scope")
			}
		}
	}
	return nil
}

// Literal quotation validates provenance only. The model still interprets the
// complete request; a quote match cannot decide intent or task completion.
func validateTaskCoverageBasis(plan *taskEvidencePlan, query string, messages []chat.Message) error {
	for _, target := range plan.CoverageTargets {
		found := strings.Contains(query, target.RequestBasis)
		for _, message := range messages {
			if message.Role == "user" && message.Kind != chat.MessageKindReviewEvidence && strings.Contains(message.Content, target.RequestBasis) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("coverage request basis must quote an actual human request")
		}
	}
	return nil
}

const taskCoverageReviewBudget = 6 * 1024

// This ledger records executions, not model-declared completion. Its handles
// are derived from exactly the canonical result bytes used by every existing
// source adapter and immutable reader. Do not copy source bodies or infer
// physical page coverage from an invocation's chunk pagination.
func (e *AgentEngine) taskCoverageReview(state *types.AgentState) map[string]any {
	if !e.needsSourceConsistencyCheck(state) {
		return nil
	}
	observations := []any{}
	view := map[string]any{
		"observations": observations, "successful_calls": 0, "failed_calls": 0,
		"omitted_calls": 0, "projection_complete": true,
		"notice": "Actual execution ledger only. A successful call, returned count, exhausted retrieval page or planner target does not prove the requested semantic coverage. Inspect the existing source companions and immutable evidence handles for real source identity, version, returned range, content and gaps. Ledger counts are execution metadata, never answer values. Generic page/chunk_page fields are retrieval pagination; physical pages require returned row.page with its offset_space. Failed calls are execution limitations, never source facts. Omitted observations remain unknown.",
	}
	seen := map[string]bool{}
	successful, failed, omitted := 0, 0, 0
	for _, message := range e.sourceConsistencyMessages(state, nil) {
		payload, err := decodeCitationJSON(message.Content)
		if err != nil {
			continue
		}
		// Runtime access summaries are separate review entries, not executions.
		if _, canonical := payload["data"]; !canonical {
			continue
		}
		id := reviewEvidenceID(message)
		if seen[id] {
			continue
		}
		seen[id] = true
		success, valid := payload["success"].(bool)
		if !valid {
			continue
		}
		if success {
			successful++
		} else {
			failed++
		}
		observation := map[string]any{"evidence_id": id, "tool": message.Name,
			"tool_call_id": message.ToolCallID, "success": success, "data_json_pointer": "/data"}
		if _, exists := payload["execution_scope"]; exists {
			observation["execution_scope_json_pointer"] = "/execution_scope"
		}
		// Reuse the omission-aware source adapters for content and exact ranges.
		// Keeping only locators here avoids a second competing source projection.
		observations = append(observations, observation)
		view["observations"] = observations
		if sourceReviewProjectionSize(view) > taskCoverageReviewBudget-128 {
			observations = observations[:len(observations)-1]
			view["observations"] = observations
			omitted++
		}
	}
	view["successful_calls"], view["failed_calls"] = successful, failed
	view["omitted_calls"], view["projection_complete"] = omitted, omitted == 0
	return view
}

const taskCoverageReviewInstructions = `Coverage contract: evaluate the complete current human request, its requested granularity and source restrictions under the SAME acceptance criteria for a natural stop, retry or exhausted execution budget. reasoning_depth only selects reasoning effort. It never waives source meaning or coverage checks. When present, advisory_task_plan.coverage_targets are advisory model interpretations anchored to actual human quotations, not facts or extra requirements. Compare them with the full human request and actual successful evidence in the existing source companions; correct an over-broad or stale plan rather than enforcing it. The task_coverage execution ledger contains runtime-derived immutable handles only, not a model's self-declared completed targets. A source citation or numerically matched token alone cannot establish complete extraction. For requested full coverage, compare all actual available source observations with the candidate, including uncited observations, unreturned continuation and explicit gaps. For requested summaries or narrow scope, require only the requested granularity; do not expand to every row. A temporary model read range cannot narrow a human request. Generic retrieval pagination is not physical page coverage: physical pages require actual row.page and its offset_space. For batched page reads, inspect requested_pages, each page_results success/continuation, pages_complete and all_pages_succeeded; top-level tool success can mean only one page succeeded and cannot establish full requested coverage. Accept an accurately limited partial or blocked response only when its limitations match actual evidence and remaining authorized capabilities; never treat an incomplete result disguised as complete as success. Record the coverage conclusion and material remaining gaps in the existing overall assessment/critical audit. Use the existing collect_evidence versus revise_answer recovery contract, and preserve all numeric, source, permission and citation checks. Do not infer completion from tool success, a plan, final-answer wording or iteration count.`
