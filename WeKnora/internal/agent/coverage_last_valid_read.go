package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

const coverageLastValidReadTool = "structured_query_series"

// nextCoverageLastValidRead fills one already bound field and window through
// the existing native read loop. An empty explicit source_binding makes the
// service return unresolved duplicate groups without calling its model.
func (e *AgentEngine) nextCoverageLastValidRead(ctx context.Context, state *types.AgentState) (types.LLMToolCall, bool) {
	if ctx.Err() != nil || state == nil || !structuredresult.LastValidEnabled() ||
		!e.evidenceCompletionEligible() || !e.standardTextDelivery(state) {
		return types.LLMToolCall{}, false
	}
	if !e.coverageLastValidReadAvailable() {
		return types.LLMToolCall{}, false
	}
	current := e.currentCoverageScopeRecord(ctx)
	if current.Revision == "" || current.AuthScope == "" || len(current.Bindings) == 0 {
		return types.LLMToolCall{}, false
	}
	current.Evidence = nil
	current, attempts := e.projectCurrentTaskCoverage(state, e.evidenceCompletion.startStep, current)
	for _, target := range e.evidenceCompletionTargets() {
		from, to, _ := coverageTargetDateRange(target)
		if !coverageLastValidTarget(target) || !coverageAnalysisDay(from) || !coverageAnalysisDay(to) || from > to {
			continue
		}
		field, ok := e.selectedCoverageEndpointField(ctx, current, target, from, to)
		if !ok || coverageEndpointAlreadyRead(current, field) {
			continue
		}
		key := reviewDigest([]string{current.Revision, current.AuthScope, field.KnowledgeBaseID,
			field.DatasetID, field.SeriesID, firstNonEmptyAgent(field.WorkbookSnapshotHash, field.SnapshotHash), from, to})
		id := "coverage-endpoint-" + key[:24]
		binding := e.coverageEndpointSourceBinding(current, field, attempts)
		if slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
			return attempt.ToolCallID == id || coverageEndpointAttemptMatches(attempt, field) && attempt.Args["include_last_valid"] == false ||
				len(binding) == 0 && coverageEndpointSelectionUnresolved(attempt, field.KnowledgeBaseID, field.DatasetID, field.SeriesID)
		}) {
			continue
		}
		if call, ok := e.coverageLastValidReadCall(ctx, id, &target.GoalIndex, field, binding); ok {
			return call, true
		}
	}
	return types.LLMToolCall{}, false
}

func (e *AgentEngine) coverageLastValidReadAvailable() bool {
	descriptor, _, native := e.toolRegistry.StructuredInvocation(coverageLastValidReadTool, json.RawMessage(`{}`))
	if !native || descriptor.Operation != "query_series" || descriptor.EffectClass != "read" || !descriptor.SupportsLastValid() {
		return false
	}
	tool, err := e.toolRegistry.GetTool(coverageLastValidReadTool)
	if err != nil {
		return false
	}
	var schema struct{ Properties map[string]json.RawMessage }
	if json.Unmarshal(tool.Parameters(), &schema) != nil {
		return false
	}
	for _, arg := range []string{"knowledge_base_id", "dataset_id", "series_id", "start_date", "end_date", "operation", "limit", "source_binding", "source_question"} {
		if schema.Properties[arg] == nil {
			return false
		}
	}
	return true
}

func (e *AgentEngine) coverageLastValidReadCall(ctx context.Context, id string, goal *int, field coverageEvidence, binding []any) (types.LLMToolCall, bool) {
	args := map[string]any{"knowledge_base_id": field.KnowledgeBaseID, "dataset_id": field.DatasetID,
		"series_id": field.SeriesID, "start_date": field.PeriodFrom, "end_date": field.PeriodTo, "operation": "rows", "limit": 1,
		"include_last_valid": true, "source_binding": binding, "source_question": e.coverageAnalysisQuestion()}
	encoded, err := json.Marshal(args)
	if err != nil {
		return types.LLMToolCall{}, false
	}
	call := types.LLMToolCall{ID: id, Type: "function", CoverageGoalIndex: goal,
		Function: types.FunctionCall{Name: coverageLastValidReadTool, Arguments: string(encoded)}}
	return call, e.coverageSourceCallAllowed(ctx, call, args)
}

const finalLastValidReadPrefix = "coverage-last-valid-"

