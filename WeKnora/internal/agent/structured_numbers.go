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
var sourceDigestLabel = regexp.MustCompile(`(?i)\bSHA-?256\b`)
var sourceOpaqueToken = regexp.MustCompile(`\b(?:[a-fA-F0-9]{64}|[a-fA-F0-9]{24}|[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})\b`)
var abbreviatedSourceDigest = regexp.MustCompile(`\b([a-fA-F0-9]{8,32})(?:…|\.\.\.)([a-fA-F0-9]{4,16})\b`)
var sourceSeriesID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{2,159}$`)
var sourceCellRow = regexp.MustCompile(`^[A-Z]{1,3}([1-9][0-9]{0,6})$`)

func opaqueSourceID(value string) bool {
	if !sourceOpaqueToken.MatchString(value) || sourceOpaqueToken.FindString(value) != value {
		return false
	}
	letter, digit := false, false
	for _, c := range value {
		letter = letter || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		digit = digit || c >= '0' && c <= '9'
	}
	return letter && digit
}

// Only a real source-inspection cell proves a source row. SQL row counters and
// user/assistant prose must never exempt a bare number from numeric checking.
func inspectedSourceRows(data map[string]any, rows map[string]bool) {
	if data["kind"] != "structured_source_inspection" || data["status"] != "ok" || data["hash_verified"] != true {
		return
	}
	add := func(value any) {
		cell, ok := value.(string)
		if !ok {
			return
		}
		match := sourceCellRow.FindStringSubmatch(cell)
		if len(match) == 2 {
			row, err := strconv.Atoi(match[1])
			if err == nil && row <= 1048576 {
				rows[match[1]] = true
			}
		}
	}
	for _, group := range []string{"ranges", "matches", "calculations"} {
		entries, _ := data[group].([]any)
		for _, entry := range entries {
			block, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if group == "matches" {
				add(block["cell"])
			}
			for _, key := range []string{"cells", "context_cells"} {
				cells, _ := block[key].([]any)
				for _, value := range cells {
					switch cell := value.(type) {
					case map[string]any:
						add(cell["cell"])
					case []any: // lossless compact model view
						if len(cell) >= 2 {
							add(cell[0])
						}
					}
				}
			}
		}
	}
}

func maskInspectedMarkdownRows(answer string, rows map[string]bool) string {
	lines := strings.Split(answer, "\n")
	column := -1
	for index, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			column = -1
			continue
		}
		cells := strings.Split(line, "|")
		for i, cell := range cells {
			label := strings.ToLower(strings.Trim(strings.TrimSpace(cell), "*`"))
			if label == "行" || label == "行号" || label == "原行号" || label == "excel行号" || label == "row" || label == "row number" {
				column = i
			}
		}
		if column >= 0 && column < len(cells) {
			row := strings.Trim(strings.TrimSpace(cells[column]), "*`")
			if rows[row] {
				cells[column] = " "
				lines[index] = strings.Join(cells, "|")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func ungroundedStructuredNumbers(answer string, messages []chat.Message) []string {
	values := []float64{}
	filenames := []string{}
	identifiers := map[string]bool{}
	digests := map[string]bool{}
	inspectedRows := map[string]bool{}
	hasDigest := false
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case float64:
			values = append(values, x)
		case map[string]any:
			for key, item := range x {
				if key == "id" || strings.HasSuffix(key, "_id") || key == "sha256" {
					id, _ := item.(string)
					if opaqueSourceID(id) || key == "series_id" && sourceSeriesID.MatchString(id) {
						identifiers[id] = true
						if key == "sha256" && len(id) == 64 {
							hasDigest = true
							digests[id] = true
						}
					}
					// Even a numeric-looking identifier is metadata, never a statistic.
					continue
				}
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
			// Fetched KB prose uses only separately verified cited chunk evidence;
			// embedded JSON in a document is not an executed aggregate.
			if msg.Name == "search_knowledge" || msg.Name == "read_document" {
				continue
			}
			start := strings.Index(msg.Content, "{")
			if start < 0 {
				continue
			}
			var data map[string]any
			if json.NewDecoder(strings.NewReader(msg.Content[start:])).Decode(&data) == nil {
				if data["status"] == "error" {
					continue
				}
				inspectedSourceRows(data, inspectedRows)
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
	// Exact known tokens are provenance, not numbers. Unknown hashes remain
	// visible to the checker; removing a known prefix must not hide a forged ID.
	answer = sourceOpaqueToken.ReplaceAllStringFunc(answer, func(id string) string {
		if identifiers[id] {
			return ""
		}
		return id
	})
	// A displayed digest abbreviation is metadata only when both ends uniquely
	// match one full digest actually returned by the source service.
	answer = abbreviatedSourceDigest.ReplaceAllStringFunc(answer, func(token string) string {
		parts := abbreviatedSourceDigest.FindStringSubmatch(token)
		matches := 0
		for id := range digests {
			if strings.HasPrefix(id, parts[1]) && strings.HasSuffix(id, parts[2]) {
				matches++
			}
		}
		if matches == 1 {
			return ""
		}
		return token
	})
	for id := range identifiers {
		if sourceSeriesID.MatchString(id) && !opaqueSourceID(id) {
			pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`)
			answer = pattern.ReplaceAllString(answer, "")
		}
	}
	if hasDigest {
		answer = sourceDigestLabel.ReplaceAllString(answer, "")
	}
	answer = maskInspectedMarkdownRows(answer, inspectedRows)
	// Citation attributes contain opaque chunk/KB IDs and source URLs, not
	// numeric claims. Keep checking every number in the surrounding prose.
	text := dateOrCell.ReplaceAllString(sourceCitationTag.ReplaceAllString(answer, ""), "")
	unsupported := []string{}
	seen := map[string]bool{}
	for _, id := range sourceOpaqueToken.FindAllString(text, -1) {
		// An opaque token containing letters cannot be a decimal calculation.
		if strings.ContainsAny(id, "abcdefABCDEF") && !identifiers[id] && !seen[id] {
			unsupported = append(unsupported, id)
			seen[id] = true
		}
	}
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
