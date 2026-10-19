package agent

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Numeric checking is independent of question vocabulary and supported analyses.
// It verifies traceability, not the semantic appropriateness of a statistic.
var evidenceDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var proseNumber = regexp.MustCompile(`[-+−]?\d+(?:,\d{3})*(?:\.\d+)?`)

// Longest complete forms precede partial forms so an invalid complete date is
// consumed once and cannot later lose its month/day to a shorter match.
var calendarDateLiteral = regexp.MustCompile(`\d{4}[-/.]\d{1,2}[-/.]\d{1,2}|\d{2,4}年\d{1,2}月\d{1,2}日?|\d{4}[-/]\d{1,2}|\d{1,2}[-/]\d{1,2}|\d{2,4}年(?:\d{1,2}月)?|\d{1,2}月(?:\d{1,2}日?)?`)
var cellOrListMarker = regexp.MustCompile(`\b[A-Z]{1,3}\d+\b|(?m)^\s*(?:[#>*-]|\*\*)*\s*\d+[.)）、]`)
var sourceCitationTag = regexp.MustCompile(`<(?:kb|web)\b[^>]*>`)
var sourceDigestLabel = regexp.MustCompile(`(?i)\bSHA-?256\b`)
var sourceOpaqueToken = regexp.MustCompile(`\b(?:[a-fA-F0-9]{64}|[a-fA-F0-9]{24}|[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})\b`)
var abbreviatedSourceDigest = regexp.MustCompile(`\b([a-fA-F0-9]{8,32})(?:…|\.\.\.)([a-fA-F0-9]{4,16})\b`)
var sourceSeriesID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{2,159}$`)
var sourceCellRow = regexp.MustCompile(`^[A-Z]{1,3}([1-9][0-9]{0,6})$`)
var sourceFormulaReference = regexp.MustCompile(`\b([A-Z]{1,3})\$?([1-9][0-9]{0,6})\b`)
var sourceFormulaQuotedText = regexp.MustCompile(`"(?:""|[^"])*"|'(?:''|[^'])*'`)

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

