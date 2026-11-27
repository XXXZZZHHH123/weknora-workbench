package agent

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type coverageDraftReview struct {
	record      coverageRecord
	targets     []taskCoverageTarget
	facts       []coverageDraftFact
	comparisons []coverageComparisonCheck
	version     terminalVersionChoice
}

type coverageDraftFact struct {
	coverageFact
	goal int
	slot string
}

type coverageDraftAssessment struct {
	anchors          []string
	roles            []string
	missing          []coverageDraftFact
	versionDisplayed bool
}

type coverageDraftFeedback struct {
	Missing        []coverageDraftFactView   `json:"undisplayed_verified_facts,omitempty"`
	Comparisons    []coverageComparisonCheck `json:"checked_same_table_comparisons,omitempty"`
	Version        *terminalVersionChoice    `json:"checked_provisional_version_choice,omitempty"`
	VersionMissing bool                      `json:"version_basis_undisplayed,omitempty"`
}

func (e *AgentEngine) currentCoverageDraftReview(ctx context.Context, state *types.AgentState) coverageDraftReview {
	var review coverageDraftReview
	if state == nil || !e.evidenceCompletionEligible() || !e.standardTextDelivery(state) {
		return review
	}
	turn := &e.evidenceCompletion
	task := e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation)
	scope := e.unverifiedSourceScopeDigest(ctx)
	record := coverageRecord{Revision: task, AuthScope: scope}
	// Bind declarations to the task and authorization under which the selected
	// receipt was verified. Reproject actual results for both switch settings.
	if turn.revision == task && turn.scope == scope {
		record.Bindings = turn.record.Bindings
	}
	record, _ = e.projectCurrentTaskCoverage(state, turn.startStep, record)
	review.targets = e.evidenceCompletionTargets()
	var versions []coverageFact
	for _, target := range review.targets {
		scoped := e.coverageRecordForTarget(ctx, target, record)
		for _, result := range evaluateTaskCoverage(target, scoped) {
			if tableComparison(target) && len(result.PinnedFacts) > 0 {
				checked := checkedTableComparison(target, result)
				for _, fact := range result.PinnedFacts {
					if fact.Role == "scoped_value" {
						checked.Sides = append(checked.Sides, e.coverageDraftFactView(coverageDraftFact{fact, target.GoalIndex, result.GapID}))
					}
				}
				review.comparisons = append(review.comparisons, checked)
			}
			for _, fact := range result.PinnedFacts {
				if fact.Role == "supporting" && !slices.Contains(target.Required, "supporting") {
					continue
				}
				switch fact.Role {
				case "scoped_value", "supporting", "coverage_boundary", "last_valid_date", "last_valid_value", "supplement", "supplement_context":
					review.facts = append(review.facts, coverageDraftFact{fact, target.GoalIndex, result.GapID})
					versions = append(versions, fact)
				}
			}
		}
	}
	record.Evidence = slices.DeleteFunc(record.Evidence, func(item coverageEvidence) bool {
		return !item.Bound || !coverageCurrentEvidence(record, item) || !e.coverageEvidenceAllowed(ctx, nil, item)
	})
	review.record = record
	review.version = currentTerminalVersionChoice(record, versions)
	return review
}

// Facts may be returned by several calls, but a role in the answer belongs
// to a physical field, source version and period. Matching one value cannot
// discharge another field with the same number.
func (fact coverageDraftFact) roleID() string {
	item := fact.Evidence
	return reviewDigest([]any{fact.goal, fact.slot, fact.Role, comparisonPhysicalSource(item), item.SnapshotHash,
		item.Sheet, item.SeriesID, item.Subject, item.Metric, item.Column, item.Object, item.Page})
}

func coverageDraftFactIdentity(item coverageEvidence) string {
	return reviewDigest([]any{coverageFactRef(item), comparisonPhysicalSource(item), item.SnapshotHash, item.ContentHash,
		item.Sheet, item.SeriesID, item.Subject, item.Metric, item.Column, item.Object, item.Page, item.Cell,
		item.Period, item.PeriodFrom, item.PeriodTo, item.Date, item.Raw, item.ReportedValue, item.Unit, item.UnitBasis})
}

func appendCoverageDraftID(ids []string, id string) []string {
	if !slices.Contains(ids, id) {
		return append(ids, id)
	}
	return ids
}

func containsCoverageDraftRoles(ids, required []string) bool {
	return !slices.ContainsFunc(required, func(id string) bool { return !slices.Contains(ids, id) })
}

func strictlyContainsCoverageDraftFacts(facts, other []string) bool {
	return len(facts) > len(other) && containsCoverageDraftRoles(facts, other)
}

