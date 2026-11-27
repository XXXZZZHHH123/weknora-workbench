package agent

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
)

const incompleteVersionEvidence = "version_evidence_attribution_incomplete"

// The selected source and its basis come from the model's declaration. Both
// receipts must still be current, and both must report comparable values.
type terminalVersionChoice struct {
	SelectedSource              string `json:"selected_source"`
	AlternativeSource           string `json:"alternative_source"`
	Basis                       string `json:"basis_quote"`
	SelectedReceipt             string `json:"selected_receipt,omitempty"`
	AlternativeReceipt          string `json:"alternative_receipt,omitempty"`
	BasisReceipt                string `json:"basis_receipt,omitempty"`
	UserSelected                bool   `json:"user_selected,omitempty"`
	basisCitation, basisChunk   string
	selectedRef, alternativeRef string
	claim                       coverageVersionClaim
}

type coverageVersionClaim struct {
	goal                  int
	selected, alternative string
}

func coverageActualVersionBody(source coverageEvidence) bool {
	body, _ := source.Raw.(string)
	return source.Operation == agenttools.ToolReadDocument && source.Kind == "body" && source.Bound &&
		source.Complete && body != "" && source.SnapshotHash != "" && source.ContentHash == candidateTextDigest(body)
}

func coverageVersionBodyReadCurrent(record coverageRecord, fact coverageEvidence) bool {
	return slices.ContainsFunc(record.Evidence, func(source coverageEvidence) bool {
		return coverageObservationRef(source) == coverageObservationRef(fact) &&
			comparisonPhysicalSource(source) == comparisonPhysicalSource(fact) && source.SnapshotHash == fact.SnapshotHash &&
			coverageCurrentEvidence(record, source) && coverageActualVersionBody(source)
	})
}

func coverageVersionBasis(source coverageEvidence, reason string, strict bool) bool {
	if reason != strings.TrimSpace(reason) || reason == "" || utf8.RuneCountInString(reason) > 160 {
		return false
	}
	body, _ := source.Raw.(string)
	if strict {
		return coverageActualVersionBody(source) && strings.ContainsFunc(reason, unicode.IsLetter) && strings.Contains(body, reason)
	}
	return strings.Contains(source.SourceTitle, reason) || strings.Contains(body, reason)
}

func versionBasisRepeatedInPeer(record coverageRecord, basis coverageEvidence, other [2]string, reason string) bool {
	return slices.ContainsFunc(record.Evidence, func(source coverageEvidence) bool {
		return comparisonPhysicalSource(source) == other && comparisonPhysicalSource(basis) != other &&
			coverageCurrentEvidence(record, source) && coverageVersionBasis(source, reason, true)
	})
}

func currentTerminalVersionChoice(record coverageRecord, facts []coverageFact) terminalVersionChoice {
	var result terminalVersionChoice
	strict := agenttools.DocumentTableEvidenceEnabled()
	for _, binding := range record.Bindings {
		if binding.VersionBasisRef == "" || binding.Role != "supplement" && (!strict || binding.Role != "primary") || strict && binding.Provisional {
			continue
		}
		alternativeRef := firstNonEmptyAgent(binding.VersionAlternativeRef, binding.VersionBasisRef)
		var selected, alternative, basis coverageEvidence
		for _, fact := range facts {
			item := fact.Evidence
			hasRole := binding.Role == "primary" && fact.Role == "scoped_value" ||
				binding.Role == "supplement" && (fact.Role == "supplement" || fact.Role == "supplement_context")
			if item.Kind != "body" || !hasRole || !coverageCurrentEvidence(record, item) {
				continue
			}
			if strict && !coverageVersionBodyReadCurrent(record, item) {
				continue
			}
			if item.EvidenceID == binding.EvidenceID && item.JSONPointer == binding.JSONPointer &&
				coverageSameSource(binding, item) && coverageLocation(binding, item) {
				selected = item
			}
			if coverageObservationRef(item) == alternativeRef && slices.ContainsFunc(record.Bindings, func(peer coverageBinding) bool {
				return peer.GoalIndex == binding.GoalIndex && peer.Role == binding.Role &&
					(!strict || !peer.Provisional) &&
					peer.EvidenceID == item.EvidenceID && peer.JSONPointer == item.JSONPointer &&
					coverageSameSource(peer, item) && coverageLocation(peer, item)
			}) {
				alternative = item
			}
		}
		if !terminalComparableVersions(selected, alternative) {
			continue
		}
		for _, source := range record.Evidence {
			if coverageObservationRef(source) == binding.VersionBasisRef && coverageCurrentEvidence(record, source) {
				basis = source
				break
			}
		}
		validBasis := coverageVersionBasis(basis, binding.VersionBasis, strict)
		if strict && validBasis {
			counterpart := comparisonPhysicalSource(alternative)
			if comparisonPhysicalSource(basis) == counterpart {
				counterpart = comparisonPhysicalSource(selected)
			}
			validBasis = !versionBasisRepeatedInPeer(record, basis, counterpart, binding.VersionBasis)
		}
		if strict && binding.VersionUserSelection {
			validBasis = coverageActualVersionBody(basis) && comparisonPhysicalSource(basis) == comparisonPhysicalSource(selected)
		}
		if !validBasis || strict &&
			comparisonPhysicalSource(basis) != comparisonPhysicalSource(selected) &&
			comparisonPhysicalSource(basis) != comparisonPhysicalSource(alternative) {
			continue
		}
		choice := terminalVersionChoice{SelectedSource: selected.SourceTitle, AlternativeSource: alternative.SourceTitle,
			Basis: binding.VersionBasis, selectedRef: coverageDraftFactIdentity(selected), alternativeRef: coverageDraftFactIdentity(alternative)}
		if strict {
			choice.SelectedReceipt, choice.AlternativeReceipt, choice.BasisReceipt = coverageFactRef(selected), coverageFactRef(alternative), coverageFactRef(basis)
			choice.UserSelected, choice.basisCitation, choice.basisChunk = binding.VersionUserSelection, basis.CitationID, basis.ChunkID
			choice.claim = coverageVersionClaim{binding.GoalIndex, coverageObservationRef(selected), coverageObservationRef(alternative)}
		}
		if result != (terminalVersionChoice{}) && result != choice {
			return terminalVersionChoice{}
		}
		result = choice
	}
	return result
}