// Immutable query observations carry original source locators and preserved
// formulas as provenance. Their row numbers can explain a source reference,
// but cannot authorize that same bare number as a computed statistic.
func queriedSourceRows(data map[string]any, rows map[string]bool) {
	if data["status"] != "ok" || data["rows_are"] != "observations" {
		return
	}
	source, ok := data["source"].(map[string]any)
	if !ok {
		return
	}
	digest, _ := source["sha256"].(string)
	if len(digest) != 64 || !opaqueSourceID(digest) {
		return
	}
	add := func(column, number string) {
		row, err := strconv.Atoi(number)
		if err != nil || row > 1048576 {
			return
		}
		col := 0
		for _, c := range column {
			col = col*26 + int(c-'A'+1)
		}
		if col > 16384 {
			return
		}
		rows[number] = true
	}
	observations, _ := data["rows"].([]any)
	for _, item := range observations {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cell, _ := row["source_cell"].(string)
		match := sourceCellRow.FindStringSubmatch(cell)
		if len(match) != 2 {
			continue
		}
		add(strings.TrimSuffix(cell, match[1]), match[1])
		if dateCell, ok := row["date_cell"].(string); ok {
			if m := sourceCellRow.FindStringSubmatch(dateCell); len(m) == 2 {
				add(strings.TrimSuffix(dateCell, m[1]), m[1])
			}
		}
		formula, _ := row["formula"].(string)
		if !strings.HasPrefix(formula, "=") {
			continue
		}
		for _, reference := range sourceFormulaReference.FindAllStringSubmatch(sourceFormulaQuotedText.ReplaceAllString(formula, ""), -1) {
			add(reference[1], reference[2])
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

// This is lexical date recognition, not proof of a date's source provenance.
// Keep rejected matches intact; no sentinels or second partial-date pass.
func maskValidatedCalendarDates(text string) string {
	masked := []byte(text)
	for _, span := range calendarDateLiteral.FindAllStringIndex(text, -1) {
		if standaloneDateToken(text, span[0], span[1]) && validCalendarLiteral(text[span[0]:span[1]]) {
			for i := span[0]; i < span[1]; i++ {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

func standaloneDateToken(text string, start, end int) bool {
	identifier := func(b byte) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
	}
	if start > 0 && (identifier(text[start-1]) || strings.ContainsRune("./-", rune(text[start-1]))) {
		return false
	}
	if end < len(text) {
		if identifier(text[end]) {
			return false
		}
		if strings.ContainsRune("./-", rune(text[end])) && end+1 < len(text) && identifier(text[end+1]) {
			return false
		}
	}
	return true
}

func validCalendarLiteral(value string) bool {
	chinese := strings.ContainsAny(value, "年月")
	hasYear := strings.Contains(value, "年")
	parts := strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune("年月日-/.", r) })
	if !chinese {
		var separator rune
		for _, r := range value {
			if strings.ContainsRune("-/.", r) {
				if separator != 0 && separator != r {
					return false
				}
				separator = r
			}
		}
		hasYear = len(parts) > 0 && len(parts[0]) == 4
	}
	numbers := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		numbers[i] = n
	}
	// A leap-year reference preserves the existing yearless February 29 form.
	year, month, day := 2000, 1, 1
	switch {
	case hasYear && len(numbers) >= 1 && len(numbers) <= 3:
		year = numbers[0]
		if len(numbers) > 1 {
			month = numbers[1]
		}
		if len(numbers) > 2 {
			day = numbers[2]
		}
	case !hasYear && len(numbers) >= 1 && len(numbers) <= 2:
		month = numbers[0]
		if len(numbers) > 1 {
			day = numbers[1]
		}
	default:
		return false
	}
	if year < 1 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return date.Year() == year && int(date.Month()) == month && date.Day() == day
}

func ungroundedStructuredNumbers(answer string, messages []chat.Message, rawSteps ...[]types.AgentStep) []string {
	values := []float64{}
	percentageValues := []float64{}
	filenames := []string{}
	identifiers := map[string]bool{}
	digests := map[string]bool{}
	inspectedRows := map[string]bool{}
	hasDigest := false
	identityLabels := map[string]bool{}
	// Capability schemas describe valid arguments; their numeric limits are
	// never executed observations. Preserve the actual call identity even when
	// replayed tool messages omit Name, without classifying arbitrary payloads.
	nonEvidenceToolCalls := map[string]bool{}
	toolNames := map[string]string{}
	canonicalTableAnalyses := map[string]types.ToolCall{}
	markNonEvidence := func(id, name, operation string) {
		if id != "" {
			if _, exists := toolNames[id]; !exists {
				toolNames[id] = name
			}
		}
		if id != "" && (name == expandTaskToolsName || name == "structured_data" && operation == "describe_tools") {
			nonEvidenceToolCalls[id] = true
		}
	}
	for _, steps := range rawSteps {
		collectDurableDocumentIdentityTitles(steps, identityLabels)
		collectArtifactIdentityTitles(steps, identityLabels)
		for _, step := range steps {
			for _, call := range step.ToolCalls {
				operation, _ := call.Args["operation"].(string)
				markNonEvidence(call.ID, call.Name, operation)
				if call.ID != "" && call.Name == documentTableAnalysisToolName {
					canonicalTableAnalyses[call.ID] = call
				}
			}
		}
	}
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			var arguments struct {
				Operation string `json:"operation"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &arguments)
			markNonEvidence(call.ID, call.Function.Name, arguments.Operation)
		}
	}
	userTexts := []string{}
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case float64:
			values = append(values, x)
			percentageValues = append(percentageValues, x, x*100)
		case map[string]any:
			if x["kind"] == "task_tool_capabilities" || x["kind"] == "document_table_analysis" {
				// Analysis wrappers are interpreted only at an actual executor
				// boundary below. Never recurse into plans, schemas or advice.
				return
			}
			if x["kind"] == "source_calculation" && x["status"] == "ok" {
				// This protocol carries explicit unit algebra. An unknown or
				// dimensional quantity cannot gain a percentage simply because
				// the model omits its unit or writes a percent sign afterward.
				actual, percentages := sourceCalculationNumericEvidence(x)
				values = append(values, actual...)
				percentageValues = append(percentageValues, percentages...)
				return
			}
			for key, item := range x {
				// Identity text is only exempt as a complete known token below.
				// Never promote its numeric substrings/dimension codes to values.
				if key == "metric" || key == "sheet" || key == "unit" || key == "dimensions" || key == "headers" || key == "literal_fields" || key == "source_context" || key == "chart_titles" {
					continue
				}
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
						percentageValues = append(percentageValues, v, v*100)
					}
				}
			}
			// Decimal calculator results arrive as numeric strings. SQL, dates,
			// catalogue IDs and natural-language model explanations are not numbers.
			if n, err := strconv.ParseFloat(x, 64); err == nil {
				values = append(values, n)
				percentageValues = append(percentageValues, n, n*100)
			}
			if strings.HasPrefix(strings.TrimSpace(x), "{") {
				var d map[string]any
				if json.Unmarshal([]byte(x), &d) == nil {
					collect(d)
				}
			}
		}
	}
	// The model-facing context may omit or truncate a large tool payload.
	// Numeric/source verification uses the canonical successful runtime result,
	// not a lossy prompt rendering or an assistant's plan. Tool schemas and
	// document prose are never executed numeric evidence.
	for _, steps := range rawSteps {
		for _, call := range steps {
			for _, executed := range call.ToolCalls {
				if executed.Name == documentTableAnalysisToolName {
					for _, result := range executedDocumentTableResults(executed) {
						collect(result)
					}
					continue
				}
				if executed.Result == nil || !executed.Result.Success {
					continue
				}
				operation, _ := executed.Args["operation"].(string)
				switch executed.Name {
				case "structured_list_series", "structured_query_series", "structured_analyze_table", "structured_inspect_source", "structured_get_result_evidence", "calculate_source_values", "calculate_document_table":
				case "structured_data":
					switch operation {
					case "list_datasets", "list_series", "query_series", "analyze_table", "inspect_source", "compare_series", "convert_price", "get_result_evidence", "execute_field_plan":
					default:
						continue
					}
				default:
					continue
				}
				var data map[string]any
				if executed.Result.Data != nil {
					// Typed source data is canonical; display Output may have
					// formatting/redaction that is no longer valid JSON.
					encoded, err := json.Marshal(executed.Result.Data)
					if err != nil || json.Unmarshal(encoded, &data) != nil {
						continue
					}
				} else if json.Unmarshal([]byte(executed.Result.Output), &data) != nil {
					continue
				}
				// A source-binding clarification can carry real, authorized field
				// identities without authorizing a calculation. Retain those exact
				// labels for describing the candidates; do not promote any numeric
				// payload from an unresolved/error result into computed evidence.
				if data["status"] != "error" {
					collectSourceIdentityLabels(data, identityLabels)
				}
				if status, _ := data["status"].(string); status != "" && status != "ok" && status != "no_data" {
					continue
				}
				inspectedSourceRows(data, inspectedRows)
				queriedSourceRows(data, inspectedRows)
				collect(data)
			}
		}
	}
	for _, msg := range messages {
		if msg.Role == "tool" {
			name := msg.Name
			if name == "" {
				name = toolNames[msg.ToolCallID]
			}
			if name == expandTaskToolsName || nonEvidenceToolCalls[msg.ToolCallID] {
				continue
			}
			if name == documentTableAnalysisToolName {
				if canonical, exists := canonicalTableAnalyses[msg.ToolCallID]; exists {
					for _, result := range executedDocumentTableResults(canonical) {
						collect(result)
					}
					continue // Display text cannot override actual execution Data/status.
				}
			}
			// Fetched KB prose uses only separately verified cited chunk evidence;
			// embedded JSON in a document is not an executed aggregate.
			if name == "search_knowledge" || name == "read_document" {
				for _, title := range fetchedDocumentIdentityTitles(msg.Content) {
					identityLabels[title] = true
				}
				continue
			}
			start := strings.Index(msg.Content, "{")
			if start < 0 {
				continue
			}
			var data map[string]any
			if json.NewDecoder(strings.NewReader(msg.Content[start:])).Decode(&data) == nil {
				if name == documentTableAnalysisToolName || data["kind"] == "document_table_analysis" {
					for _, result := range documentTableAnalysisExecutions(name, data) {
						collect(result)
					}
					continue
				}
				if data["status"] == "error" {
					continue
				}
				collectSourceIdentityLabels(data, identityLabels)
				if status, _ := data["status"].(string); status != "" && status != "ok" && status != "no_data" && status != "resolved" {
					continue
				}
				inspectedSourceRows(data, inspectedRows)
				queriedSourceRows(data, inspectedRows)
				collect(data)
			}
		}
		if msg.Role == "user" && msg.Kind != chat.MessageKindReviewEvidence {
			userTexts = append(userTexts, msg.Content)
		}
	}
	labels := make([]string, 0, len(identityLabels))
	for label := range identityLabels {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool { return len(labels[i]) > len(labels[j]) })
	for _, text := range userTexts {
		for _, label := range labels {
			text = maskExactSourceIdentity(text, label)
		}
		for _, n := range proseNumber.FindAllString(text, -1) {
			if v, e := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64); e == nil {
				values = append(values, v)
				percentageValues = append(percentageValues, v, v*100)
			}
		}
	}
	if len(values) == 0 {
		return nil
	} // No numeric evidence; ordinary conversation.
	for _, file := range filenames {
		answer = strings.ReplaceAll(answer, file, "")
	}
	for _, label := range labels {
		answer = maskExactSourceIdentity(answer, label)
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
	answer = regexp.MustCompile(`\b([1-9][0-9]{0,6})\s*[–—~～-]\s*([1-9][0-9]{0,6})\s*行`).ReplaceAllStringFunc(answer, func(token string) string {
		parts := regexp.MustCompile(`[1-9][0-9]{0,6}`).FindAllString(token, -1)
		if len(parts) == 2 && inspectedRows[parts[0]] && inspectedRows[parts[1]] {
			return " "
		}
		return token
	})
	for row := range inspectedRows {
		answer = regexp.MustCompile(`(?:第\s*)?\b`+regexp.QuoteMeta(row)+`\s*行`).ReplaceAllString(answer, " ")
	}
	// Citation attributes contain opaque chunk/KB IDs and source URLs, not
	// numeric claims. Keep checking every number in the surrounding prose.
	text := sourceCitationTag.ReplaceAllString(answer, "")
	text = maskValidatedCalendarDates(text)
	text = cellOrListMarker.ReplaceAllString(text, "")
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
		candidates := values
		if percent {
			candidates = percentageValues
		}
		for _, v := range candidates {
			if math.Abs(math.Abs(number)-math.Abs(v)) <= tolerance {
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

// Return only canonical results of real document-table executions. The
// planner's wrapper is not itself a calculation, and unsuccessful/needs-source
// advice cannot authorize numeric facts. Partial wrappers retain only their
// actual successful children; their coverage still remains partial.
func executedDocumentTableResults(call types.ToolCall) []map[string]any {
	if call.Result == nil || (call.Name != documentTableCalculationToolName && call.Name != documentTableAnalysisToolName) {
		return nil
	}
	var data map[string]any
	if call.Result.Data != nil {
		encoded, err := json.Marshal(call.Result.Data)
		if err != nil || json.Unmarshal(encoded, &data) != nil {
			return nil
		}
	} else if json.Unmarshal([]byte(call.Result.Output), &data) != nil {
		return nil
	}
	if call.Name == documentTableCalculationToolName {
		if call.Result.Success && data["kind"] == "source_calculation" && data["status"] == "ok" {
			return []map[string]any{data}
		}
		return nil
	}
	if !call.Result.Success && data["status"] != "partial" {
		return nil
	}
	return documentTableAnalysisExecutions(call.Name, data)
}

func documentTableAnalysisExecutions(toolName string, data map[string]any) []map[string]any {
	if toolName != documentTableAnalysisToolName || data["kind"] != "document_table_analysis" || (data["status"] != "ok" && data["status"] != "partial") {
		return nil
	}
	results := []map[string]any{}
	executions, _ := data["executions"].([]any)
	for _, raw := range executions {
		execution, ok := raw.(map[string]any)
		if !ok || execution["success"] != true || execution["tool_name"] != documentTableCalculationToolName {
			continue
		}
		result, ok := execution["result"].(map[string]any)
		if ok && result["kind"] == "source_calculation" && result["status"] == "ok" {
			results = append(results, result)
		}
	}
	return results
}

// Source-bound calculation results explicitly distinguish displayed percent
// values, dimensionless ratios, other dimensions and unknown units. Only the
// first two can support a percent claim. Input quotes and other metadata are
// provenance, not additional executed numerical results.
func sourceCalculationNumericEvidence(data map[string]any) ([]float64, []float64) {
	values, percentages := []float64{}, []float64{}
	add := func(raw any, unit, status string) {
		var value float64
		switch typed := raw.(type) {
		case float64:
			value = typed
		case string:
			parsed, err := strconv.ParseFloat(typed, 64)
			if err != nil {
				return
			}
			value = parsed
		default:
			return
		}
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return
		}
		values = append(values, value)
		if status == "known" && unit == "%" {
			percentages = append(percentages, value)
		} else if status == "dimensionless" && unit == "" {
			percentages = append(percentages, value*100)
		}
	}
	// These are explicit program-counted execution coverage, not arbitrary
	// row indices or numbers mentioned in provenance text. Counts authorize
	// their original values only, never a percentage conversion.
	for _, key := range []string{"selected_row_count", "calculated_row_count"} {
		add(data[key], "", "unknown")
	}
	calculations, _ := data["calculations"].([]any)
	for _, raw := range calculations {
		quantity, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		unit, _ := quantity["unit"].(string)
		status, _ := quantity["unit_status"].(string)
		add(quantity["value"], unit, status)
		add(quantity["exact_value"], unit, status)
		inputs, _ := quantity["inputs"].(map[string]any)
		for _, rawInput := range inputs {
			input, ok := rawInput.(map[string]any)
			if !ok {
				continue
			}
			unit, _ := input["unit"].(string)
			status := "unknown"
			if unit != "" {
				status = "known"
			}
			add(input["value"], unit, status)
		}
	}
	return values, percentages
}

// Only the authorized filesystem inventory can prove an output identity. Do
// not parse a model's proposed path or arbitrary shell/document JSON as proof.
// Mask exact names/paths only; numbers in surrounding analysis stay checked.
func collectArtifactIdentityTitles(steps []types.AgentStep, labels map[string]bool) {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Name != agenttools.ToolListSandboxFiles || call.Result == nil || !call.Result.Success || call.Result.Data == nil {
				continue
			}
			raw, _ := json.Marshal(call.Result.Data)
			var inventory struct {
				Kind  string `json:"kind"`
				Files []struct {
					Path string `json:"path"`
					Name string `json:"name"`
				} `json:"files"`
			}
			if json.Unmarshal(raw, &inventory) != nil || inventory.Kind != "sandbox_file_inventory" {
				continue
			}
			for _, file := range inventory.Files {
				if path.IsAbs(file.Path) && path.Base(file.Path) == file.Name && file.Name != "" {
					labels[file.Path], labels[file.Name] = true, true
				}
			}
		}
	}
}

func collectDurableDocumentIdentityTitles(steps []types.AgentStep, labels map[string]bool) {
	add := func(value any) {
		title, ok := value.(string)
		if !ok || strings.TrimSpace(title) == "" {
			return
		}
		if _, err := strconv.ParseFloat(strings.TrimSpace(title), 64); err == nil {
			return
		}
		labels[title] = true
	}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if (call.Name != "search_knowledge" && call.Name != "read_document") || call.Result == nil || !call.Result.Success {
				continue
			}
			for _, field := range []string{"results", "chunks"} {
				encoded, _ := json.Marshal(call.Result.Data[field])
				var rows []map[string]any
				if json.Unmarshal(encoded, &rows) != nil {
					continue
				}
				for _, row := range rows {
					chunkID, _ := row["chunk_id"].(string)
					knowledgeID, _ := row["knowledge_id"].(string)
					if strings.TrimSpace(chunkID) == "" || strings.TrimSpace(knowledgeID) == "" {
						continue
					}
					add(row["knowledge_title"])
					add(row["title"])
					add(row["file_name"])
				}
			}
			knowledgeID, _ := call.Result.Data["knowledge_id"].(string)
			if call.Name == "read_document" && strings.TrimSpace(knowledgeID) != "" {
				add(call.Result.Data["knowledge_title"])
				if document, ok := call.Result.Data["document"].(map[string]any); ok {
					add(document["title"])
					add(document["file_name"])
				}
			}
		}
	}
}

// Search/read tools render authorized document identity outside escaped chunk
// content. Only those direct retrieval/document headers are identities: body
// text, nested fake XML and assistant citations cannot invent a file title.
func fetchedDocumentIdentityTitles(content string) []string {
	decoder := xml.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	stack := []string{}
	titles := []string{}
	knowledgeRetrieval := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return titles
		}
		if err != nil {
			return nil
		}
		switch node := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if node.Name.Local != "retrieval" {
					return nil
				}
				for _, attribute := range node.Attr {
					if attribute.Name.Local == "type" && attribute.Value == "knowledge" {
						knowledgeRetrieval = true
					}
				}
				if !knowledgeRetrieval {
					return nil
				}
			}
			if len(stack) == 1 && stack[0] == "retrieval" && node.Name.Local == "document" {
				title, id := "", ""
				for _, attribute := range node.Attr {
					switch attribute.Name.Local {
					case "title":
						title = attribute.Value
					case "id":
						id = attribute.Value
					}
				}
				if id != "" && title != "" {
					if _, err := strconv.ParseFloat(strings.TrimSpace(title), 64); err != nil {
						titles = append(titles, title)
					}
				}
			}
			stack = append(stack, node.Name.Local)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil
			}
			stack = stack[:len(stack)-1]
		}
	}
}

// Only actual catalogue and source-context fields establish label identities.
// SQL text, free-form notices and arbitrary metadata do not authorize labels.
func collectSourceIdentityLabels(data map[string]any, labels map[string]bool) {
	add := func(value any) {
		text, ok := value.(string)
		if !ok || len(text) > 512 || strings.TrimSpace(text) == "" {
			return
		}
		if _, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return
		}
		if len(proseNumber.FindAllString(text, -1)) > 0 {
			labels[text] = true
		}
	}
	var catalogue func(any)
	catalogue = func(value any) {
		switch x := value.(type) {
		case []any:
			for _, item := range x {
				catalogue(item)
			}
		case map[string]any:
			for _, key := range []string{"metric", "sheet", "unit", "region_name", "source_file", "file"} {
				add(x[key])
			}
			if id, ok := x["series_id"].(string); ok && sourceSeriesID.MatchString(id) {
				add(id)
			}
			if formula, ok := x["formula"].(string); ok {
				add(formula)
				add(strings.TrimPrefix(formula, "="))
			}
			if dimensions, ok := x["dimensions"].(map[string]any); ok {
				for _, value := range dimensions {
					add(value)
				}
			}
			if headers, ok := x["headers"].([]any); ok {
				for _, entry := range headers {
					if h, ok := entry.(map[string]any); ok {
						add(h["value"])
					}
				}
			}
			for _, key := range []string{"source_context", "formula_source", "references", "fields", "candidates"} {
				catalogue(x[key])
			}
		}
	}
	catalogue(data["series"])
	catalogue(data["literal_fields"])
	if data["kind"] == "structured_source_ambiguity" || data["kind"] == "structured_field_resolution" || data["kind"] == "structured_source_binding" {
		catalogue(data["fields"])
	}
	if datasets, ok := data["datasets"].([]any); ok {
		for _, entry := range datasets {
			if dataset, ok := entry.(map[string]any); ok {
				catalogue(dataset["literal_fields"])
				add(dataset["source_file"])
			}
		}
	}
	if data["kind"] == "structured_source_inspection" && data["status"] == "ok" && data["hash_verified"] == true {
		for _, group := range []string{"ranges", "matches"} {
			entries, _ := data[group].([]any)
			for _, entry := range entries {
				block, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				if group == "matches" {
					add(block["value"])
				}
				for _, field := range []string{"cells", "context_cells"} {
					cells, _ := block[field].([]any)
					for _, raw := range cells {
						switch cell := raw.(type) {
						case map[string]any:
							add(cell["value"])
							if formula, ok := cell["formula"].(string); ok {
								add(formula)
								add(strings.TrimPrefix(formula, "="))
							}
						case []any:
							if len(cell) >= 2 {
								add(cell[1])
							}
						}
					}
				}
			}
		}
	}
}

func maskExactSourceIdentity(answer, label string) string {
	// A catalogue token cannot exempt the same characters inside a longer
	// ASCII identifier (e.g. known 3128B must not approve forged 3128B9).
	asciiWord := func(c byte) bool {
		return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
	}
	var out strings.Builder
	for {
		index := strings.Index(answer, label)
		if index < 0 {
			out.WriteString(answer)
			break
		}
		end := index + len(label)
		bounded := !(index > 0 && asciiWord(answer[index-1]) && asciiWord(label[0])) && !(end < len(answer) && asciiWord(answer[end]) && asciiWord(label[len(label)-1]))
		out.WriteString(answer[:index])
		if bounded {
			out.WriteByte(' ')
		} else {
			out.WriteString(label)
		}
		answer = answer[end:]
	}
	return out.String()
}

func hasStructuredHistoryEvidence(messages []chat.Message) bool {
	names := map[string]string{}
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				names[call.ID] = call.Function.Name
			}
		}
	}
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		name := msg.Name
		if name == "" {
			name = names[msg.ToolCallID]
		}
		if name == "search_knowledge" || name == "read_document" {
			continue
		}
		start := strings.Index(msg.Content, "{")
		if start >= 0 {
			var data map[string]any
			if json.NewDecoder(strings.NewReader(msg.Content[start:])).Decode(&data) == nil && (name == documentTableAnalysisToolName || data["kind"] == "document_table_analysis") {
				if len(documentTableAnalysisExecutions(name, data)) > 0 {
					return true
				}
				continue // A plan/wrapper cannot prove its own execution or tool identity.
			}
		}
		if structuredResultHasEvidence(msg.Content) {
			return true
		}
	}
	return false
}
