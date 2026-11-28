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

type finalSupplementGap struct {
	goal                     int
	library, primaryDocument string
	metric, period           string
}

// Select the channel and query from the model's value goal and the executed
// field gap. The structured catalog alone never establishes that gap.
func (e *AgentEngine) finalSupplementPointGap(ctx context.Context, record coverageRecord) (finalSupplementGap, bool) {
	if e.taskPlan == nil || len(e.selectedDocs) != 0 || len(e.config.KnowledgeIDs) != 0 ||
		e.config.PageContext != nil {
		return finalSupplementGap{}, false
	}
	var result finalSupplementGap
	targets := e.evidenceCompletionTargets()
	if slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" }) {
		return finalSupplementGap{}, false
	}
	for _, target := range targets {
		if target.StructuredTaskGoal == nil || target.Metric == "" || target.SourceTime != "observed_period" ||
			(target.SourceMode != "named_first" && target.SourceMode != "open") ||
			target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" {
			continue
		}
		from, to, grain := coverageTargetDateRange(target)
		if grain != "day" || from != to || !coverageAnalysisDay(from) {
			continue
		}
		current := e.coverageRecordForTarget(ctx, target, record)
		var field coverageEvidence
		for _, item := range current.Evidence {
			if item.SourceDomain != "structured" || item.PeriodFrom != from || item.PeriodTo != to ||
				!e.coverageMatchesPrimaryReference(target, item) {
				continue
			}
			if item.Kind == "cell" && item.Date == from && item.Raw != nil {
				field = coverageEvidence{}
				break // An in-period original value needs no independent fallback.
			}
			if item.Kind != "gap" || !item.Complete || item.SeriesID == "" || item.Operation != "query_series" {
				continue
			}
			if field.SeriesID != "" && (!coverageSameEndpointField(field, item) || !coverageSameEndpointSnapshot(field, item)) {
				return finalSupplementGap{}, false
			}
			field = item
		}
		if field.KnowledgeBaseID == "" {
			continue
		}
		gap := finalSupplementGap{goal: target.GoalIndex, library: field.KnowledgeBaseID, primaryDocument: field.KnowledgeID,
			metric: target.Metric, period: from}
		if result != (finalSupplementGap{}) && result != gap {
			return finalSupplementGap{}, false // More than one task needs a source decision.
		}
		result = gap
	}
	return result, result != (finalSupplementGap{})
}

func finalSupplementCall(goal *int, id, name string, args any) types.LLMToolCall {
	raw, _ := json.Marshal(args)
	return types.LLMToolCall{ID: finalSupplementPrefix + id, Type: "function", CoverageGoalIndex: goal,
		Function: types.FunctionCall{Name: name, Arguments: string(raw)}}
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
	if len(agenttools.CurrentDocumentScopeIDs(e.toolRegistry)) != 0 {
		var goal *int
		if pointGap {
			goal = &gap.goal
		}
		return e.finalBoundDocumentReads(ctx, attempts, goal, remaining)
	}
	if !pointGap || remaining < 2 || !e.withinIterationBudget(state.CurrentRound+2) || !e.restrictedCoverageSourceAuthorized(ctx, coverageEvidence{
		SourceDomain: "structured", SourceID: gap.primaryDocument, KnowledgeBaseID: gap.library,
	}) {
		return nil
	}
	var candidates []map[string]any
	searched := false
	query := gap.metric + " " + gap.period
	bindAttempted := false
	for _, attempt := range attempts {
		switch attempt.Operation {
		case agenttools.ToolSearchKnowledge:
			searched = searched || attempt.ToolCallID == finalSupplementPrefix+"discover" ||
				attempt.Args["query"] == query && attempt.Args["mode"] == agenttools.SearchModeKeyword
		case agenttools.ToolListDocuments:
		case agenttools.ToolBindDocumentScope:
			bindAttempted = bindAttempted || attempt.ToolCallID == finalSupplementPrefix+"bind"
		default:
			continue
		}
		if attempt.Success && attempt.result != nil {
			candidates = append(candidates, sourceNavigationRows(attempt.result.Data["documents"])...)
		}
	}
	sources := e.finalSupplementSources(ctx, candidates, gap, remaining-1)
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

func (e *AgentEngine) finalSupplementSources(ctx context.Context, candidates []map[string]any, gap finalSupplementGap, limit int) []agenttools.DocumentSourceSelection {
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
		if len(pages) == 0 {
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
	if slices.ContainsFunc(sources, func(source agenttools.DocumentSourceSelection) bool { return source.Date != nil }) {
		sources = slices.DeleteFunc(sources, func(source agenttools.DocumentSourceSelection) bool { return source.Date == nil })
	}
	slices.SortFunc(sources, func(a, b agenttools.DocumentSourceSelection) int {
		return strings.Compare(a.KnowledgeID, b.KnowledgeID)
	})
	return sources[:min(len(sources), limit)]
}

// Complete already bound page reads without replacing the plan. The normal
// pipeline still applies the task-specific restrictions and access checks.
func (e *AgentEngine) finalBoundDocumentReads(ctx context.Context, attempts []coverageAttempt, goal *int, limit int) []types.LLMToolCall {
	var calls []types.LLMToolCall
	selected := agenttools.CurrentDocumentScopeIDs(e.toolRegistry)
	for _, id := range selected {
		pages := agenttools.CurrentDocumentReadPages(e.toolRegistry, id)
		pages = slices.DeleteFunc(pages, func(page int) bool {
			return slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
				if attempt.Operation != agenttools.ToolReadDocument || attempt.Args["id"] != id || attempt.result == nil ||
					attempt.result.PreflightRejected || attempt.result.Data["execution_started"] == false {
					return false
				}
				resultPage, _ := citationViewRevision(attempt.result.Data["page"])
				requestedPage, _ := citationViewRevision(attempt.Args["page"])
				return resultPage == page || requestedPage == page || slices.ContainsFunc(reviewAsSlice(attempt.Args["pages"]),
					func(value any) bool { return citationInt(value) == page })
			})
		})
		if len(pages) == 0 {
			continue
		}
		pages = pages[:min(len(pages), 8)] // Native bounded page batch limit.
		input := agenttools.ReadDocumentInput{ID: id, Pages: pages}
		if len(pages) == 1 {
			input.Page, input.Pages = pages[0], nil
		}
		key := reviewDigest([]any{id, pages})
		call := finalSupplementCall(goal, "read-"+key[:24], agenttools.ToolReadDocument, input)
		var args map[string]any
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		if e.coverageSourceCallAllowed(ctx, call, args) {
			calls = append(calls, call)
		}
		if len(calls) == limit {
			break
		}
	}
	return calls
}