func moreCompleteCoverageDraft(a candidateDecision, assessment coverageDraftAssessment, b candidateDecision, roles, facts []string) bool {
	if len(assessment.roles) != len(roles) {
		return len(assessment.roles) > len(roles)
	}
	// A draft can retain a genuinely witnessed fact even without a validated
	// plan. Use strict inclusion for that tie so repeated numbers cannot win.
	if aContains, bContains := strictlyContainsCoverageDraftFacts(assessment.anchors, facts), strictlyContainsCoverageDraftFacts(facts, assessment.anchors); aContains != bContains {
		return aContains
	}
	return a.CandidateHash < b.CandidateHash
}

func (e *AgentEngine) rememberCoverageDraft(ctx context.Context, decision candidateDecision, assessment coverageDraftAssessment, review coverageDraftReview, state *types.AgentState) {
	if decision.Err != nil || len(decision.Unsupported) != 0 || len(assessment.anchors) == 0 {
		return
	}
	if prior, current := e.currentCheckedCoverageDraft(ctx, state, review); current &&
		!moreCompleteCoverageDraft(decision, assessment, prior, e.turnReview.CoverageDraftRoles, e.turnReview.CoverageDraftFacts) {
		return
	}
	e.turnReview.CoverageDraft = decision
	e.turnReview.CoverageDraftFacts = assessment.anchors
	e.turnReview.CoverageDraftRoles = assessment.roles
}

func (e *AgentEngine) currentCheckedCoverageDraft(ctx context.Context, state *types.AgentState, review coverageDraftReview) (candidateDecision, bool) {
	draft := e.turnReview.CoverageDraft
	if draft.Candidate == "" ||
		draft.TaskHash != e.candidateTaskDigest(e.turnReview.Query, e.turnReview.TaskConversation) ||
		draft.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		draft.CandidateHash != candidateTextDigest(draft.Candidate) ||
		draft.StandardReview != e.standardAnswerReview() ||
		draft.StructuredResultReview != e.structuredResultReviewEnabled() {
		return candidateDecision{}, false
	}
	current := e.assessCoverageDraft(review, draft.Candidate, state)
	if len(current.anchors) == 0 || !containsCoverageDraftRoles(current.anchors, e.turnReview.CoverageDraftFacts) ||
		!containsCoverageDraftRoles(current.roles, e.turnReview.CoverageDraftRoles) {
		return candidateDecision{}, false
	}
	return draft, true
}

func (e *AgentEngine) currentCoverageDraft(ctx context.Context, state *types.AgentState) (string, bool) {
	if e.turnReview.CoverageDraft.Candidate == "" {
		return "", false
	}
	draft, ok := e.currentCheckedCoverageDraft(ctx, state, e.currentCoverageDraftReview(ctx, state))
	return draft.Candidate, ok
}

func (e *AgentEngine) bindCoverageDraftDelivery(base, final string) {
	if base == e.turnReview.CoverageDraft.Candidate && base == final && final != "" {
		e.turnReview.CoverageDraftDeliveryHash = candidateTextDigest(final)
	}
}

func (e *AgentEngine) coverageDraftDeliveryCurrent(ctx context.Context, state *types.AgentState) bool {
	if state == nil || e.turnReview.CoverageDraftDeliveryHash == "" ||
		e.turnReview.CoverageDraftDeliveryHash != candidateTextDigest(state.FinalAnswer) {
		return false
	}
	base, ok := e.currentCoverageDraft(ctx, state)
	return ok && state.FinalAnswer == base
}

func (e *AgentEngine) coverageDraftAnchors(ctx context.Context, candidate string, state *types.AgentState) []string {
	return e.assessCoverageDraft(e.currentCoverageDraftReview(ctx, state), candidate, state).anchors
}

