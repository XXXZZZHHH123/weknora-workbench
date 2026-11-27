package agent

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// The selected source and its basis come from the model's declaration. Both
// receipts must still be current, and both must report comparable values.
type terminalVersionChoice struct {
	SelectedSource              string `json:"selected_source"`
	AlternativeSource           string `json:"alternative_source"`
	Basis                       string `json:"basis_quote"`
	selectedRef, alternativeRef string
}

func coverageVersionBasis(source coverageEvidence, reason string) bool {
	if reason != strings.TrimSpace(reason) || reason == "" || utf8.RuneCountInString(reason) > 160 {
		return false
	}
	body, _ := source.Raw.(string)
	return strings.Contains(source.SourceTitle, reason) || strings.Contains(body, reason)
}

func currentTerminalVersionChoice(record coverageRecord, facts []coverageFact) terminalVersionChoice {
	var result terminalVersionChoice
	for _, binding := range record.Bindings {
		if binding.VersionBasisRef == "" || binding.Role != "supplement" {
			continue
		}
		var selected, alternative, basis coverageEvidence
		for _, fact := range facts {
			item := fact.Evidence
			if item.Kind != "body" || (fact.Role != "supplement" && fact.Role != "supplement_context") || !coverageCurrentEvidence(record, item) {
				continue
			}
			if item.EvidenceID == binding.EvidenceID && item.JSONPointer == binding.JSONPointer &&
				coverageSameSource(binding, item) && coverageLocation(binding, item) {
				selected = item
			}
			if coverageObservationRef(item) == binding.VersionBasisRef && slices.ContainsFunc(record.Bindings, func(peer coverageBinding) bool {
				return peer.GoalIndex == binding.GoalIndex && peer.Role == "supplement" &&
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
		if !coverageVersionBasis(basis, binding.VersionBasis) {
			continue
		}
		choice := terminalVersionChoice{selected.SourceTitle, alternative.SourceTitle, binding.VersionBasis, coverageDraftFactIdentity(selected), coverageDraftFactIdentity(alternative)}
		if result != (terminalVersionChoice{}) && result != choice {
			return terminalVersionChoice{}
		}
		result = choice
	}
	return result
}

func terminalComparableVersions(a, b coverageEvidence) bool {
	date := firstNonEmptyAgent(a.Date, a.SourceDate)
	if a.SourceID == "" || b.SourceID == "" || a.SourceID == b.SourceID || a.SourceTitle == "" || b.SourceTitle == "" ||
		a.Metric == "" || a.Metric != b.Metric || a.Subject != b.Subject || !terminalUnitFromSource(a) || !terminalUnitFromSource(b) || a.Unit != b.Unit ||
		date == "" || date != firstNonEmptyAgent(b.Date, b.SourceDate) || a.Grain != b.Grain || a.Period != b.Period ||
		a.PeriodFrom != b.PeriodFrom || a.PeriodTo != b.PeriodTo {
		return false
	}
	left, leftNumeric := coverageComparisonNumber(a)
	right, rightNumeric := coverageComparisonNumber(b)
	return leftNumeric && rightNumeric && left.Cmp(right) != 0
}
