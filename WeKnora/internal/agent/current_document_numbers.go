package agent

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Source qualification precedes token parsing. Neither history messages,
// inspection catalogs, failed calculations nor metadata supply document text.
// Conflicting versions/bodies cannot lend numbers to one another. A projected
// view contributes only its actual returned text, never its unseen origin.
func currentReadDocumentNumbers(steps []types.AgentStep) (values, percentages []float64) {
	state := &types.AgentState{RoundSteps: steps}
	rows, conflicts := currentCitationReferenceRows(state)
	views := currentCitationSourceViews(state)
	seen, seenPercent := map[float64]bool{}, map[float64]bool{}
	for id, observed := range views {
		if rows[id] == nil || conflicts[id] {
			continue
		}
		for _, view := range observed {
			if status, _ := view.data["status"].(string); status != "" && status != "ok" {
				continue
			}
			if _, known := citationViewIdentity(view); !known {
				continue
			}
			text := documentNumericProse(citationString(view.row["content"]))
			for _, span := range proseNumber.FindAllStringIndex(text, -1) {
				token := text[span[0]:span[1]]
				value, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-"), 64)
				if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
					continue
				}
				if !seen[value] {
					values = append(values, value)
					seen[value] = true
				}
				if (strings.HasPrefix(text[span[1]:], "%") || strings.HasPrefix(text[span[1]:], "％")) && !seenPercent[value] {
					percentages = append(percentages, value)
					seenPercent[value] = true
				}
			}
		}
	}
	sort.Float64s(values)
	sort.Float64s(percentages)
	return values, percentages
}
