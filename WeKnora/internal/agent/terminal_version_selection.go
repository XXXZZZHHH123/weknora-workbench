package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// The selected source and its basis come from the model's declaration. Both
// receipts must still be current, and both must report comparable values.
type terminalVersionChoice struct {
	selected, basis, reason string
}

func coverageVersionBasis(source coverageEvidence, reason string) bool {
	if reason != strings.TrimSpace(reason) || reason == "" || utf8.RuneCountInString(reason) > 160 {
		return false
	}
	body, _ := source.Raw.(string)
	return strings.Contains(source.SourceTitle, reason) || strings.Contains(body, reason)
}

func currentTerminalVersionChoice(record coverageRecord, facts []terminalCoverageFact) terminalVersionChoice {
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
		choice := terminalVersionChoice{selected.SourceTitle, alternative.SourceTitle, binding.VersionBasis}
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
	left, leftNumeric := terminalComparisonNumber(a)
	right, rightNumeric := terminalComparisonNumber(b)
	return leftNumeric && rightNumeric && left.Cmp(right) != 0
}

func (choice terminalVersionChoice) render(chinese bool) string {
	if choice.selected == "" {
		return ""
	}
	delivery := terminalCoverageDelivery{chinese: chinese}
	return fmt.Sprintf(delivery.text("补充披露暂采用《%s》。版本选择依据是《%s》中的“%s”。",
		"The supplemental disclosure uses “%s” provisionally. The basis for that version choice is “%s”, which contains “%s”."),
		terminalEvidenceText(choice.selected), terminalEvidenceText(choice.basis), terminalEvidenceText(choice.reason))
}