func (e *AgentEngine) assessCoverageDraft(review coverageDraftReview, candidate string, state *types.AgentState) coverageDraftAssessment {
	var assessment coverageDraftAssessment
	if state == nil || terminalCandidateFallback(candidate) || !e.candidateEvidenceBudgetFits(candidate, state) ||
		e.candidateCitationsInvalid(candidate, state, e.turnReview.Conversation) {
		return assessment
	}
	paragraphs := strings.Split(strings.ReplaceAll(candidate, "\r\n", "\n"), "\n\n")
	visible := make([]coverageDraftParagraph, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		visible = append(visible, newCoverageDraftParagraph(paragraph))
	}
	var witnesses []float64
	safeCalls := map[string]bool{}
	add := func(item coverageEvidence) {
		assessment.anchors = appendCoverageDraftID(assessment.anchors, coverageDraftFactIdentity(item))
		raw, _ := coverageDraftRaw(item)
		if number, err := strconv.ParseFloat(raw, 64); err == nil {
			witnesses = append(witnesses, number)
		}
		if item.Kind == "body" || item.Kind == "derived" {
			safeCalls[item.ToolCallID] = true
		}
	}
	for _, fact := range review.facts {
		item := fact.Evidence
		if slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool { return paragraph.displaysVerifiedRole(fact, review.facts) }) {
			add(item)
			assessment.roles = appendCoverageDraftID(assessment.roles, fact.roleID())
		} else {
			assessment.missing = append(assessment.missing, fact)
		}
	}
	if review.version.SelectedSource != "" && slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool {
		selected := strings.ToLower(review.version.SelectedSource)
		alternative := strings.ToLower(review.version.AlternativeSource)
		return strings.Contains(strings.ReplaceAll(paragraph.lower, alternative, ""), selected) &&
			strings.Contains(paragraph.lower, alternative) &&
			strings.Contains(strings.ReplaceAll(strings.ReplaceAll(paragraph.lower, alternative, ""), selected, ""), strings.ToLower(review.version.Basis))
	}) && containsCoverageDraftRoles(assessment.anchors, []string{review.version.selectedRef, review.version.alternativeRef}) {
		assessment.roles = appendCoverageDraftID(assessment.roles, reviewDigest(review.version))
		assessment.versionDisplayed = true
	}
	// When there are no bound target facts, an actual displayed and cited
	// source field still contributes to ranking a usable draft. These roles
	// never become required facts or revision feedback.
	nativeCells := map[string]bool{}
	for _, item := range review.record.Evidence {
		if item.Kind == "cell" && item.CitationID != "" && item.Cell != "" {
			nativeCells[item.CitationID] = true
		}
	}
	for _, item := range review.record.Evidence {
		if slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool { return paragraph.displays(item, "", false) }) {
			add(item)
		}
		if len(review.facts) != 0 || item.CitationID == "" && item.ChunkID == "" || item.Kind == "derived" && nativeCells[item.CitationID] ||
			!slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool { return paragraph.displays(item, "", true) }) {
			continue
		}
		role := reviewDigest([]any{comparisonPhysicalSource(item), item.SnapshotHash,
			item.Sheet, item.SeriesID, item.Subject, item.Metric, item.Column, item.Object, item.Page})
		assessment.roles = appendCoverageDraftID(assessment.roles, role)
	}
	if len(assessment.anchors) == 0 {
		return assessment
	}
	var selected []types.AgentStep
	for _, step := range state.RoundSteps {
		var calls []types.ToolCall
		for _, call := range step.ToolCalls {
			if safeCalls[call.ID] {
				calls = append(calls, call)
			}
		}
		selected = append(selected, types.AgentStep{ToolCalls: calls})
	}
	evidence := e.answerNumericEvidence(candidate, selected)
	evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &chat.NumericObservations{Values: witnesses}})
	if len(ungroundedStructuredNumberOccurrencesWithEmptyEvidence(candidate, evidence, true, selected)) > 0 {
		assessment.anchors = nil
		assessment.roles = nil
		assessment.versionDisplayed = false
	}
	return assessment
}

type coverageDraftParagraph struct {
	text, lower string
	chunks      []string
	results     []string
}

var coverageDraftAssertionBoundary = regexp.MustCompile(`[。！？!?；;\n]+|\.[ \t]+`)

func newCoverageDraftParagraph(text string) coverageDraftParagraph {
	visible := agenttools.StripThinkBlocks(sourceCitationTag.ReplaceAllStringFunc(text, numericMask))
	return coverageDraftParagraph{visible, strings.ToLower(visible), uniqueCandidateCitationIDs(text), uniqueCandidateEvidenceIDs(text)}
}

func (paragraph coverageDraftParagraph) hasLabel(label string) bool {
	return label != "" && strings.Contains(paragraph.lower, strings.ToLower(label))
}

func (paragraph coverageDraftParagraph) hasField(item coverageEvidence) bool {
	if item.Metric == "" {
		return paragraph.hasLabel(item.Subject)
	}
	return paragraph.hasLabel(item.Metric) && (item.Subject == "" || paragraph.hasLabel(item.Subject))
}

func coverageDraftRaw(item coverageEvidence) (string, bool) {
	if item.Kind == "body" && item.ReportedValue != "" {
		return item.ReportedValue, true
	}
	return reviewScalarWitnessQuote(item.Raw)
}

