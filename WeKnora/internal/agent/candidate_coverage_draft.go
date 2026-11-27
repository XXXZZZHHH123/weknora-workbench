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
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type coverageDraftReview struct {
	record      coverageRecord
	targets     []taskCoverageTarget
	facts       []coverageDraftFact
	readings    [][]coverageDraftFact
	comparisons []coverageComparisonCheck
	version     terminalVersionChoice
}

type coverageDraftFact struct {
	coverageFact
	goal int
	slot string
}

type coverageDraftAssessment struct {
	anchors            []string
	roles              []string
	missing            []coverageDraftFact
	versionDisplayed   bool
	versionBlocked     bool
	endpointMismatch   bool
	comparisonBlocked  bool
	partialComparison  bool
	observedComparison bool
	unattributedTable  []unattributedTableValue
	tableScopeReview   bool
}

type coverageDraftFeedback struct {
	Missing           []coverageDraftFactView   `json:"undisplayed_verified_facts,omitempty"`
	Comparisons       []coverageComparisonCheck `json:"checked_same_table_comparisons,omitempty"`
	Version           *terminalVersionChoice    `json:"checked_provisional_version_choice,omitempty"`
	VersionMissing    bool                      `json:"version_basis_undisplayed,omitempty"`
	UnverifiedVersion bool                      `json:"unverified_version_claim,omitempty"`
	TableAttribution  []unattributedTableValue  `json:"unattributed_cited_table_values,omitempty"`
	CheckTableScope   bool                      `json:"check_independent_table_scope,omitempty"`
}

