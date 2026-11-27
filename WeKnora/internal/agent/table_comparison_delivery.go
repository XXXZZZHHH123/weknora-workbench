package agent

import (
	"encoding/json"
	"math/big"
)

type coverageComparisonCheck struct {
	GoalIndex int                     `json:"goal_index"`
	Table     string                  `json:"table"`
	Result    string                  `json:"reported_numbers,omitempty"`
	SameUnit  bool                    `json:"same_verified_unit"`
	OpenSides []string                `json:"unverified_sides,omitempty"`
	Sides     []coverageDraftFactView `json:"verified_sides,omitempty"`
}

// The relation describes the printed values. A comparison of quantities
// requires compatible, independently verified units.
func checkedTableComparison(target taskCoverageTarget, result coverageCompletion) coverageComparisonCheck {
	check := coverageComparisonCheck{GoalIndex: target.GoalIndex, Table: target.Table, OpenSides: result.OpenItems}
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
	return new(big.Rat).SetString(value)
}