func (paragraph coverageDraftParagraph) displays(item coverageEvidence, role string, requireCitation bool) bool {
	if !slices.Contains([]string{"cell", "derived", "last_valid", "gap", "body"}, item.Kind) {
		return false
	}
	structuredCite := item.CitationID != "" && slices.Contains(paragraph.results, item.CitationID)
	cited := structuredCite || item.ChunkID != "" && slices.Contains(paragraph.chunks, item.ChunkID)
	if requireCitation && !cited && (item.CitationID != "" || item.ChunkID != "") {
		return false
	}
	// A document chunk can hold several fields with the same value. Require
	// the actual field or cell in the cited paragraph, even for a real citation.
	field := paragraph.hasField(item)
	cell := paragraph.hasLabel(item.Cell)
	if item.Kind == "body" && !cited || !field && !cell && !structuredCite {
		return false
	}
	if !cited && !field && !paragraph.hasLabel(item.SourceTitle) {
		return false
	}
	if item.Kind == "gap" {
		return role == "coverage_boundary" && item.Complete && (cited || paragraph.hasLabel(item.SourceTitle)) &&
			(paragraph.hasLabel(item.Period) || item.PeriodFrom != "" && coverageTextHasDate(paragraph.text, item.PeriodFrom) &&
				(item.PeriodTo == "" || coverageTextHasDate(paragraph.text, item.PeriodTo)))
	}
	dated := item.Date == "" || coverageTextHasDate(paragraph.text, item.Date) || paragraph.hasLabel(item.Date)
	if requireCitation && !dated {
		// As-reported periods can legitimately appear as their original row
		// label instead of the normalized date representing that row.
		dated = paragraph.hasLabel(item.Period) || paragraph.hasLabel(item.RowLabel) || cell
	}
	if role == "last_valid_date" || item.Kind == "last_valid" && item.Raw == nil {
		return item.Date != "" && dated && (cited || paragraph.hasLabel(item.SourceTitle))
	}
	if requireCitation && !dated || item.Kind == "last_valid" && !dated {
		return false
	}
	raw, ok := coverageDraftRaw(item)
	if !ok || raw == "" {
		return false
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		assertions := coverageDraftAssertionBoundary.Split(paragraph.text, -1)
		for _, assertion := range assertions {
			assertion = strings.ToLower(assertion)
			local := coverageDraftParagraph{lower: assertion}
			localField := local.hasField(item)
			localCell := item.Cell != "" && strings.Contains(assertion, strings.ToLower(item.Cell))
			if !localField && !localCell && !(structuredCite && len(assertions) == 1) {
				continue
			}
			if requireCitation && terminalUnitFromSource(item) && !local.hasLabel(item.Unit) {
				continue
			}
			if requireCitation && item.Date != "" && calendarDateLiteral.MatchString(assertion) &&
				!coverageTextHasDate(assertion, item.Date) && !local.hasLabel(item.Date) && !local.hasLabel(item.Period) {
				continue
			}
			if coverageDraftAmountOccurrences(assertion, number) > 0 {
				return true
			}
		}
		return false
	}
	// A cited body anchors a textual display only when the selected source
	// supplies the actual excerpt. A title or a bare citation is insufficient.
	return len(raw) <= 512 && paragraph.hasLabel(raw)
}

func coverageDraftAmountOccurrences(text string, raw float64) int {
	numeric := cellOrListMarker.ReplaceAllStringFunc(maskValidatedCalendarDates(text), numericMask)
	matches := 0
	for _, token := range proseNumber.FindAllString(numeric, -1) {
		token = strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-")
		value, err := strconv.ParseFloat(token, 64)
		_, fraction, _ := strings.Cut(token, ".")
		if err == nil && math.Abs(value-raw) <= 0.500001*math.Pow10(-len(fraction)) {
			matches++
		}
	}
	return matches
}

func (paragraph coverageDraftParagraph) displaysVerifiedRole(fact coverageDraftFact, facts []coverageDraftFact) bool {
	item := fact.Evidence
	if !paragraph.displays(item, fact.Role, true) {
		return false
	}
	number, err := strconv.ParseFloat(item.ReportedValue, 64)
	if item.Kind != "body" || item.ChunkID == "" || err != nil {
		return true
	}
	// A document citation identifies a chunk, which can include several
	// columns with the same value. A single printed operand cannot, by
	// itself, establish two independent cells within that chunk.
	fields := map[[3]string]bool{{item.Metric, item.Subject, item.Column}: true}
	for _, peer := range facts {
		other := peer.Evidence
		if other.ChunkID == item.ChunkID && other.ReportedValue == item.ReportedValue && paragraph.displays(other, peer.Role, true) {
			fields[[3]string{other.Metric, other.Subject, other.Column}] = true
		}
	}
	return len(fields) == 1 || coverageDraftAmountOccurrences(paragraph.text, number) >= len(fields)
}

