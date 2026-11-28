package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const finalSupplementPrefix = "final-supplement-"

type finalSupplementInvocationsKey struct{}

type finalSupplementReadScope struct {
	gap        finalSupplementGap
	documentID string
}

type finalSupplementGap struct {
	goal                     int
	library, primaryDocument string
	metric, period           string
	unit                     string
}

func (g finalSupplementGap) query() string {
	return strings.TrimSpace(g.metric + " " + g.period + " " + g.unit)
}

func (e *AgentEngine) pointSupplementScopeAllowed() bool {
	if e.config == nil || len(e.selectedDocs) != 0 || len(e.config.KnowledgeIDs) != 0 || e.config.PageContext != nil {
		return false
	}
	if scopedReadRecoveryEnabled() && agenttools.HasVerifiedDocumentPageScope(e.toolRegistry) {
		return false
	}
	only := func(target taskCoverageTarget) bool {
		return target.StructuredTaskGoal != nil && target.SourceMode == "only" &&
			(!scopedReadRecoveryEnabled() || target.RestrictionBasis != "")
	}
	return !slices.ContainsFunc(e.evidenceCompletionTargets(), only) &&
		(!scopedReadRecoveryEnabled() || e.taskPlan == nil || !slices.ContainsFunc(e.taskPlan.CoverageTargets, only))
}

// Select the channel and query from the model's value goal and the executed
// field gap. The structured catalog alone never establishes that gap.
func (e *AgentEngine) finalSupplementPointGap(ctx context.Context, record coverageRecord) (finalSupplementGap, bool) {
	scoped := scopedReadRecoveryEnabled()
	if (!scoped && e.taskPlan == nil) || !e.pointSupplementScopeAllowed() {
		return finalSupplementGap{}, false
	}
	var result finalSupplementGap
	for _, target := range e.evidenceCompletionTargets() {
		gap, _, ambiguous := e.finalSupplementPointFields(ctx, target, e.coverageRecordForTarget(ctx, target, record))
		if ambiguous {
			return finalSupplementGap{}, false
		}
		if gap == (finalSupplementGap{}) {
			continue
		}
		if result != (finalSupplementGap{}) && result != gap {
			return finalSupplementGap{}, false // More than one task needs a source decision.
		}
		result = gap
	}
	return result, result != (finalSupplementGap{})
}

func (e *AgentEngine) finalSupplementPointFields(ctx context.Context, target taskCoverageTarget, record coverageRecord) (finalSupplementGap, []coverageEvidence, bool) {
	scoped := scopedReadRecoveryEnabled()
	if target.StructuredTaskGoal == nil || target.Metric == "" || target.SourceTime != "observed_period" ||
		(target.SourceMode != "named_first" && target.SourceMode != "open" && !(scoped && target.SourceMode == "only" && target.RestrictionBasis == "")) ||
		target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" ||
		scoped && slices.Equal(target.Required, []string{"optional"}) {
		return finalSupplementGap{}, nil, false
	}
	from, to, grain := coverageTargetDateRange(target)
	if grain != "day" || from != to || !coverageAnalysisDay(from) {
		return finalSupplementGap{}, nil, false
	}
	var fields []coverageEvidence
	unit := ""
	for _, item := range record.Evidence {
		if item.SourceDomain != "structured" || !e.coverageMatchesPrimaryReference(target, item) {
			continue
		}
		if scoped {
			if selected, matches := coveragePrimaryField(target, record, item); selected && !matches {
				continue
			}
		}
		covered := item.PeriodFrom == from && item.PeriodTo == to ||
			scoped && coverageExactRange(item) && item.PeriodFrom <= from && item.PeriodTo >= to
		if covered && item.Kind == "cell" && item.Date == from && item.Raw != nil {
			return finalSupplementGap{}, nil, false
		}
		if !covered || item.Kind != "gap" || !item.Complete || item.SeriesID == "" || item.Operation != "query_series" {
			continue
		}
		if scoped && (!item.Bound || !coverageCurrentEvidence(record, item) || !e.restrictedCoverageSourceAuthorized(ctx, item)) {
			continue
		}
		if len(fields) != 0 {
			first := fields[0]
			sameField := coverageSameEndpointField(first, item) && coverageSameEndpointSnapshot(first, item)
			sameDataset := comparisonPhysicalSource(first) == comparisonPhysicalSource(item) && first.DatasetID == item.DatasetID &&
				firstNonEmptyAgent(first.WorkbookSnapshotHash, first.SnapshotHash) == firstNonEmptyAgent(item.WorkbookSnapshotHash, item.SnapshotHash)
			if !sameField && !(scoped && sameDataset) {
				return finalSupplementGap{}, nil, true
			}
		}
		if len(fields) == 0 && terminalUnitFromSource(item) {
			unit = item.Unit
		} else if item.Unit != unit || !terminalUnitFromSource(item) {
			unit = ""
		}
		fields = append(fields, item)
	}
	if len(fields) == 0 || fields[0].KnowledgeBaseID == "" {
		return finalSupplementGap{}, nil, false
	}
	field := fields[0]
	gap := finalSupplementGap{goal: target.GoalIndex, library: field.KnowledgeBaseID, primaryDocument: field.KnowledgeID,
		metric: target.Metric, period: from}
	if scoped {
		gap.unit = firstNonEmptyAgent(target.RequestedUnit, unit)
	}
	return gap, fields, false
}