func versionChoiceCited(candidate string, choice terminalVersionChoice, facts []coverageDraftFact) bool {
	var chosen, alternative *coverageDraftFact
	for index := range facts {
		fact := &facts[index]
		if fact.goal != choice.claim.goal {
			continue
		}
		switch coverageDraftFactIdentity(fact.Evidence) {
		case choice.selectedRef:
			chosen = fact
		case choice.alternativeRef:
			alternative = fact
		}
	}
	if chosen == nil || alternative == nil {
		return false
	}
	var displaysChosen, displaysAlternative bool
	displaysBasis := choice.UserSelected
	start := 0
	for _, location := range sourceCitationTag.FindAllStringIndex(candidate, -1) {
		assertion := newCoverageDraftParagraph(candidate[start:location[1]])
		start = location[1]
		for _, component := range []struct {
			fact      *coverageDraftFact
			displayed *bool
		}{{chosen, &displaysChosen}, {alternative, &displaysAlternative}} {
			item := component.fact.Evidence
			for _, receipt := range coverageFactReceipts(item) {
				if versionReceiptCited(assertion, receipt.Citation, receipt.Chunk) {
					locator := item
					locator.CitationID, locator.ChunkID = receipt.Citation, receipt.Chunk
					locator.Date = "" // The surrounding checked role already verifies the observation date.
					*component.displayed = *component.displayed || assertion.displays(locator, component.fact.Role, true)
				}
			}
		}
		displaysBasis = displaysBasis || versionReceiptCited(assertion, choice.basisCitation, choice.basisChunk) && assertion.hasLabel(choice.Basis)
	}
	return displaysChosen && displaysAlternative && displaysBasis
}

func versionReceiptCited(assertion coverageDraftParagraph, citation, chunk string) bool {
	return citation != "" && slices.Contains(assertion.results, citation) ||
		chunk != "" && slices.Contains(assertion.chunks, chunk)
}

func terminalComparableVersions(a, b coverageEvidence) bool {
	date := firstNonEmptyAgent(a.Date, a.SourceDate)
	if a.SourceID == "" || b.SourceID == "" || a.SourceID == b.SourceID || a.SourceTitle == "" || b.SourceTitle == "" ||
		a.Metric == "" || a.Metric != b.Metric || a.Subject != b.Subject || !terminalUnitFromSource(a) || !terminalUnitFromSource(b) || a.Unit != b.Unit ||
		date == "" || date != firstNonEmptyAgent(b.Date, b.SourceDate) || a.Grain != b.Grain || a.Period != b.Period ||
		a.PeriodFrom != b.PeriodFrom || a.PeriodTo != b.PeriodTo {
		return false
	}
	if agenttools.DocumentTableEvidenceEnabled() && (comparisonPhysicalSource(a) == comparisonPhysicalSource(b) ||
		a.Sheet != b.Sheet || a.Column != b.Column || a.SnapshotHash == "" || b.SnapshotHash == "") {
		return false
	}
	left, leftNumeric := coverageComparisonNumber(a)
	right, rightNumeric := coverageComparisonNumber(b)
	return leftNumeric && rightNumeric && left.Cmp(right) != 0
}
