package agent

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/jsonschema-go/jsonschema"
)

// Coverage is a model interpretation of the human task, never an execution
// status or a source grant. RequestBasis is checked against human messages;
// actual source identities are supplied only by successful tool observations.
type taskCoverageTarget struct {
	GoalIndex    int    `json:"goal_index"`
	RequestBasis string `json:"request_basis"`
	Criteria     string `json:"criteria,omitempty"`
	SourceScope  string `json:"source_scope,omitempty"`
	*StructuredTaskGoal
}

// StructuredTaskGoal is embedded so the decoder can distinguish old targets
// from targets carrying any of the newer fields.
type StructuredTaskGoal struct {
	SourceMode       string         `json:"source_mode,omitempty"`
	BasisTurn        *int           `json:"basis_turn,omitempty"`
	RestrictionBasis string         `json:"restriction_basis,omitempty"`
	PrimaryRef       string         `json:"primary_ref,omitempty"`
	Metric           string         `json:"metric,omitempty"`
	Period           map[string]any `json:"period,omitempty"`
	SourceTime       string         `json:"source_time,omitempty"`
	VersionTask      string         `json:"version_task,omitempty"`
	Required         []string       `json:"required,omitempty"`
	OnVerifiedGap    []string       `json:"on_verified_gap,omitempty"`
	Delivery         map[string]any `json:"delivery,omitempty"`
}

const structuredTaskCoverageSchema = `{
 "type":"object","additionalProperties":false,
 "if":{"properties":{"source_mode":{"const":"only"}}},
 "then":{"required":["restriction_basis"],"properties":{"restriction_basis":{"type":"string"},"on_verified_gap":{"not":{"contains":{"const":"attributed_supplement"}}}}},
 "else":{"properties":{"restriction_basis":{"type":"null"}}},
 "required":["goal_index","request_basis","source_mode"],
 "properties":{
  "goal_index":{"type":"integer","minimum":0,"maximum":7},
  "request_basis":{"type":"string","minLength":1,"maxLength":512,"pattern":"\\S"},
  "criteria":{"type":"string","minLength":1,"maxLength":512,"pattern":"\\S"},
  "source_scope":{"type":"string","minLength":1,"maxLength":512,"pattern":"\\S"},
  "basis_turn":{"type":"integer","minimum":0,"maximum":8},
  "restriction_basis":{"type":["string","null"],"minLength":1,"maxLength":512,"pattern":"\\S"},
  "primary_ref":{"type":"string","minLength":1,"maxLength":512,"pattern":"\\S"},
  "source_mode":{"type":"string","enum":["only","named_first","open"]},
  "metric":{"type":"string","minLength":1,"maxLength":512},
  "period":{"type":"object","additionalProperties":false,"required":["grain"],"if":{"properties":{"grain":{"const":"none"}}},"then":{"not":{"anyOf":[{"required":["from"]},{"required":["to"]},{"required":["count"]}]}},"properties":{"scope":{"type":"string","minLength":1,"maxLength":256},"from":{"type":"string","minLength":1,"maxLength":128},"to":{"type":"string","minLength":1,"maxLength":128},"count":{"type":"integer","minimum":1},"grain":{"type":"string","enum":["none","day","week","month","quarter","year","report_period","report_date","crop_year","episode","event_date","event_period","chart_endpoint","chart_trend","forecast_scenario"]}}},
  "source_time":{"type":"string","enum":["observed_period","latest_observation","disclosed_at","forecast_period","general_concept","latest_relevant_assessment","cover_date","catalog_snapshot","publication_date"]},
  "version_task":{"type":"string","enum":["single","compare_versions","compare_sources","adopt_if_conflict","consistent_series","separate_conflicts","any_attributed_source"]},
  "required":{"type":"array","maxItems":4,"uniqueItems":true,"items":{"enum":["scoped_value","explicit","supporting","optional"]}},
  "on_verified_gap":{"type":"array","maxItems":4,"uniqueItems":true,"items":{"enum":["report_gap","last_valid_same_field","attributed_supplement","clarify"]}},
  "delivery":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"type":"string","enum":["text","chart","line_chart","bar_chart","file","image"]},"aggregation":{"type":"string","enum":["as_reported","mean","sum","count","min_max","difference","difference_and_return","last_valid_trading_day"]}}}
 }
}`

var structuredTaskCoverageValidator = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(structuredTaskCoverageSchema), &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
})

func validateTaskCoverageTargets(plan *taskEvidencePlan, raw []map[string]any, structured bool) error {
	if len(plan.CoverageTargets) > 8 {
		return fmt.Errorf("coverage targets exceed protocol budget")
	}
	for index, target := range plan.CoverageTargets {
		if target.GoalIndex < 0 || target.GoalIndex >= len(plan.Goals) {
			return fmt.Errorf("coverage target requires an existing goal index")
		}
		if target.StructuredTaskGoal == nil {
			for _, text := range []string{target.RequestBasis, target.Criteria, target.SourceScope} {
				if strings.TrimSpace(text) == "" || len(text) > 2048 || utf8.RuneCountInString(text) > 512 {
					return fmt.Errorf("coverage target requires bounded request basis, criteria and source scope")
				}
			}
			continue
		}
		if !structured {
			return fmt.Errorf("unsupported structured coverage target")
		}
		schema, err := structuredTaskCoverageValidator()
		if err != nil {
			return err
		}
		if err := schema.Validate(raw[index]); err != nil {
			return err
		}
		// Keep the validated values when encoding/json would merge repeated object keys.
		target.Period, _ = raw[index]["period"].(map[string]any)
		target.Delivery, _ = raw[index]["delivery"].(map[string]any)
		target.RestrictionBasis, _ = raw[index]["restriction_basis"].(string)
	}
	return nil
}

// Literal quotation validates provenance only. The model interprets intent.
func validateTaskCoverageBasis(plan *taskEvidencePlan, query string, messages []chat.Message, turns []string, pinned func(string) bool) error {
	quoted := func(quote string, texts []string) bool {
		return strings.TrimSpace(quote) != "" && slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(text, quote) })
	}
	legacyTurns := documentScopeUserTexts(query, messages)
	for _, target := range plan.CoverageTargets {
		basis := turns
		switch {
		case target.StructuredTaskGoal == nil:
			basis = legacyTurns
		case target.BasisTurn != nil:
			index := *target.BasisTurn
			if index < 0 || index >= len(turns) {
				return fmt.Errorf("coverage basis_turn does not identify a visible human message")
			}
			basis = turns[index : index+1]
		}
		if !quoted(target.RequestBasis, basis) {
			return fmt.Errorf("coverage request basis must quote an actual human request")
		}
		if target.StructuredTaskGoal == nil {
			continue
		}
		for _, ref := range []string{target.PrimaryRef, target.RestrictionBasis} {
			if ref != "" && !pinned(ref) && (taskCoverageSourceHandle(ref) || !quoted(ref, turns)) {
				return fmt.Errorf("coverage source references need a user quotation or verified source selection")
			}
		}
		if target.RestrictionBasis != target.PrimaryRef && pinned(target.RestrictionBasis) && pinned(target.PrimaryRef) {
			return fmt.Errorf("coverage pinned restriction and primary_ref identify different sources")
		}
	}
	return nil
}

func taskCoverageSourceHandle(text string) bool {
	scheme, _, handle := strings.Cut(strings.ToLower(strings.TrimSpace(text)), "://")
	return handle && slices.Contains([]string{"doc", "kb", "chunk", "evidence", "url", "weknora"}, scheme)
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