// Continue a verified native gap even when the optional task declaration
// did not retain the field. A completed read pins the field and window.
func (e *AgentEngine) nextFinalLastValidRead(ctx context.Context, record coverageRecord, attempts []coverageAttempt) (types.LLMToolCall, bool) {
	selected, goal := e.finalLastValidField(ctx, record)
	if selected.EvidenceID == "" || !e.coverageLastValidReadAvailable() || coverageEndpointAlreadyRead(record, selected) {
		return types.LLMToolCall{}, false
	}
	key := reviewDigest([]string{record.Revision, record.AuthScope, selected.KnowledgeBaseID, selected.DatasetID, selected.SeriesID,
		selected.SnapshotHash, selected.PeriodFrom, selected.PeriodTo})
	id := finalLastValidReadPrefix + key[:24]
	if slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool { return attempt.ToolCallID == id }) {
		return types.LLMToolCall{}, false
	}
	return e.coverageLastValidReadCall(ctx, id, goal, selected, scopedEndpointSourceBinding(record, selected, attempts))
}

// Track the same missing value after a failed read or an expired read budget.
func (e *AgentEngine) finalLastValidField(ctx context.Context, record coverageRecord) (coverageEvidence, *int) {
	if !scopedReadRecoveryEnabled() || !structuredresult.LastValidEnabled() {
		return coverageEvidence{}, nil
	}
	var selected coverageEvidence
	var goal *int
	for _, item := range record.Evidence {
		if item.Kind != "gap" || item.Operation != "query_series" || !item.Complete || item.SeriesID == "" ||
			item.DatasetID != item.SourceID || item.Sheet == "" || item.Column == "" || item.Period != "" ||
			item.LastValidFilterHash != "" || item.DateMode == "none" || item.DateMode == "period" ||
			!coverageExactRange(item) {
			continue
		}
		if item.LastValidDate == "" {
			item.LastValidDate = scopedEndpointDirectoryDate(record, item)
		}
		if !coverageAnalysisDay(item.LastValidDate) || item.LastValidDate >= item.PeriodFrom {
			continue
		}
		endpoint, _ := coverageEndpointForGap(record, item)
		if coverageProtocolLastValid(endpoint) || scopedEndpointValueAlreadyRead(record, item) || coverageInspectedEndpointAlreadyRead(record, item) {
			continue
		}
		target, allowed := e.finalLastValidGapGoal(ctx, record, item)
		if !allowed {
			continue
		}
		if selected.EvidenceID != "" && (!coverageSameEndpointField(selected, item) || !coverageSameEndpointSnapshot(selected, item) ||
			selected.LastValidDate != item.LastValidDate) {
			return coverageEvidence{}, nil
		}
		selected, goal = item, target
	}
	return selected, goal
}

func (e *AgentEngine) finalLastValidGapGoal(ctx context.Context, record coverageRecord, item coverageEvidence) (*int, bool) {
	targets := e.evidenceCompletionTargets()
	if len(targets) == 0 {
		return nil, true
	}
	var goal *int
	for _, target := range targets {
		from, to, _ := coverageTargetDateRange(target)
		if target.SourceTime != "" && target.SourceTime != "observed_period" ||
			target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" || slices.Equal(target.Required, []string{"optional"}) ||
			!coverageEqual(from, item.PeriodFrom) || !coverageEqual(to, item.PeriodTo) || !coverageEqual(target.Table, item.Sheet) ||
			!e.coverageMatchesPrimaryReference(target, item) || !e.coverageEvidenceAllowed(ctx, &target, item) {
			continue
		}
		if selected, matches := coveragePrimaryField(target, record, item); selected && !matches {
			continue
		}
		if goal != nil {
			return nil, false
		}
		goal = &target.GoalIndex
	}
	return goal, goal != nil
}

func scopedEndpointDirectoryDate(record coverageRecord, gap coverageEvidence) string {
	var date string
	for _, item := range record.Evidence {
		item.PeriodFrom, item.PeriodTo = gap.PeriodFrom, gap.PeriodTo
		if item.Kind == "directory" && item.Operation == "list_series" && coverageSameEndpointField(item, gap) &&
			coverageSameEndpointSnapshot(item, gap) && coverageAnalysisDay(item.LastValidDate) {
			if date != "" && date != item.LastValidDate {
				return ""
			}
			date = item.LastValidDate
		}
	}
	return date
}

