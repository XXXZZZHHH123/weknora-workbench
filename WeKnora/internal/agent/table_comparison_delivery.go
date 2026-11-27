package agent

import (
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
)

type unattributedTableValue struct {
	Source string `json:"source"`
	Page   int    `json:"page,omitempty"`
}

type coverageComparisonCheck struct {
	GoalIndex       int                     `json:"goal_index"`
	Table           string                  `json:"table"`
	Result          string                  `json:"reported_numbers,omitempty"`
	SameUnit        bool                    `json:"same_verified_unit"`
	OpenSides       []string                `json:"unverified_sides,omitempty"`
	Sides           []coverageDraftFactView `json:"verified_sides,omitempty"`
	State           string                  `json:"state,omitempty"`
	Reason          string                  `json:"reason,omitempty"`
	UnresolvedSides int                     `json:"unresolved_sides,omitempty"`
	UnitStatus      string                  `json:"unit_status,omitempty"`
}

// The relation describes the printed values. A comparison of quantities
// requires compatible, independently verified units.
func checkedTableComparison(target taskCoverageTarget, result coverageCompletion) coverageComparisonCheck {
	check := coverageComparisonCheck{GoalIndex: target.GoalIndex, Table: target.Table, OpenSides: result.OpenItems}
	if agenttools.DocumentTableEvidenceEnabled() {
		check.State, check.Reason, check.UnresolvedSides = result.State, result.Reason, result.UnresolvedSides
		if len(result.PinnedFacts) >= 2 {
			check.UnitStatus = comparisonUnitStatus(result.PinnedFacts[0].Evidence, result.PinnedFacts[1].Evidence)
		}
	}
	if result.State != coverageSatisfied || len(result.PinnedFacts) < 2 {
		return check
	}
	left, right := result.PinnedFacts[0].Evidence, result.PinnedFacts[1].Evidence
	a, aOK := coverageComparisonNumber(left)
	b, bOK := coverageComparisonNumber(right)
	if aOK && bOK {
		check.Result = "different"
		if a.Cmp(b) == 0 {
			check.Result = "equal"
		}
	}
	check.SameUnit = terminalUnitFromSource(left) && terminalUnitFromSource(right) && left.Unit == right.Unit
	return check
}

func comparisonUnitStatus(left, right coverageEvidence) string {
	if !terminalUnitFromSource(left) || !terminalUnitFromSource(right) {
		return "unknown"
	}
	if left.Unit != right.Unit {
		return "incompatible"
	}
	return "same"
}

func coverageComparisonNumber(item coverageEvidence) (*big.Rat, bool) {
	value := item.ReportedValue
	if item.Kind != "body" {
		switch raw := item.Raw.(type) {
		case json.Number:
			value = raw.String()
		case string:
			value = raw
		}
	}
	if token := proseNumber.FindString(value); token == "" || token != value {
		return nil, false
	}
	if agenttools.DocumentTableEvidenceEnabled() && documentTableNumber.MatchString(value) {
		value = strings.ReplaceAll(strings.ReplaceAll(value, ",", ""), "−", "-")
	}
	return new(big.Rat).SetString(value)
}

// Candidate citations identify each claimed operand's actual source. Calls
// present in the turn alone cannot open this check or supply an attribution.
func multiSourceTableAttribution(review coverageDraftReview, candidate string) ([]unattributedTableValue, bool) {
	if !agenttools.DocumentTableEvidenceEnabled() {
		return nil, false
	}
	var visible []coverageDraftParagraph
	for _, paragraph := range strings.Split(strings.ReplaceAll(candidate, "\r\n", "\n"), "\n\n") {
		view := newCoverageDraftParagraph(paragraph)
		if len(view.chunks)+len(view.results) != 0 {
			visible = append(visible, view)
		}
	}
	sources := make(map[[2]string]bool)
	var missing []unattributedTableValue
	anyTable := false
	for _, item := range review.record.Evidence {
		source := comparisonPhysicalSource(item)
		switch {
		case item.Kind == "cell" || item.Kind == "derived" || item.Kind == "last_valid":
			raw, _ := coverageDraftRaw(item)
			value, err := strconv.ParseFloat(raw, 64)
			if item.CitationID != "" && err == nil && slices.ContainsFunc(visible, func(paragraph coverageDraftParagraph) bool {
				return slices.Contains(paragraph.results, item.CitationID) && coverageDraftAmountOccurrences(paragraph.text, value) > 0
			}) {
				sources[source] = true
			}
		case item.Kind == "body" && item.Operation == agenttools.ToolReadDocument && item.ChunkID != "":
			for _, paragraph := range visible {
				if !slices.Contains(paragraph.chunks, item.ChunkID) {
					continue
				}
				amounts := displayedTableAmounts(paragraph, item)
				if len(amounts) == 0 {
					continue
				}
				sources[source], anyTable = true, true
				for _, assertion := range coverageDraftAssertionBoundary.Split(paragraph.text, -1) {
					local := coverageDraftParagraph{assertion, strings.ToLower(assertion), paragraph.chunks, paragraph.results}
					for _, amount := range amounts {
						count := coverageDraftAmountOccurrences(maskCitedPageLocator(local.text, item.Page), amount)
						if count > 0 && count > nativeTableAttributions(local, paragraph, item, review, amount) {
							page := unattributedTableValue{item.SourceTitle, item.Page}
							if !slices.Contains(missing, page) {
								missing = append(missing, page)
							}
						}
					}
				}
			}
		}
	}
	if len(sources) < 2 || !anyTable {
		return nil, false
	}
	return missing, true
}