func finalSupplementCall(goal *int, id, name string, args any) types.LLMToolCall {
	raw, _ := json.Marshal(args)
	return types.LLMToolCall{ID: finalSupplementPrefix + id, Type: "function", CoverageGoalIndex: goal,
		Function: types.FunctionCall{Name: name, Arguments: string(raw)}}
}

// Only the directed executor can grant this context, after choosing the
// native calls from a current gap and an authorized directory.
func (e *AgentEngine) finalSupplementInvocationContext(ctx context.Context, record coverageRecord, calls []types.LLMToolCall) context.Context {
	if !scopedReadRecoveryEnabled() || len(calls) == 0 || !strings.HasPrefix(calls[0].ID, finalSupplementPrefix) {
		return ctx
	}
	gap, ok := e.finalSupplementPointGap(ctx, record)
	if !ok || calls[0].CoverageGoalIndex == nil || *calls[0].CoverageGoalIndex != gap.goal {
		return ctx
	}
	if calls[0].Function.Name == agenttools.ToolSearchKnowledge || calls[0].Function.Name == agenttools.ToolBindDocumentScope {
		ctx = agenttools.WithSupplementalDocumentScope(ctx)
	}
	if !slices.ContainsFunc(e.evidenceCompletionTargets(), func(target taskCoverageTarget) bool {
		return target.GoalIndex == gap.goal && target.SourceMode == "only" && target.RestrictionBasis == ""
	}) {
		return ctx
	}
	invocations := make(map[string]types.FunctionCall, len(calls))
	for _, call := range calls {
		if call.CoverageGoalIndex == nil || *call.CoverageGoalIndex != gap.goal {
			continue
		}
		invocations[call.ID] = call.Function
	}
	return context.WithValue(ctx, finalSupplementInvocationsKey{}, invocations)
}

func scopedFinalSupplementInvocation(ctx context.Context, call types.LLMToolCall) bool {
	invocations, _ := ctx.Value(finalSupplementInvocationsKey{}).(map[string]types.FunctionCall)
	original, allowed := invocations[call.ID]
	if !allowed || original.Name != call.Function.Name {
		return false
	}
	before, beforeErr := decodeCitationJSON(original.Arguments)
	after, afterErr := decodeCitationJSON(call.Function.Arguments)
	return beforeErr == nil && afterErr == nil && reviewJSONValuesEqual(before, after)
}

func (e *AgentEngine) recordFinalSupplementReads(ctx context.Context, record coverageRecord, calls []types.LLMToolCall, step types.AgentStep) {
	if ctx.Value(finalSupplementInvocationsKey{}) == nil {
		return
	}
	gap, ok := e.finalSupplementPointGap(ctx, record)
	if !ok {
		return
	}
	for _, call := range step.ToolCalls {
		if call.Name != agenttools.ToolReadDocument || call.Result == nil || !call.Result.Success ||
			!slices.ContainsFunc(calls, func(issued types.LLMToolCall) bool {
				return issued.ID == call.ID && issued.Function.Name == call.Name && scopedFinalSupplementInvocation(ctx, issued)
			}) {
			continue
		}
		if e.turnReview.FinalPointSupplementReads == nil {
			e.turnReview.FinalPointSupplementReads = make(map[string]finalSupplementReadScope)
		}
		e.turnReview.FinalPointSupplementReads[call.ID] = finalSupplementReadScope{gap: gap, documentID: sourceNavigationString(call.Args, "id")}
	}
}