func scopedEndpointValueAlreadyRead(record coverageRecord, gap coverageEvidence) bool {
	return slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		if (item.Kind != "cell" && item.Kind != "last_valid") || item.Cell == "" || item.Raw == nil ||
			item.Date != gap.LastValidDate && (item.Date < gap.PeriodFrom || item.Date > gap.PeriodTo) {
			return false
		}
		item.PeriodFrom, item.PeriodTo = gap.PeriodFrom, gap.PeriodTo
		return coverageSameEndpointField(item, gap) && coverageSameEndpointSnapshot(item, gap)
	})
}

func scopedEndpointSourceBinding(record coverageRecord, gap coverageEvidence, attempts []coverageAttempt) []any {
	for _, attempt := range attempts {
		binding, explicit := attempt.Args["source_binding"].([]any)
		if !attempt.Success || attempt.Operation != "query_series" || !explicit || binding == nil {
			continue
		}
		if slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
			return item.ToolCallID == attempt.ToolCallID && item.Kind == "gap" &&
				coverageSameEndpointField(item, gap) && coverageSameEndpointSnapshot(item, gap)
		}) {
			return binding
		}
	}

	// A successful scoped query already resolved the selected field. The
	// catalog supplies only the real group members needed to repeat that
	// selection. The service checks completeness against its current catalog.
	selected := false
	var considered []string
	for _, item := range record.Evidence {
		if item.Kind != "directory" || item.Operation != "list_series" || item.SeriesID == "" ||
			item.KnowledgeBaseID != gap.KnowledgeBaseID || item.DatasetID != gap.DatasetID ||
			item.Sheet != gap.Sheet || item.Metric != gap.Metric || !coverageSameEndpointSnapshot(item, gap) ||
			sourceNavigationString(coverageObject(item.Raw), "chart_id") != "" {
			continue
		}
		selected = selected || item.SeriesID == gap.SeriesID && item.Column == gap.Column
		considered = append(considered, item.SeriesID)
	}
	slices.Sort(considered)
	considered = slices.Compact(considered)
	if !selected || len(considered) < 2 {
		return []any{} // Explicit empty groups cannot invoke a source model.
	}
	return []any{map[string]any{"considered_ids": considered, "selected_ids": []string{gap.SeriesID},
		"reason": "Continue the same field already verified and queried for this request and range."}}
}

func coverageLastValidTarget(target taskCoverageTarget) bool {
	return target.StructuredTaskGoal != nil && target.RequestBasis != "" && target.SourceTime == "observed_period" &&
		(target.SourceMode == "only" || target.SourceMode == "named_first") &&
		target.VersionTask != "compare_sources" && target.VersionTask != "compare_versions" &&
		slices.Contains(target.OnVerifiedGap, "last_valid_same_field")
}

func (e *AgentEngine) selectedCoverageEndpointField(ctx context.Context, record coverageRecord, target taskCoverageTarget, from, to string) (coverageEvidence, bool) {
	var selected coverageEvidence
	for _, binding := range record.Bindings {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" || binding.SeriesID == "" ||
			binding.EvidenceID == "" || binding.JSONPointer == "" || binding.SnapshotHash == "" ||
			binding.PeriodFrom != from || binding.PeriodTo != to {
			continue
		}
		for _, field := range record.Evidence {
			if !field.Bound || field.SourceDomain != "structured" || field.DatasetID == "" || field.SourceID != field.DatasetID ||
				field.Sheet == "" || field.Column == "" || field.SeriesID != binding.SeriesID ||
				field.DateMode == "none" || field.DateMode == "period" ||
				field.LastValidFilterHash != "" || field.Period != "" || !validFigureDigest(field.SnapshotHash) ||
				field.EvidenceID != binding.EvidenceID || field.JSONPointer != binding.JSONPointer ||
				!coverageSameSource(binding, field) || !coverageLocation(binding, field) ||
				!coverageCurrentEvidence(record, field) || !e.coverageEvidenceAllowed(ctx, &target, field) {
				continue
			}
			if current := record.Snapshots[coverageManifestKey(field)]; current != "" && field.ManifestHash != "" && current != field.ManifestHash {
				continue
			}
			switch field.Kind {
			case "directory":
				metadata := coverageObject(field.Raw)
				if field.Operation != "list_series" || !coverageAnalysisDay(field.LastValidDate) || field.LastValidDate >= from ||
					sourceNavigationString(metadata, "source_dataset_id", "chart_id") != "" {
					continue
				}
			case "gap":
				if !field.Complete || field.PeriodFrom != from || field.PeriodTo != to ||
					field.Operation != "query_series" && (field.Operation != "analyze_table" || !coverageCurrentAnalysisWorkbook(record, field)) {
					continue
				}
			default:
				continue
			}
			field.PeriodFrom, field.PeriodTo = from, to
			if selected.EvidenceID != "" && (!coverageSameEndpointField(selected, field) || !coverageSameEndpointSnapshot(selected, field)) {
				return coverageEvidence{}, false // Competing model selections need a binding decision.
			}
			if selected.EvidenceID == "" || selected.Kind == "directory" && field.Kind == "gap" {
				selected = field
			}
		}
	}
	return selected, selected.EvidenceID != ""
}

