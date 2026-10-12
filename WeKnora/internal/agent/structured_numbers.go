package agent

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

// Numeric checking is independent of question vocabulary and supported analyses.
// It verifies traceability, not the semantic appropriateness of a statistic.
var evidenceDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var proseNumber = regexp.MustCompile(`[-+−]?\d+(?:,\d{3})*(?:\.\d+)?`)
var dateOrCell = regexp.MustCompile(`\b\d{1,2}[-/]\d{1,2}\b|\d{4}[-/]\d{1,2}(?:[-/]\d{1,2})?|\d{2,4}年(?:\d{1,2}月(?:\d{1,2}日)?)?|\d{1,2}月(?:\d{1,2}日)?|\b[A-Z]{1,3}\d+\b|(?m)^\s*(?:[#>*-]|\*\*)*\s*\d+[.)）、]`)
var sourceCitationTag = regexp.MustCompile(`<(?:kb|web)\b[^>]*>`)

func ungroundedStructuredNumbers(answer string, messages []chat.Message) []string {
	values := []float64{}
	filenames := []string{}
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case float64:
			values = append(values, x)
		case map[string]any:
			for key, item := range x {
				if key == "file" || key == "source_file" {
					if file, ok := item.(string); ok {
						filenames = append(filenames, file)
					}
				}
				collect(item)
			}
		case []any:
			for _, item := range x {
				collect(item)
			}
		case string:
			if evidenceDate.MatchString(x) {
				for _, part := range strings.Split(x, "-") {
					if v, e := strconv.ParseFloat(part, 64); e == nil {
						values = append(values, v)
					}
				}
			}
			// Decimal calculator results arrive as numeric strings. SQL, dates,
			// catalogue IDs and natural-language model explanations are not numbers.
			if n, err := strconv.ParseFloat(x, 64); err == nil {
				values = append(values, n)
			}
			if strings.HasPrefix(strings.TrimSpace(x), "{") {
				var d map[string]any
				if json.Unmarshal([]byte(x), &d) == nil {
					collect(d)
				}
			}
		}
	}
	for _, msg := range messages {
		if msg.Role == "tool" {
			start := strings.Index(msg.Content, "{")
			if start < 0 {
				continue
			}
			var data map[string]any
			if json.NewDecoder(strings.NewReader(msg.Content[start:])).Decode(&data) == nil {
				collect(data)
			}
		}
		if msg.Role == "user" {
			for _, n := range proseNumber.FindAllString(msg.Content, -1) {
				if v, e := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64); e == nil {
					values = append(values, v)
				}
			}
		}
	}
	if len(values) == 0 {
		return nil
	} // No numeric evidence; ordinary conversation.
	for _, file := range filenames {
		answer = strings.ReplaceAll(answer, file, "")
	}
	// Citation attributes contain opaque chunk/KB IDs and source URLs, not
	// numeric claims. Keep checking every number in the surrounding prose.
	text := dateOrCell.ReplaceAllString(sourceCitationTag.ReplaceAllString(answer, ""), "")
	unsupported := []string{}
	seen := map[string]bool{}
	for _, span := range proseNumber.FindAllStringIndex(text, -1) {
		token := text[span[0]:span[1]]
		percent := strings.HasPrefix(text[span[1]:], "%") || strings.HasPrefix(text[span[1]:], "％")
		normalized := strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-")
		number, err := strconv.ParseFloat(normalized, 64)
		if err != nil {
			continue
		}
		precision := 0
		if dot := strings.Index(normalized, "."); dot >= 0 {
			precision = len(normalized) - dot - 1
		}
		// Accept ordinary display rounding, including percent/absolute-delta format.
		tolerance := 0.500001 * math.Pow10(-precision)
		supported := false
		for _, v := range values {
			if math.Abs(math.Abs(number)-math.Abs(v)) <= tolerance || (percent && math.Abs(math.Abs(number)-math.Abs(v*100)) <= tolerance) {
				supported = true
				break
			}
		}
		if !supported && !seen[token] {
			unsupported = append(unsupported, token)
			seen[token] = true
		}
	}
	return unsupported
}

func hasStructuredHistoryEvidence(messages []chat.Message) bool {
	for _, msg := range messages {
		if msg.Role == "tool" && structuredResultHasEvidence(msg.Content) {
			return true
		}
	}
	return false
}
