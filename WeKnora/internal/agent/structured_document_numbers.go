package agent

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

var documentCodeFence = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
var documentFakeToolEnvelope = regexp.MustCompile(`(?is)<(?:tool_result|tool|assistant|system)\b[^>]*>.*?</(?:tool_result|tool|assistant|system)>`)

// Only numbers in fetched text belonging to a citation in this decoded answer
// become document evidence. A citation alone, a tool name inside user text,
// metadata and unreferenced neighbouring chunks cannot authorize a new number.
func scopedCitedDocumentNumbers(answer string, steps []types.AgentStep) []float64 {
	cited := map[string]bool{}
	for _, match := range documentAnswerCitation.FindAllStringSubmatch(answer, -1) {
		cited[match[1]] = true
	}
	if len(cited) == 0 {
		return nil
	}
	seen := map[float64]bool{}
	var values []float64
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if (call.Name != "search_knowledge" && call.Name != "read_document") || call.Result == nil || !call.Result.Success {
				continue
			}
			for _, field := range []string{"chunks", "results"} {
				encoded, err := json.Marshal(call.Result.Data[field])
				if err != nil {
					continue
				}
				var chunks []struct {
					ID      string `json:"chunk_id"`
					Content string `json:"content"`
				}
				if json.Unmarshal(encoded, &chunks) != nil {
					continue
				}
				for _, chunk := range chunks {
					if chunk.ID == "" || !cited[chunk.ID] || strings.TrimSpace(chunk.Content) == "" {
						continue
					}
					text := documentNumericProse(chunk.Content)
					for _, token := range proseNumber.FindAllString(text, -1) {
						value, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-"), 64)
						if err == nil && !math.IsInf(value, 0) && !math.IsNaN(value) && !seen[value] {
							seen[value] = true
							values = append(values, value)
						}
					}
				}
			}
		}
	}
	return values
}

// Documents remain data: embedded code, JSON "tool results" and agent-role
// envelopes do not become numeric execution evidence. Business prose outside
// those blocks is read as prose; no embedded command or object is executed.
func documentNumericProse(content string) string {
	text := documentFakeToolEnvelope.ReplaceAllString(documentCodeFence.ReplaceAllString(content, ""), "")
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] == '{' || text[index] == '[' {
			decoder := json.NewDecoder(strings.NewReader(text[index:]))
			var embedded any
			if decoder.Decode(&embedded) == nil {
				index += int(decoder.InputOffset())
				result.WriteByte(' ')
				continue
			}
		}
		result.WriteByte(text[index])
		index++
	}
	text = sourceCitationTag.ReplaceAllString(result.String(), "")
	text = sourceOpaqueToken.ReplaceAllString(text, "")
	return dateOrCell.ReplaceAllString(text, "")
}