type coverageDraftFactView struct {
	Goal       int    `json:"goal_index"`
	Role       string `json:"role"`
	Ref        string `json:"ref"`
	Citation   string `json:"citation_handle,omitempty"`
	Source     string `json:"source,omitempty"`
	Sheet      string `json:"sheet,omitempty"`
	Metric     string `json:"metric,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Page       int    `json:"page,omitempty"`
	Cell       string `json:"cell,omitempty"`
	Row        string `json:"row,omitempty"`
	Column     string `json:"column,omitempty"`
	Date       string `json:"date,omitempty"`
	Period     string `json:"period,omitempty"`
	PeriodFrom string `json:"period_from,omitempty"`
	PeriodTo   string `json:"period_to,omitempty"`
	SourceDate string `json:"source_date,omitempty"`
	Value      string `json:"raw,omitempty"`
	Unit       string `json:"unit,omitempty"`
	UnitBasis  string `json:"unit_basis,omitempty"`
	Status     string `json:"source_status,omitempty"`
}

func (e *AgentEngine) coverageDraftFactView(fact coverageDraftFact) coverageDraftFactView {
	item := fact.Evidence
	value, _ := coverageDraftRaw(item)
	if len(value) > 512 {
		value = ""
	}
	var citation string
	if e.modelContext != nil {
		citation = firstNonEmptyAgent(e.modelContext.CitableEvidenceHandle(item.CitationID), e.modelContext.CitableChunkHandle(item.ChunkID))
	}
	return coverageDraftFactView{
		Goal: fact.goal, Role: fact.Role, Ref: coverageFactRef(item), Citation: citation, Source: item.SourceTitle,
		Sheet: item.Sheet, Metric: item.Metric, Subject: item.Subject, Page: item.Page, Cell: item.Cell, Row: item.RowLabel,
		Column: item.Column, Date: item.Date, Period: item.Period, PeriodFrom: item.PeriodFrom, PeriodTo: item.PeriodTo,
		SourceDate: item.SourceDate, Value: value, Unit: item.Unit, UnitBasis: item.UnitBasis, Status: item.Status,
	}
}

func (e *AgentEngine) coverageDraftFeedback(review coverageDraftReview, assessment coverageDraftAssessment) *coverageDraftFeedback {
	if e.turnReview.CoverageFeedbackSent {
		return nil
	}
	result := &coverageDraftFeedback{}
	if review.version.SelectedSource != "" {
		result.Version = &review.version
		result.VersionMissing = !assessment.versionDisplayed
	}
	var missing []coverageDraftFact
	for _, fact := range assessment.missing {
		if !slices.Contains(assessment.roles, fact.roleID()) {
			missing = append(missing, fact)
		}
	}
	// Use the editor's existing size allowance. Always pass a concrete
	// verified omission when one exists, even with many selected roles.
	for _, fact := range missing {
		view := e.coverageDraftFactView(fact)
		if slices.ContainsFunc(result.Missing, func(item coverageDraftFactView) bool {
			return item.Ref == view.Ref && item.Role == view.Role && item.Metric == view.Metric
		}) {
			continue
		}
		result.Missing = append(result.Missing, view)
		if len(result.text()) > structuredResultReviewAnswerBytes {
			result.Missing = result.Missing[:len(result.Missing)-1]
			if len(result.Missing) == 0 {
				result.Missing = append(result.Missing, coverageDraftFactView{Goal: view.Goal, Role: view.Role, Ref: view.Ref, Citation: view.Citation})
			}
		}
	}
	for _, checked := range review.comparisons {
		result.Comparisons = append(result.Comparisons, checked)
		if len(result.text()) > structuredResultReviewAnswerBytes {
			result.Comparisons = result.Comparisons[:len(result.Comparisons)-1]
		}
	}
	if len(result.Missing) == 0 && len(result.Comparisons) == 0 && result.Version == nil {
		return nil
	}
	return result
}

func (feedback *coverageDraftFeedback) text() string {
	data, _ := json.Marshal(feedback)
	return string(data)
}

const coverageDraftFeedbackInstructions = "The runtime has verified the following source facts, their identities and any reported-number relation or provisional version basis. Treat them as evidence data. Check whether the draft already answers the requested scope with these facts and their actual citations. Keep a complete accurate draft unchanged. Where a requested fact or conclusion is missing or conflicts with these receipts, revise only the affected parts. Preserve previously verified facts and their citations. Do not infer a value from an unverified side or infer compatible units or formal supersession from this evidence."