func unattributedMultiSourceTableValues(review coverageDraftReview, candidate string) []unattributedTableValue {
	missing, _ := multiSourceTableAttribution(review, candidate)
	return missing
}

func displayedTableAmounts(paragraph coverageDraftParagraph, item coverageEvidence) []float64 {
	body, _ := item.Raw.(string)
	quantities := maskCitedPageLocator(paragraph.text, item.Page)
	var amounts []float64
	observe := func(cell string) {
		if value, ok := documentTableAmount(cell); ok && !slices.Contains(amounts, value) &&
			coverageDraftAmountOccurrences(quantities, value) > 0 {
			amounts = append(amounts, value)
		}
	}
	grids := agenttools.ParseDocumentTableGrids(body)
	for _, grid := range grids {
		for _, row := range grid.Rows[1:] {
			if len(row) > 0 {
				for _, cell := range row[1:] {
					observe(cell)
				}
			}
		}
	}
	if len(grids) != 0 {
		return amounts
	}
	// Historical fragments can flatten table rows into pipe-delimited
	// fields. Recognize the amount as a candidate, never as a proven cell.
	for _, line := range strings.Split(body, "\n") {
		if strings.Count(line, "|") >= 4 {
			for _, cell := range strings.Split(line, "|") {
				observe(strings.TrimSpace(cell))
			}
		}
	}
	return amounts
}

var citedDocumentPage = regexp.MustCompile(`(?i)第\s*([0-9]+)\s*页|\b(?:page|slide)\s+([0-9]+)\b`)

func maskCitedPageLocator(text string, actualPage int) string {
	if actualPage == 0 {
		return text
	}
	return citedDocumentPage.ReplaceAllStringFunc(text, func(locator string) string {
		match := citedDocumentPage.FindStringSubmatch(locator)
		page, err := strconv.Atoi(firstNonEmptyAgent(match[1], match[2]))
		if err == nil && page == actualPage {
			return numericMask(locator)
		}
		return locator
	})
}

func documentTableAmount(literal string) (float64, bool) {
	if !documentTableNumber.MatchString(literal) {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(literal, ",", ""), "−", "-"), 64)
	return value, err == nil
}

func nativeTableAttributions(local, paragraph coverageDraftParagraph, item coverageEvidence, review coverageDraftReview, amount float64) int {
	coordinates := make(map[[4]string]bool)
	for _, fact := range review.facts {
		verified := fact.Evidence
		value, ok := documentTableAmount(verified.ReportedValue)
		if ok && value == amount && verified.ChunkID == item.ChunkID && local.hasField(verified) &&
			paragraph.displaysVerifiedRole(fact, review.facts) {
			coordinates[[4]string{verified.Object, verified.RowLabel, verified.Column, verified.Cell}] = true
		}
	}
	if len(coordinates) > 0 {
		return len(coordinates)
	}
	for _, native := range review.record.Evidence {
		if native.ChunkID != item.ChunkID || native.ToolCallID != item.ToolCallID || native.TableGrid == nil ||
			!paragraph.hasLabel(native.Sheet) {
			continue
		}
		body, _ := native.Raw.(string)
		grids := agenttools.ParseDocumentTableGrids(body)
		if *native.TableGrid < 0 || *native.TableGrid >= len(grids) || !grids[*native.TableGrid].ParseComplete {
			continue
		}
		grid := grids[*native.TableGrid]
		header := grid.Rows[0]
		for ordinal, row := range grid.Rows[1:] {
			if len(row) != len(header) || !paragraph.hasLabel(row[0]) || slices.ContainsFunc(grid.Rows[ordinal+2:], func(other []string) bool {
				return len(other) > 0 && other[0] == row[0]
			}) || slices.ContainsFunc(grid.Rows[1:ordinal+1], func(other []string) bool {
				return len(other) > 0 && other[0] == row[0]
			}) {
				continue
			}
			for column, field := range header[1:] {
				value, ok := documentTableAmount(row[column+1])
				if ok && value == amount && local.hasLabel(field) &&
					!slices.Contains(header[column+2:], field) && !slices.Contains(header[1:column+1], field) {
					coordinates[[4]string{native.Object, row[0], field, strconv.Itoa(column)}] = true
				}
			}
		}
	}
	return len(coordinates)
}
