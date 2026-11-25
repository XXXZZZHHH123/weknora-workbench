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
	collect := func(content string) {
		text := documentNumericProse(content)
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
			collect(citationString(view.row["content"]))
		}
	}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.ExecutionName() != "read_document" || call.Result == nil || !call.Result.Success || call.Result.Data[types.DocumentImageTextEvidenceKey] == nil {
				continue
			}
			data := reviewAsMap(call.Result.Data)
			annotations, _ := data[types.DocumentImageTextEvidenceKey].([]any)
			for _, raw := range annotations {
				annotation, _ := raw.(map[string]any)
				ownerID := citationString(annotation["chunk_id"])
				parentID := citationString(annotation["parent_chunk_id"])
				text := citationString(annotation["text"])
				// A cut within a line can change a numeric token. A retained
				// newline marks a complete row boundary in a truncated OCR view.
				completeRows := annotation["text_truncated"] != true || strings.HasSuffix(text, "\n")
				if annotation["kind"] == string(types.ChunkTypeImageOCR) && completeRows && rows[ownerID] != nil && !conflicts[ownerID] &&
					rows[parentID] != nil && !conflicts[parentID] && reviewImageTextBoundToSource(data, annotation) {
					collect(text)
				}
			}
		}
	}
	sort.Float64s(values)
	sort.Float64s(percentages)
	return values, percentages
}