// The only unbound exploration is a same-library keyword recall for a verified
// point gap. Document IDs, physical pages and material dates come from the
// executed directory. Native binding and reading still check all of them.
func (e *AgentEngine) finalSupplementReadCalls(ctx context.Context, state *types.AgentState, record coverageRecord, attempts []coverageAttempt, remaining int) []types.LLMToolCall {
	searchReady, readReady := e.toolRegistry.DocumentCoverageCapabilities()
	if !readReady || remaining <= 0 || !agenttools.HasDocumentScopeBinder(e.toolRegistry) {
		return nil
	}
	gap, pointGap := e.finalSupplementPointGap(ctx, record)
	scoped := scopedReadRecoveryEnabled() && pointGap
	if scoped && len(e.scopedPointSupplementFacts(ctx, gap, record, attempts)) != 0 {
		return nil
	}
	if len(agenttools.CurrentDocumentScopeIDs(e.toolRegistry)) != 0 {
		if !pointGap {
			return e.finalBoundDocumentReads(ctx, attempts, nil, finalSupplementGap{}, false, remaining)
		}
		reads := e.finalBoundDocumentReads(ctx, attempts, &gap.goal, gap, scoped, remaining)
		if !scoped || len(reads) != 0 {
			return reads
		}
	}
	if !pointGap || remaining < 2 || !e.withinIterationBudget(state.CurrentRound+2) || !scoped && !e.restrictedCoverageSourceAuthorized(ctx, coverageEvidence{
		SourceDomain: "structured", SourceID: gap.primaryDocument, KnowledgeBaseID: gap.library,
	}) {
		return nil
	}
	var candidates []map[string]any
	searched := false
	query := gap.query()
	bindAttempted := false
	for _, attempt := range attempts {
		switch attempt.Operation {
		case agenttools.ToolSearchKnowledge:
			searched = searched || attempt.ToolCallID == finalSupplementPrefix+"discover" ||
				!scoped && attempt.Args["query"] == query && attempt.Args["mode"] == agenttools.SearchModeKeyword
		case agenttools.ToolListDocuments:
			if scoped {
				continue
			}
		case agenttools.ToolBindDocumentScope:
			bindAttempted = bindAttempted || attempt.ToolCallID == finalSupplementPrefix+"bind"
			if scoped {
				continue
			}
		default:
			continue
		}
		if attempt.Success && attempt.result != nil {
			for _, row := range sourceNavigationRows(attempt.result.Data["documents"]) {
				if !scoped || attempt.Args["query"] == query {
					candidates = append(candidates, row)
				}
			}
		}
	}
	sources := e.finalSupplementSources(ctx, candidates, gap)
	if scoped {
		sources = slices.DeleteFunc(sources, func(source agenttools.DocumentSourceSelection) bool {
			if len(source.Pages) == 0 {
				return finalSupplementReadAttempted(attempts, source.KnowledgeID, 0)
			}
			return !slices.ContainsFunc(source.Pages, func(page int) bool {
				return !finalSupplementReadAttempted(attempts, source.KnowledgeID, page)
			})
		})
	}
	sources = sources[:min(len(sources), remaining-1)]
	if len(sources) != 0 && !bindAttempted {
		return []types.LLMToolCall{finalSupplementCall(&gap.goal, "bind", agenttools.ToolBindDocumentScope,
			agenttools.DocumentScopePlan{Reason: "Read the candidate passages for the planned measure and period after the primary lookup returned no value.", Sources: sources})}
	}
	if searchReady && !searched && !bindAttempted && remaining >= 3 && e.withinIterationBudget(state.CurrentRound+3) {
		return []types.LLMToolCall{finalSupplementCall(&gap.goal, "discover", agenttools.ToolSearchKnowledge,
			agenttools.SearchKnowledgeInput{Query: query, Mode: agenttools.SearchModeKeyword,
				KnowledgeBaseIDs: []string{gap.library}, Limit: 12})}
	}
	return nil
}