func coverageSameEndpointField(left, right coverageEvidence) bool {
	return left.KnowledgeBaseID == right.KnowledgeBaseID && left.DatasetID == right.DatasetID &&
		left.SeriesID == right.SeriesID && left.Sheet == right.Sheet && left.Column == right.Column &&
		left.PeriodFrom == right.PeriodFrom && left.PeriodTo == right.PeriodTo && left.Period == right.Period
}

func coverageEndpointAlreadyRead(record coverageRecord, field coverageEvidence) bool {
	if receipt, ok := coverageEndpointForGap(record, field); ok && receipt.LastValidStatus != "unverified" {
		return true
	}
	return coverageInspectedEndpointAlreadyRead(record, field)
}

func coverageInspectedEndpointAlreadyRead(record coverageRecord, field coverageEvidence) bool {
	for _, published := range inspectedPublishedLastValid(record) {
		if published.Date < field.PeriodFrom && published.SeriesID == field.SeriesID && published.Sheet == field.Sheet &&
			published.Column == field.Column && published.DatasetID == field.DatasetID && published.KnowledgeBaseID == field.KnowledgeBaseID &&
			coverageSameEndpointSnapshot(published, field) {
			return true
		}
	}
	return false
}

func coverageEndpointAttemptMatches(attempt coverageAttempt, field coverageEvidence) bool {
	args := attempt.Args
	if sourceNavigationString(args, "dataset_id") != field.DatasetID ||
		sourceNavigationString(args, "start_date") != field.PeriodFrom || sourceNavigationString(args, "end_date") != field.PeriodTo ||
		coverageEndpointFilterHash(args) != "" {
		return false
	}
	if kb := sourceNavigationString(args, "knowledge_base_id"); kb != "" && kb != field.KnowledgeBaseID {
		return false
	}
	return attempt.Operation == "query_series" && args["series_id"] == field.SeriesID ||
		attempt.Operation == "analyze_table" && coverageContainsString(args["series_ids"], field.SeriesID)
}

func (e *AgentEngine) coverageEndpointSourceBinding(record coverageRecord, field coverageEvidence, attempts []coverageAttempt) []any {
	for _, attempt := range attempts {
		if !attempt.Success || !coverageEndpointAttemptMatches(attempt, field) ||
			!e.coverageAnalysisQuestionBound(attempt.Args) {
			continue
		}
		binding, _ := attempt.Args["source_binding"].([]any)
		if len(binding) == 0 {
			continue
		}
		if slices.ContainsFunc(record.Evidence, func(read coverageEvidence) bool {
			return read.ToolCallID == attempt.ToolCallID && (read.Kind == "gap" || read.Kind == "analysis_input") &&
				read.Bound && coverageCurrentEvidence(record, read) && coverageSameEndpointField(read, field) && coverageSameEndpointSnapshot(read, field)
		}) {
			return binding
		}
	}
	return []any{}
}

func coverageEndpointSelectionUnresolved(attempt coverageAttempt, library, dataset, series string) bool {
	if attempt.Operation != "query_series" || attempt.result == nil || !attempt.Success ||
		attempt.Args["dataset_id"] != dataset || attempt.Args["series_id"] != series {
		return false
	}
	data := attempt.result.Data
	if data["kind"] != "structured_source_binding" ||
		data["status"] != "needs_clarification" && data["status"] != "plan_mismatch" {
		return false
	}
	return coverageObject(data["source_context"])["knowledge_base_id"] == library
}

type coverageLastValidSelectionIssue struct {
	Library       string  `json:"knowledge_base_id"`
	Dataset       string  `json:"dataset_id"`
	Series        string  `json:"previously_requested_series_id"`
	RequestedFrom string  `json:"requested_from,omitempty"`
	RequestedTo   string  `json:"requested_to,omitempty"`
	CandidateSets [][]any `json:"candidate_groups"`
}