func (e *AgentEngine) currentCoverageDraftReview(ctx context.Context, state *types.AgentState, draft ...string) coverageDraftReview {
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
	inspected := inspectedPublishedLastValid(record)
	review.targets = e.evidenceCompletionTargets()
	candidate := ""
	if len(draft) != 0 {
		candidate = draft[0]
	}
	var versions []coverageFact
	for _, target := range review.targets {
		scoped := e.coverageRecordForTarget(ctx, target, record)
		var readings []coverageFact
		var aligned bool
		if plannedTableComparison(target) {
			receipts := scoped
			receipts.Evidence = slices.DeleteFunc(slices.Clone(scoped.Evidence), func(item coverageEvidence) bool {
				return !e.coverageEvidenceAllowed(ctx, nil, item)
			})
			readings, aligned = e.tableReceiptReadings(target, receipts, candidate)
		}
		for _, result := range evaluateTaskCoverage(target, scoped) {
			if tableComparison(target) && (agenttools.DocumentTableEvidenceEnabled() || len(result.PinnedFacts) > 0) || aligned {
				checked := checkedTableComparison(target, result)
				if result.State != coverageSatisfied && aligned {
					checked.State, checked.Reason = result.State, result.Reason
					var pair []coverageDraftFact
					for _, fact := range readings {
						item := coverageDraftFact{fact, target.GoalIndex, result.GapID}
						pair = append(pair, item)
						checked.Sides = append(checked.Sides, e.coverageDraftFactView(item))
					}
					review.readings = append(review.readings, pair)
					review.facts = append(review.facts, pair...)
					observed := checkedTableComparison(target, coverageCompletion{State: coverageSatisfied, PinnedFacts: readings})
					checked.Result, checked.SameUnit, checked.UnitStatus = observed.Result, observed.SameUnit, comparisonUnitStatus(readings[0].Evidence, readings[1].Evidence)
				} else {
					for _, fact := range result.PinnedFacts {
						if fact.Role == "scoped_value" {
							checked.Sides = append(checked.Sides, e.coverageDraftFactView(coverageDraftFact{fact, target.GoalIndex, result.GapID}))
						}
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
	record.Evidence = append(record.Evidence, inspected...)
	record.Evidence = slices.DeleteFunc(record.Evidence, func(item coverageEvidence) bool {
		return !item.Bound || !coverageCurrentEvidence(record, item) || !e.coverageEvidenceAllowed(ctx, nil, item)
	})
	review.record = record
	review.version = currentTerminalVersionChoice(record, versions)
	return review
}

func comparisonDraftChecked(decision candidateDecision, assessment coverageDraftAssessment) bool {
	return decision.ReasonCode == checkedPartialComparisonReason && assessment.partialComparison ||
		decision.ReasonCode == checkedTableReadingsReason && assessment.observedComparison
}

func (review coverageDraftReview) blockedComparison() bool {
	return (agenttools.DocumentTableEvidenceEnabled() || len(review.readings) > 0) && slices.ContainsFunc(review.comparisons, func(comparison coverageComparisonCheck) bool {
		return comparison.State != coverageSatisfied
	})
}

// Facts may be returned by several calls, but a role in the answer belongs
// to a physical field, source version and period. Matching one value cannot
// discharge another field with the same number.
func (fact coverageDraftFact) roleID() string {
	item := fact.Evidence
	if agenttools.DocumentTableEvidenceEnabled() {
		return reviewDigest([]any{fact.goal, fact.slot, fact.Role, item.ComparisonSourceRef, comparisonPhysicalSource(item),
			item.SnapshotHash, item.Sheet, item.SeriesID, item.Subject, item.Metric, item.Column, item.Object, item.TableGrid})
	}
	return reviewDigest([]any{fact.goal, fact.slot, fact.Role, comparisonPhysicalSource(item), item.SnapshotHash,
		item.Sheet, item.SeriesID, item.Subject, item.Metric, item.Column, item.Object, item.Page})
}

func coverageDraftFactIdentity(item coverageEvidence) string {
	if agenttools.DocumentTableEvidenceEnabled() {
		return coverageEvidenceFactKey(item)
	}
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
	if decision.Err != nil || len(decision.Unsupported) != 0 || len(assessment.anchors) == 0 ||
		assessment.versionBlocked || assessment.endpointMismatch || len(assessment.unattributedTable) != 0 || assessment.comparisonBlocked &&
		!comparisonDraftChecked(decision, assessment) {
		return
	}
	if prior, current := e.currentCheckedCoverageDraft(ctx, state, review); current &&
		!moreCompleteCoverageDraft(decision, assessment, prior, e.turnReview.CoverageDraftRoles, e.turnReview.CoverageDraftFacts) {
		return
	}
	e.turnReview.CoverageDraft = decision
	e.turnReview.CoverageDraftFacts = assessment.anchors
	e.turnReview.CoverageDraftRoles = assessment.roles
	if agenttools.DocumentTableEvidenceEnabled() {
		common.PipelineInfo(ctx, "Agent", "coverage_draft_checked", map[string]any{
			"candidate_hash": decision.CandidateHash, "verified_fact_ids": assessment.anchors, "displayed_role_ids": assessment.roles,
			"reason": "candidate_contains_current_verified_facts", "adopted_version_basis_displayed": assessment.versionDisplayed,
		})
	}
}

func (e *AgentEngine) currentCheckedCoverageDraft(ctx context.Context, state *types.AgentState, review coverageDraftReview) (candidateDecision, bool) {
	draft := e.turnReview.CoverageDraft
	if draft.Candidate == "" ||
		draft.TaskHash != e.candidateTaskDigest(e.turnReview.Query, e.turnReview.TaskConversation) ||
		draft.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		draft.CandidateHash != candidateTextDigest(draft.Candidate) ||
		draft.StandardReview != e.standardAnswerReview() ||
		draft.StructuredResultReview != e.structuredResultReviewEnabled() ||
		agenttools.DocumentTableEvidenceEnabled() && !draft.PublicationEvidence.current(e.terminalEvidenceRevision(ctx, state)) {
		return candidateDecision{}, false
	}
	if draft.ReasonCode == checkedTableReadingsReason && len(review.readings) == 0 {
		review = e.currentCoverageDraftReview(ctx, state, draft.Candidate)
	}
	current := e.assessCoverageDraft(ctx, review, draft.Candidate, state)
	if current.versionBlocked || current.endpointMismatch || len(current.unattributedTable) != 0 || current.comparisonBlocked &&
		!comparisonDraftChecked(draft, current) ||
		len(current.anchors) == 0 || !containsCoverageDraftRoles(current.anchors, e.turnReview.CoverageDraftFacts) ||
		!containsCoverageDraftRoles(current.roles, e.turnReview.CoverageDraftRoles) {
		return candidateDecision{}, false
	}
	return draft, true
}

func (e *AgentEngine) currentCoverageDraft(ctx context.Context, state *types.AgentState) (string, bool) {
	if e.turnReview.CoverageDraft.Candidate == "" {
		return "", false
	}
	draft, ok := e.currentCheckedCoverageDraft(ctx, state, e.currentCoverageDraftReview(ctx, state, e.turnReview.CoverageDraft.Candidate))
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
	return e.assessCoverageDraft(ctx, e.currentCoverageDraftReview(ctx, state, candidate), candidate, state).anchors
}

func (e *AgentEngine) assessCoverageDraft(ctx context.Context, review coverageDraftReview, candidate string, state *types.AgentState) coverageDraftAssessment {
	versionClaims := e.evidenceCompletion.versionClaims[candidateTextDigest(strings.TrimSpace(candidate))]
	assessment := coverageDraftAssessment{comparisonBlocked: review.blockedComparison(), versionBlocked: len(versionClaims) > 0}
	if state == nil || terminalCandidateFallback(candidate) || !e.candidateEvidenceBudgetFits(candidate, state, ctx) ||
		e.candidateCitationsInvalid(candidate, state, e.turnReview.Conversation, ctx) {
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
		if slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool {
			if paragraph.displaysVerifiedRole(fact, review.facts) {
				return true
			}
			return agenttools.DocumentTableEvidenceEnabled() && slices.ContainsFunc(item.Receipts, func(receipt coverageFactReceipt) bool {
				copy := fact
				copy.Evidence.CitationID, copy.Evidence.ChunkID = receipt.Citation, receipt.Chunk
				return (receipt.Citation != "" || receipt.Chunk != "") && paragraph.displaysVerifiedRole(copy, review.facts)
			})
		}) {
			add(item)
			assessment.roles = appendCoverageDraftID(assessment.roles, fact.roleID())
		} else {
			assessment.missing = append(assessment.missing, fact)
		}
	}
	var tableNumbers []float64
	assessment.unattributedTable, assessment.tableScopeReview, tableNumbers = citedMultiSourceTableValues(review, candidate)
	if assessment.comparisonBlocked {
		assessment.partialComparison = verifiedOneSidedComparison(review, assessment, candidate)
		assessment.observedComparison = len(review.readings) > 0 && !slices.ContainsFunc(review.readings, func(pair []coverageDraftFact) bool {
			return slices.ContainsFunc(pair, func(fact coverageDraftFact) bool { return !slices.Contains(assessment.roles, fact.roleID()) })
		}) && verifiedObservedComparisonNumbers(review, assessment, candidate, tableNumbers)
	}
	if review.version.SelectedSource != "" && containsCoverageDraftRoles(assessment.anchors, []string{review.version.selectedRef, review.version.alternativeRef}) {
		if agenttools.DocumentTableEvidenceEnabled() {
			assessment.versionDisplayed = versionChoiceCited(candidate, review.version, review.facts)
		} else {
			selected := strings.ToLower(review.version.SelectedSource)
			alternative := strings.ToLower(review.version.AlternativeSource)
			assessment.versionDisplayed = slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool {
				return strings.Contains(strings.ReplaceAll(paragraph.lower, alternative, ""), selected) &&
					strings.Contains(paragraph.lower, alternative) &&
					strings.Contains(strings.ReplaceAll(strings.ReplaceAll(paragraph.lower, alternative, ""), selected, ""), strings.ToLower(review.version.Basis))
			})
		}
		if assessment.versionDisplayed {
			assessment.roles = appendCoverageDraftID(assessment.roles, reviewDigest(review.version))
			assessment.versionBlocked = slices.ContainsFunc(versionClaims, func(claim coverageVersionClaim) bool { return claim != review.version.claim })
		}
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
		if coverageProtocolLastValid(item) && item.LastValidDirection == "before_range" && item.CitationID != "" &&
			slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool {
				return slices.Contains(paragraph.results, item.CitationID) && !paragraph.displaysEndpoint(item)
			}) {
			assessment.endpointMismatch = true
			assessment.missing = append(assessment.missing, coverageDraftFact{
				coverageFact: coverageFact{Role: "last_valid_value", Evidence: item}, slot: item.EvidenceID,
			})
		}
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
	evidence := appendCoverageLastValidNumbers(e.answerNumericEvidence(candidate, selected), review.record)
	evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &chat.NumericObservations{Values: witnesses}})
	if len(ungroundedStructuredNumberOccurrencesWithEmptyEvidence(candidate, evidence, true, selected)) > 0 {
		assessment.anchors = nil
		assessment.roles = nil
		assessment.versionDisplayed = false
		assessment.observedComparison = false
		assessment.versionBlocked = len(versionClaims) > 0
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
	if coverageProtocolLastValid(item) && item.LastValidDirection == "before_range" && role != "last_valid_date" {
		return (!requireCitation || item.CitationID == "" || structuredCite) && paragraph.displaysEndpoint(item)
	}
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
	if fact.Role == "last_valid_value" && coverageProtocolLastValid(item) {
		return paragraph.displaysEndpoint(item)
	}
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

func (paragraph coverageDraftParagraph) displaysEndpoint(item coverageEvidence) bool {
	cited := item.CitationID != "" && slices.Contains(paragraph.results, item.CitationID)
	if !cited && !paragraph.hasLabel(item.SourceTitle) {
		return false
	}
	value, ok := coverageFiniteNumber(item.Raw)
	if !ok {
		return false
	}
	for _, text := range coverageDraftAssertionBoundary.Split(paragraph.text, -1) {
		assertion := newCoverageDraftParagraph(text)
		addresses := cellOrListMarker.FindAllString(strings.ToUpper(assertion.text), -1)
		otherCell := slices.ContainsFunc(addresses, func(address string) bool {
			column, _ := coverageExcelCell(address)
			return column != "" && address != item.DateCell
		})
		if !coverageTextHasDate(assertion.text, item.Date) ||
			!slices.Contains(addresses, item.Cell) && (!cited || otherCell) ||
			terminalUnitFromSource(item) && !assertion.hasLabel(item.Unit) {
			continue
		}
		numeric := cellOrListMarker.ReplaceAllStringFunc(maskValidatedCalendarDates(assertion.text), numericMask)
		for _, token := range proseNumber.FindAllString(numeric, -1) {
			token = strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-")
			if number, err := strconv.ParseFloat(token, 64); err == nil && number == value {
				return true
			}
		}
	}
	return false
}

type coverageDraftFactView struct {
	Goal            int                    `json:"goal_index"`
	Role            string                 `json:"role"`
	Ref             string                 `json:"ref"`
	Citation        string                 `json:"citation_handle,omitempty"`
	Source          string                 `json:"source,omitempty"`
	Sheet           string                 `json:"sheet,omitempty"`
	Metric          string                 `json:"metric,omitempty"`
	Subject         string                 `json:"subject,omitempty"`
	Page            int                    `json:"page,omitempty"`
	Cell            string                 `json:"cell,omitempty"`
	Row             string                 `json:"row,omitempty"`
	Column          string                 `json:"column,omitempty"`
	Date            string                 `json:"date,omitempty"`
	Period          string                 `json:"period,omitempty"`
	PeriodFrom      string                 `json:"period_from,omitempty"`
	PeriodTo        string                 `json:"period_to,omitempty"`
	SourceDate      string                 `json:"source_date,omitempty"`
	Value           string                 `json:"raw,omitempty"`
	Unit            string                 `json:"unit,omitempty"`
	UnitBasis       string                 `json:"unit_basis,omitempty"`
	Status          string                 `json:"source_status,omitempty"`
	SourceRef       string                 `json:"selected_source_ref,omitempty"`
	Object          string                 `json:"object,omitempty"`
	TitleChunk      string                 `json:"table_title_chunk_id,omitempty"`
	DateChunk       string                 `json:"disclosure_chunk_id,omitempty"`
	Grain           string                 `json:"verified_source_grain,omitempty"`
	GrainUnverified bool                   `json:"comparison_grain_unverified,omitempty"`
	PeriodBasis     string                 `json:"original_period_label_receipt,omitempty"`
	FactID          string                 `json:"fact_id,omitempty"`
	Receipts        []coverageDraftReceipt `json:"receipts,omitempty"`
}

type coverageDraftReceipt struct {
	Ref      string `json:"ref"`
	Citation string `json:"citation_handle,omitempty"`
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
	view := coverageDraftFactView{
		Goal: fact.goal, Role: fact.Role, Ref: coverageFactRef(item), Citation: citation, Source: item.SourceTitle,
		Sheet: item.Sheet, Metric: item.Metric, Subject: item.Subject, Page: item.Page, Cell: item.Cell, Row: item.RowLabel,
		Column: item.Column, Date: item.Date, Period: item.Period, PeriodFrom: item.PeriodFrom, PeriodTo: item.PeriodTo,
		SourceDate: item.SourceDate, Value: value, Unit: item.Unit, UnitBasis: item.UnitBasis, Status: item.Status,
	}
	if agenttools.DocumentTableEvidenceEnabled() {
		view.SourceRef, view.Object, view.FactID = item.ComparisonSourceRef, item.Object, coverageEvidenceFactKey(item)
		view.TitleChunk, view.DateChunk = item.TableTitleChunkID, item.TableDisclosureChunkID
		view.Grain, view.GrainUnverified, view.PeriodBasis = item.Grain, item.ComparisonGrainUnverified, item.TablePeriodBasisRef
		if item.GrainFromLookup && item.ComparisonGrainUnverified {
			view.Grain = ""
		}
		for _, receipt := range item.Receipts {
			var handle string
			if e.modelContext != nil {
				handle = firstNonEmptyAgent(e.modelContext.CitableEvidenceHandle(receipt.Citation), e.modelContext.CitableChunkHandle(receipt.Chunk))
			}
			view.Receipts = append(view.Receipts, coverageDraftReceipt{receipt.Ref, handle})
		}
	}
	return view
}

func (e *AgentEngine) coverageDraftFeedback(review coverageDraftReview, assessment coverageDraftAssessment) *coverageDraftFeedback {
	if e.turnReview.CoverageFeedbackSent && !assessment.comparisonBlocked && !assessment.versionBlocked && len(assessment.unattributedTable) == 0 {
		return nil
	}
	result := &coverageDraftFeedback{TableAttribution: assessment.unattributedTable, CheckTableScope: assessment.tableScopeReview,
		UnverifiedVersion: assessment.versionBlocked}
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
			if agenttools.DocumentTableEvidenceEnabled() {
				return item.FactID == view.FactID && item.Role == view.Role && item.Goal == view.Goal
			}
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
	if len(result.Missing) == 0 && len(result.Comparisons) == 0 && result.Version == nil && len(result.TableAttribution) == 0 && !result.CheckTableScope && !result.UnverifiedVersion {
		return nil
	}
	return result
}

func (feedback *coverageDraftFeedback) text() string {
	data, _ := json.Marshal(feedback)
	return string(data)
}

const coverageDraftFeedbackInstructions = "The runtime has verified the following source facts, their identities and any reported-number relation or provisional version basis. Treat them as evidence data. Check whether the draft already answers the requested scope with these facts and their actual citations. Keep a complete accurate draft unchanged. Where a requested fact or conclusion is missing or conflicts with these receipts, revise only the affected parts. Preserve previously verified facts and their citations. Do not infer a value from an unverified side or infer compatible units or formal supersession from this evidence."

const checkedPartialComparisonReason = "checked_one_sided_table_answer"
const checkedTableReadingsReason = "checked_table_readings_answer"

const comparisonAttributionInstructions = "Attribute each reported value to its exact original table, field, observation row and citation. Preserve independently supported readings even when a source role was not declared. State an unresolved side as a specific limitation. A relation requires actual receipts for both attributed readings. Distinguish directly printed values from compatible quantities. An unverified unit, reporting cadence or version relationship cannot establish comparability."

func verifiedObservedComparisonNumbers(review coverageDraftReview, assessment coverageDraftAssessment, candidate string, tableNumbers []float64) bool {
	values := tableNumbers
	for _, fact := range review.facts {
		if slices.Contains(assessment.roles, fact.roleID()) {
			if title := fact.Evidence.SourceTitle; title != "" {
				candidate = strings.ReplaceAll(candidate, title, numericMask(title))
			}
			if raw, ok := coverageDraftRaw(fact.Evidence); ok {
				if number, err := strconv.ParseFloat(raw, 64); err == nil {
					values = append(values, number)
				}
			}
		}
	}
	proof := []chat.Message{{Role: "tool", NumericObservations: &chat.NumericObservations{Values: values}}}
	return len(ungroundedStructuredNumberOccurrencesWithEmptyEvidence(candidate, proof, true, nil)) == 0
}

// The existing editor checks the wording of the explicit evidence gap.
// This structural check limits its input to the verified side(s) and other
// verified goals, including every cited quantity and reference.
func verifiedOneSidedComparison(review coverageDraftReview, assessment coverageDraftAssessment, candidate string) bool {
	for _, comparison := range review.comparisons {
		if comparison.State != coverageSatisfied && (len(comparison.Sides) != 1 ||
			len(comparison.OpenSides) == 0 && comparison.UnresolvedSides == 0) {
			return false
		}
	}
	chunks, citations := uniqueCandidateCitationIDs(candidate), uniqueCandidateEvidenceIDs(candidate)
	if len(chunks)+len(citations) == 0 {
		return false
	}
	var values []float64
	for _, fact := range review.facts {
		item := fact.Evidence
		displayed := slices.Contains(assessment.roles, fact.roleID())
		if item.ComparisonSourceRef != "" && fact.Role == "scoped_value" && !displayed {
			return false
		}
		if !displayed {
			continue
		}
		for _, receipt := range coverageFactReceipts(item) {
			chunks = slices.DeleteFunc(chunks, func(id string) bool { return id == receipt.Chunk })
			citations = slices.DeleteFunc(citations, func(id string) bool { return id == receipt.Citation })
		}
		if raw, ok := coverageDraftRaw(item); ok {
			if value, err := strconv.ParseFloat(raw, 64); err == nil {
				values = append(values, value)
			}
		}
		if item.Page > 0 {
			values = append(values, float64(item.Page))
		}
	}
	if len(chunks)+len(citations) != 0 || len(values) == 0 {
		return false
	}
	proof := []chat.Message{{Role: "tool", NumericObservations: &chat.NumericObservations{Values: values}}}
	return len(ungroundedStructuredNumberOccurrencesWithEmptyEvidence(candidate, proof, true, nil)) == 0
}