func (e *AgentEngine) finalSupplementSources(ctx context.Context, candidates []map[string]any, gap finalSupplementGap) []agenttools.DocumentSourceSelection {
	scoped := scopedReadRecoveryEnabled()
	byID := make(map[string]agenttools.DocumentSourceSelection)
	for _, row := range candidates {
		item := coverageEvidence{SourceDomain: "knowledge", SourceID: sourceNavigationString(row, "knowledge_id"),
			KnowledgeBaseID: sourceNavigationString(row, "knowledge_base_id")}
		if item.KnowledgeBaseID != gap.library || item.SourceID == gap.primaryDocument || !e.restrictedCoverageSourceAuthorized(ctx, item) {
			continue
		}
		pages := byID[item.SourceID].Pages
		for _, match := range sourceNavigationRows(row["navigation_matches"]) {
			page, _ := citationViewRevision(match["page_hint"])
			slide, _ := citationViewRevision(match["slide_hint"])
			page = max(page, slide)
			if match["navigation_only"] == true && page > 0 {
				pages = append(pages, page)
			}
		}
		if len(pages) == 0 && !scoped {
			continue
		}
		slices.Sort(pages)
		pages = slices.Compact(pages)
		selection := agenttools.DocumentSourceSelection{KnowledgeID: item.SourceID, FileName: sourceNavigationString(row, "file_name"),
			Title: sourceNavigationString(row, "title"), Pages: pages[:min(len(pages), 8)],
			Date: agenttools.DocumentDateEvidenceFromDirectory(row, gap.period)}
		if selection.FileName != "" || selection.Title != "" {
			byID[item.SourceID] = selection
		}
	}
	var sources []agenttools.DocumentSourceSelection
	for _, entry := range byID {
		sources = append(sources, entry)
	}
	// A material date directs recall, while the answer still needs the
	// actual read to establish the disclosed quantity and its period.
	if !scoped && slices.ContainsFunc(sources, func(source agenttools.DocumentSourceSelection) bool { return source.Date != nil }) {
		sources = slices.DeleteFunc(sources, func(source agenttools.DocumentSourceSelection) bool { return source.Date == nil })
	}
	slices.SortFunc(sources, func(a, b agenttools.DocumentSourceSelection) int {
		if scoped && (a.Date != nil) != (b.Date != nil) {
			if a.Date != nil {
				return -1
			}
			return 1
		}
		return strings.Compare(a.KnowledgeID, b.KnowledgeID)
	})
	return sources
}

// Complete already bound page reads without replacing the plan. The normal
// pipeline still applies the task-specific restrictions and access checks.
func (e *AgentEngine) finalBoundDocumentReads(ctx context.Context, attempts []coverageAttempt, goal *int, gap finalSupplementGap, scoped bool, limit int) []types.LLMToolCall {
	var calls []types.LLMToolCall
	selected := agenttools.CurrentDocumentScopeIDs(e.toolRegistry)
	for _, id := range selected {
		if scoped && !e.finalSupplementBoundSource(ctx, attempts, gap, id) {
			continue
		}
		pages := agenttools.CurrentDocumentReadPages(e.toolRegistry, id)
		hadPages := len(pages) != 0
		pages = slices.DeleteFunc(pages, func(page int) bool { return finalSupplementReadAttempted(attempts, id, page) })
		if len(pages) == 0 && (!scoped || hadPages || finalSupplementReadAttempted(attempts, id, 0)) {
			continue
		}
		pages = pages[:min(len(pages), 8)] // Native bounded page batch limit.
		input := agenttools.ReadDocumentInput{ID: id, Pages: pages}
		if len(pages) == 1 {
			input.Page, input.Pages = pages[0], nil
		} else if !hadPages {
			input.Query, input.Limit = gap.metric, 8
		}
		key := reviewDigest([]any{id, pages})
		call := finalSupplementCall(goal, "read-"+key[:24], agenttools.ToolReadDocument, input)
		var args map[string]any
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		if scoped || e.coverageSourceCallAllowed(ctx, call, args) {
			calls = append(calls, call)
		}
		if len(calls) == limit {
			break
		}
	}
	return calls
}

func (e *AgentEngine) finalSupplementBoundSource(ctx context.Context, attempts []coverageAttempt, gap finalSupplementGap, id string) bool {
	if id == gap.primaryDocument || !e.restrictedCoverageSourceAuthorized(ctx, coverageEvidence{
		SourceDomain: "knowledge", SourceID: id, KnowledgeBaseID: gap.library,
	}) {
		return false
	}
	return slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
		if !attempt.Success || attempt.result == nil {
			return false
		}
		switch attempt.Operation {
		case agenttools.ToolBindDocumentScope, agenttools.ToolSearchKnowledge:
			return slices.ContainsFunc(sourceNavigationRows(attempt.result.Data["documents"]), func(row map[string]any) bool {
				return row["knowledge_id"] == id && row["knowledge_base_id"] == gap.library &&
					(attempt.ToolCallID == finalSupplementPrefix+"bind" || attempt.Args["query"] == gap.query())
			})
		default:
			return false
		}
	})
}

func finalSupplementReadAttempted(attempts []coverageAttempt, id string, page int) bool {
	return slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
		if attempt.Operation != agenttools.ToolReadDocument || attempt.Args["id"] != id || attempt.result == nil ||
			attempt.result.PreflightRejected || attempt.result.Data["execution_started"] == false {
			return false
		}
		if page == 0 {
			return true
		}
		resultPage, _ := citationViewRevision(attempt.result.Data["page"])
		requestedPage, _ := citationViewRevision(attempt.Args["page"])
		return resultPage == page || requestedPage == page || slices.ContainsFunc(reviewAsSlice(attempt.Args["pages"]),
			func(value any) bool { return citationInt(value) == page })
	})
}