// Keep the actual service's duplicate groups in the existing source-decision
// turn. The attempted ID names the unresolved read, not an accepted field.
func (e *AgentEngine) lastValidSelectionFeedback(ctx context.Context, state *types.AgentState) []coverageLastValidSelectionIssue {
	if state == nil || !structuredresult.LastValidEnabled() || !e.evidenceCompletionEligible() {
		return nil
	}
	current := e.currentCoverageScopeRecord(ctx)
	if current.Revision == "" {
		return nil
	}
	current.Evidence = nil
	current, attempts := e.projectCurrentTaskCoverage(state, e.evidenceCompletion.startStep, current)
	positions := make(map[string]int, len(attempts))
	for i, attempt := range attempts {
		positions[attempt.ToolCallID] = i
	}
	issues := map[[5]string]coverageLastValidSelectionIssue{}
	for index, attempt := range attempts {
		dataset, series := sourceNavigationString(attempt.Args, "dataset_id"), sourceNavigationString(attempt.Args, "series_id")
		if attempt.result == nil || dataset == "" || series == "" || attempt.Args["include_last_valid"] == false {
			continue
		}
		library := sourceNavigationString(coverageObject(attempt.result.Data["source_context"]), "knowledge_base_id")
		if library == "" || !coverageEndpointSelectionUnresolved(attempt, library, dataset, series) ||
			!slices.ContainsFunc(current.Evidence, func(field coverageEvidence) bool {
				return (field.Operation == "list_series" && field.Kind == "directory" || field.Kind == "gap" || field.Kind == "analysis_input") && field.Bound &&
					field.KnowledgeBaseID == library && field.DatasetID == dataset && field.SeriesID == series &&
					coverageCurrentEvidence(current, field) && e.coverageEvidenceAllowed(ctx, nil, field)
			}) {
			continue
		}
		from, to := coverageLastValidQuestionWindow(e.evidenceCompletionTargets(), attempt.Args)
		if slices.ContainsFunc(current.Evidence, func(item coverageEvidence) bool {
			resolved := coverageProtocolLastValid(item) || positions[item.ToolCallID] > index &&
				(item.Kind == "gap" || item.Kind == "cell" && item.Operation == "query_series")
			return item.Bound && item.KnowledgeBaseID == library && item.DatasetID == dataset && item.SeriesID == series &&
				resolved && (from == "" || item.PeriodFrom == from && item.PeriodTo == to) &&
				coverageCurrentEvidence(current, item) && e.coverageEvidenceAllowed(ctx, nil, item)
		}) {
			continue
		}
		groups, _ := attempt.result.Data["fields"].([]any)
		issue := coverageLastValidSelectionIssue{Library: library, Dataset: dataset, Series: series, RequestedFrom: from, RequestedTo: to}
		for _, raw := range groups {
			group, _ := raw.([]any)
			if slices.ContainsFunc(group, func(field any) bool { return coverageObject(field)["series_id"] == series }) {
				issue.CandidateSets = append(issue.CandidateSets, group)
			}
		}
		if len(issue.CandidateSets) == 0 {
			continue
		}
		issues[[5]string{library, dataset, series, from, to}] = issue
	}
	var result []coverageLastValidSelectionIssue
	for _, issue := range issues {
		result = append(result, issue)
	}
	slices.SortFunc(result, func(a, b coverageLastValidSelectionIssue) int {
		return slices.Compare([]string{a.Library, a.Dataset, a.Series, a.RequestedFrom, a.RequestedTo},
			[]string{b.Library, b.Dataset, b.Series, b.RequestedFrom, b.RequestedTo})
	})
	return result
}

func coverageLastValidQuestionWindow(targets []taskCoverageTarget, args map[string]any) (string, string) {
	if from, to := sourceNavigationString(args, "start_date"), sourceNavigationString(args, "end_date"); coverageAnalysisDay(from) && coverageAnalysisDay(to) && from <= to {
		return from, to
	}
	var foundFrom, foundTo string
	for _, target := range targets {
		if !coverageLastValidTarget(target) {
			continue
		}
		from, to, _ := coverageTargetDateRange(target)
		if !coverageAnalysisDay(from) || !coverageAnalysisDay(to) || from > to {
			continue
		}
		if foundFrom != "" && (foundFrom != from || foundTo != to) {
			return "", ""
		}
		foundFrom, foundTo = from, to
	}
	return foundFrom, foundTo
}
