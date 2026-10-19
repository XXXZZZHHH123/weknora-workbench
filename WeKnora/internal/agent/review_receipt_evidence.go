package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const reviewReceiptContainerQuoteLimit = 1024
const (
	reviewReceiptUniqueWitnessNodeLimit  = 65536
	reviewReceiptUniqueWitnessDepthLimit = 64
)

// reviewSourceWitness ties a quoted passage to one immutable current-turn
// tool result and the exact JSON path that produced it.
type reviewSourceWitness struct {
	EvidenceID       string                  `json:"evidence_id,omitempty"`
	JSONPointer      string                  `json:"json_pointer,omitempty"`
	Quote            string                  `json:"quote,omitempty"`
	Kind             string                  `json:"kind"`
	SourcePointerRef *int                    `json:"source_pointer_ref,omitempty"`
	RowRef           *reviewStructuredRowRef `json:"row_ref,omitempty"`
}

// reviewStructuredRowRef selects a cell already present in the bounded row
// companion. It is only a locator; resolution still binds the cell to the
// immutable tool result and runs the ordinary receipt visibility checks.
type reviewStructuredRowRef struct {
	EntryIndex int    `json:"entry_index"`
	RowIndex   int    `json:"row_index"`
	Column     string `json:"column"`
}

func (r *reviewStructuredRowRef) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"entry_index", "row_index", "column"} {
		raw, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("row_ref requires non-null %s", name)
		}
	}
	for name := range fields {
		if name != "entry_index" && name != "row_index" && name != "column" {
			return fmt.Errorf("row_ref contains unsupported field %s", name)
		}
	}
	if err := json.Unmarshal(fields["entry_index"], &r.EntryIndex); err != nil {
		return fmt.Errorf("row_ref entry_index must be an integer: %w", err)
	}
	if err := json.Unmarshal(fields["row_index"], &r.RowIndex); err != nil {
		return fmt.Errorf("row_ref row_index must be an integer: %w", err)
	}
	if err := json.Unmarshal(fields["column"], &r.Column); err != nil {
		return fmt.Errorf("row_ref column must be a string: %w", err)
	}
	return nil
}

// Quote may be a literal JSON scalar. Preserve its spelling without converting
// through float64; this only normalizes the wire type, never source provenance.
func (w *reviewSourceWitness) UnmarshalJSON(data []byte) error {
	type plainWitness reviewSourceWitness
	var value plainWitness
	wire := struct {
		*plainWitness
		Quote json.RawMessage `json:"quote"`
	}{plainWitness: &value}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, quotePresent := fields["quote"]
	if value.RowRef != nil {
		for _, forbidden := range []string{"evidence_id", "json_pointer", "quote", "source_pointer_ref"} {
			if _, exists := fields[forbidden]; exists {
				return fmt.Errorf("row_ref cannot be combined with %s", forbidden)
			}
		}
		if value.RowRef.EntryIndex < 0 || value.RowRef.RowIndex < 0 || value.RowRef.Column == "" {
			return fmt.Errorf("row_ref requires non-negative entry_index and row_index and a column name")
		}
		*w = reviewSourceWitness(value)
		return nil
	}
	if !quotePresent {
		if value.Kind != "source" || value.EvidenceID == "" || value.JSONPointer == "" || value.SourcePointerRef != nil {
			return fmt.Errorf("omitting quote requires kind=source, evidence_id and an explicit json_pointer")
		}
		*w = reviewSourceWitness(value)
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(wire.Quote)))
	decoder.UseNumber()
	var scalar any
	if err := decoder.Decode(&scalar); err != nil {
		return fmt.Errorf("witness quote must be text or a JSON number/boolean: %w", err)
	}
	switch v := scalar.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("witness quote cannot be empty or whitespace-only")
		}
		value.Quote = v
	case json.Number:
		value.Quote = v.String()
	case bool:
		value.Quote = strconv.FormatBool(v)
	default:
		return fmt.Errorf("witness quote must be text or a JSON number/boolean")
	}
	*w = reviewSourceWitness(value)
	return nil
}

// reviewWitnessVisible verifies both provenance and exposure. It deliberately
// does not assess whether the quote semantically supports a claim.
func reviewWitnessVisible(input map[string]any, reader *reviewEvidenceReader, witness reviewSourceWitness) error {
	if witness.RowRef != nil {
		if witness.Kind != "source" {
			return fmt.Errorf("row_ref is allowed only for source evidence")
		}
		resolved, err := resolveStructuredRowWitness(input, reader, witness)
		if err != nil {
			return err
		}
		witness = resolved
	}
	citationLocator := witness.SourcePointerRef != nil
	if citationLocator {
		resolved, err := resolveCitationBodyWitness(input, reader, witness)
		if err != nil {
			return err
		}
		witness = resolved
	} else if witness.EvidenceID == "" {
		return fmt.Errorf("witness requires an evidence identity and exact quote")
	}
	quoteOmitted := witness.Quote == ""
	if witness.EvidenceID == "" || len(witness.JSONPointer) > 1024 {
		return fmt.Errorf("witness requires an evidence identity and valid pointer")
	}
	if !quoteOmitted && strings.TrimSpace(witness.Quote) == "" {
		return fmt.Errorf("witness quote cannot be whitespace-only")
	}
	if quoteOmitted && (witness.Kind != "source" || witness.JSONPointer == "" || citationLocator) {
		return fmt.Errorf("omitting quote requires source evidence with an explicit json_pointer")
	}
	if witness.Kind != "source" && witness.Kind != "execution" {
		return fmt.Errorf("witness kind must be source or execution")
	}
	record, ok := reviewRecordByID(reader, witness.EvidenceID)
	if !ok {
		return fmt.Errorf("witness does not resolve to an immutable source result")
	}
	if !citationLocator && witness.JSONPointer == "" {
		if witness.Kind != "source" {
			return fmt.Errorf("pointerless witness path search is allowed only for source evidence")
		}
		data, dataErr := reviewWitnessRawValue(record, reviewSourceWitness{
			EvidenceID: witness.EvidenceID, JSONPointer: "/data", Quote: witness.Quote, Kind: "source",
		})
		if dataErr != nil {
			return dataErr
		}
		pointer, count, overflow := uniqueReviewScalarPointer(data, witness.Quote)
		switch {
		case overflow:
			return fmt.Errorf("quote path search exceeded its safety bound; provide an explicit json_pointer")
		case count == 0:
			return fmt.Errorf("quote did not match a scalar in successful result data; provide an explicit json_pointer")
		case count != 1:
			return fmt.Errorf("quote matched multiple scalar paths in successful result data; provide an explicit json_pointer")
		default:
			witness.JSONPointer = pointer
		}
	}
	value, err := reviewWitnessRawValue(record, witness)
	if err != nil {
		return err
	}
	if quoteOmitted {
		quote, scalar := reviewScalarWitnessQuote(value)
		if scalar {
			witness.Quote = quote
		} else {
			switch value.(type) {
			case map[string]any, []any:
				encoded, encodeErr := json.Marshal(value)
				if encodeErr != nil || len(encoded) > reviewReceiptContainerQuoteLimit {
					return fmt.Errorf("omitting quote for a container requires a compact source value within %d bytes", reviewReceiptContainerQuoteLimit)
				}
				// The locator is already bound to this immutable source result and
				// exact pointer. Derive only the complete canonical value; the common
				// raw-value and visibility checks below still require all of it to
				// have been exposed.
				witness.Quote = string(encoded)
			default:
				return fmt.Errorf("omitting quote is allowed only for a scalar or a compact container at the exact source pointer")
			}
		}
	}
	if !reviewQuoteMatchesValue(value, witness.Quote) {
		// A model may name the inspected cell/row object instead of its scalar
		// child. Keep that exact scope: allow a literal inside a small container
		// only when the ENTIRE unchanged container was exposed. Do not search a
		// different result, repair a wrong scalar pointer, or promote a hidden leaf.
		switch value.(type) {
		case map[string]any, []any:
			encoded, encodeErr := json.Marshal(value)
			if !citationLocator && witness.Kind == "source" && encodeErr == nil && len(encoded) <= reviewReceiptContainerQuoteLimit {
				_, matches, overflow := uniqueReviewScalarPointer(value, witness.Quote)
				if matches > 0 && !overflow {
					container := witness
					container.Quote = string(encoded)
					if reviewWitnessVisibleInInput(input, record, container) || reviewWitnessVisibleInTranscript(reader, container, value) {
						return nil
					}
					return fmt.Errorf("container quote requires the entire exact container to be inspected; read that path or use an exposed scalar path")
				}
			}
		}
		switch value.(type) {
		case map[string]any, []any:
			return fmt.Errorf("witness quote does not match its source container: provide the complete short compact JSON object/array, or quote a scalar value at its exact JSON pointer; object/array fragments do not match")
		}
		return fmt.Errorf("witness quote does not exactly match its source pointer")
	}
	if citationLocator {
		// The locator is deliberately limited to the body currently projected in
		// citation_source_evidence. A later reader observation cannot turn it into
		// a locator for a hidden suffix.
		if reviewWitnessVisibleInCitationEvidence(input["citation_source_evidence"], witness) {
			return nil
		}
	} else if reviewWitnessVisibleInInput(input, record, witness) || reviewWitnessVisibleInTranscript(reader, witness, value) {
		return nil
	}
	return fmt.Errorf("witness was not exposed: evidence_id=%s json_pointer=%s; inspect that exact path before approving", witness.EvidenceID, witness.JSONPointer)
}

func resolveStructuredRowWitness(input map[string]any, reader *reviewEvidenceReader, witness reviewSourceWitness) (reviewSourceWitness, error) {
	if witness.RowRef == nil || witness.EvidenceID != "" || witness.JSONPointer != "" || witness.Quote != "" || witness.SourcePointerRef != nil {
		return reviewSourceWitness{}, fmt.Errorf("row_ref must be the only evidence locator")
	}
	view := reviewAsMap(input["structured_query_row_companion"])
	if view == nil {
		return reviewSourceWitness{}, fmt.Errorf("row_ref requires a displayed structured query row companion")
	}
	entries := reviewAsSlice(view["entries"])
	if witness.RowRef.EntryIndex >= len(entries) {
		return reviewSourceWitness{}, fmt.Errorf("row_ref entry_index is outside the displayed companion")
	}
	entry := reviewAsMap(entries[witness.RowRef.EntryIndex])
	if entry == nil {
		return reviewSourceWitness{}, fmt.Errorf("row_ref does not identify a displayed row entry")
	}
	evidenceID, _ := entry["evidence_id"].(string)
	base, _ := entry["source_json_pointer"].(string)
	if evidenceID == "" || base == "" {
		return reviewSourceWitness{}, fmt.Errorf("row_ref entry has no source identity or path")
	}
	rows := reviewAsSlice(entry["rows"])
	if witness.RowRef.RowIndex >= len(rows) {
		return reviewSourceWitness{}, fmt.Errorf("row_ref row_index is outside the displayed rows")
	}
	row := rows[witness.RowRef.RowIndex]
	column := witness.RowRef.Column
	encoding, _ := entry["row_encoding"].(string)
	var value any
	var pointer string
	switch encoding {
	case "matrix":
		columns := reviewAsSlice(entry["columns"])
		columnIndex, matches := uniqueReviewColumnIndex(columns, column)
		rowValues := reviewAsSlice(row)
		if matches != 1 || columnIndex >= len(rowValues) {
			return reviewSourceWitness{}, fmt.Errorf("row_ref column is not a unique displayed matrix column")
		}
		value = rowValues[columnIndex]
		pointer = fmt.Sprintf("%s/%d/%d", base, witness.RowRef.RowIndex, columnIndex)
	case "objects":
		rowObject := reviewAsMap(row)
		var exists bool
		value, exists = rowObject[column]
		if rowObject == nil || !exists {
			return reviewSourceWitness{}, fmt.Errorf("row_ref column is not present in the displayed object row")
		}
		pointer = fmt.Sprintf("%s/%d/%s", base, witness.RowRef.RowIndex, escapeReviewPointerSegment(column))
	case "positional":
		constants := reviewAsMap(entry["constant_fields"])
		if constantValue, exists := constants[column]; exists {
			if _, duplicate := uniqueReviewColumnIndex(reviewAsSlice(entry["columns"]), column); duplicate != 0 {
				return reviewSourceWitness{}, fmt.Errorf("row_ref column is ambiguous between constant_fields and positional columns")
			}
			value = constantValue
		} else {
			columns := reviewAsSlice(entry["columns"])
			columnIndex, matches := uniqueReviewColumnIndex(columns, column)
			rowValues := reviewAsSlice(row)
			if matches != 1 || columnIndex >= len(rowValues) {
				return reviewSourceWitness{}, fmt.Errorf("row_ref column is not a unique displayed positional column")
			}
			value = rowValues[columnIndex]
		}
		pointer = fmt.Sprintf("%s/%d/%s", base, witness.RowRef.RowIndex, escapeReviewPointerSegment(column))
	default:
		return reviewSourceWitness{}, fmt.Errorf("row_ref uses an unsupported row encoding")
	}
	quote, ok := reviewScalarWitnessQuote(value)
	if !ok {
		return reviewSourceWitness{}, fmt.Errorf("row_ref must identify a displayed string, number, boolean or present null cell")
	}
	return reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: pointer, Quote: quote, Kind: "source"}, nil
}

func uniqueReviewColumnIndex(columns []any, name string) (int, int) {
	index, matches := -1, 0
	for i, raw := range columns {
		if column, ok := raw.(string); ok && column == name {
			index = i
			matches++
		}
	}
	return index, matches
}

func reviewScalarWitnessQuote(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "null", true
	case string:
		return typed, typed != ""
	case json.Number:
		return typed.String(), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

// uniqueReviewScalarPointer locates a quote only among scalar leaves in the
// successful result's data subtree. It never examines envelope metadata or
// tool output, and callers still run the derived path through normal raw and
// visibility validation.
func uniqueReviewScalarPointer(data any, quote string) (string, int, bool) {
	return uniqueReviewScalarPointerWithLimits(data, quote, reviewReceiptUniqueWitnessNodeLimit, reviewReceiptUniqueWitnessDepthLimit)
}

func uniqueReviewScalarPointerWithLimits(data any, quote string, nodeLimit, depthLimit int) (string, int, bool) {
	if quote == "" || nodeLimit <= 0 || depthLimit < 0 {
		return "", 0, false
	}
	nodes, count, found := 0, 0, ""
	overflow := false
	var walk func(any, string, int)
	walk = func(value any, pointer string, depth int) {
		if overflow || count > 1 {
			return
		}
		if depth > depthLimit || nodes >= nodeLimit || len(pointer) > 1024 {
			overflow = true
			return
		}
		nodes++
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if overflow || count > 1 {
					break
				}
				walk(typed[key], pointer+"/"+escapeReviewPointerSegment(key), depth+1)
			}
		case []any:
			for index, child := range typed {
				if overflow || count > 1 {
					break
				}
				walk(child, pointer+"/"+strconv.Itoa(index), depth+1)
			}
		default:
			if reviewQuoteMatchesValue(value, quote) {
				count++
				if count == 1 {
					found = pointer
				}
			}
		}
	}
	walk(data, "/data", 0)
	if overflow || count != 1 {
		found = ""
	}
	return found, count, overflow
}

func escapeReviewPointerSegment(segment string) string {
	segment = strings.ReplaceAll(segment, "~", "~0")
	return strings.ReplaceAll(segment, "/", "~1")
}

// resolveCitationBodyWitness translates the displayed source_pointer_ref
// through the server-owned citation projection tables. It must identify one
// main body item exactly; additional observations cannot stand in for it.
func resolveCitationBodyWitness(input map[string]any, reader *reviewEvidenceReader, witness reviewSourceWitness) (reviewSourceWitness, error) {
	if witness.Kind != "source" || witness.EvidenceID != "" || witness.JSONPointer != "" || witness.Quote == "" {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator requires source kind, quote, and no other locator")
	}
	ref := *witness.SourcePointerRef
	if ref < 0 {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator is out of range")
	}
	view := reviewAsMap(input["citation_source_evidence"])
	if view == nil {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator requires citation source evidence")
	}
	items := reviewAsSlice(view["items"])
	var item map[string]any
	itemMatches := 0
	for _, rawItem := range items {
		candidate := reviewAsMap(rawItem)
		candidateRef, validRef := exactReviewIndex(candidate["source_pointer_ref"])
		if validRef && candidateRef == ref {
			item = candidate
			itemMatches++
		}
	}
	if itemMatches != 1 {
		return reviewSourceWitness{}, fmt.Errorf("source_pointer_ref does not uniquely identify a displayed citation body")
	}
	content, contentOK := item["content"].(string)
	if item["content_included"] != true || !contentOK || content == "" || !strings.Contains(content, witness.Quote) {
		return reviewSourceWitness{}, fmt.Errorf("source_pointer_ref does not identify currently displayed body text")
	}
	pointers := reviewAsSlice(view["source_pointers"])
	if ref >= len(pointers) {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator is out of range")
	}
	pointerRow := reviewAsSlice(pointers[ref])
	if len(pointerRow) != 2 {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator has an invalid pointer mapping")
	}
	resultIndex, ok := exactReviewIndex(pointerRow[0])
	if !ok {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator has an invalid result mapping")
	}
	jsonPointer, ok := pointerRow[1].(string)
	if !ok || (jsonPointer != "/data" && !strings.HasPrefix(jsonPointer, "/data/")) {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator must point into returned source data")
	}
	results := reviewAsSlice(view["source_results"])
	if resultIndex >= len(results) {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator has an out-of-range source result")
	}
	resultRow := reviewAsSlice(results[resultIndex])
	if len(resultRow) < 3 {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator has an invalid source result")
	}
	tool, toolOK := resultRow[0].(string)
	callID, callOK := resultRow[1].(string)
	evidenceID, idOK := resultRow[2].(string)
	if !toolOK || !callOK || !idOK || tool == "" || callID == "" || evidenceID == "" {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator has an invalid source identity")
	}
	record, found := reviewRecordByID(reader, evidenceID)
	if !found || record.Tool != tool || record.ToolCallID != callID {
		return reviewSourceWitness{}, fmt.Errorf("citation body locator does not match an immutable source result")
	}
	return reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: jsonPointer, Quote: witness.Quote, Kind: "source"}, nil
}

func exactReviewIndex(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case int64:
		return int(number), number >= 0 && int64(int(number)) == number
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && parsed >= 0 && int64(int(parsed)) == parsed
	case float64:
		return int(number), number >= 0 && number == float64(int(number))
	default:
		return 0, false
	}
}

func reviewRecordByID(reader *reviewEvidenceReader, id string) (reviewEvidenceRecord, bool) {
	if reader == nil {
		return reviewEvidenceRecord{}, false
	}
	if index, ok := reader.byID[id]; ok && index >= 0 && index < len(reader.records) {
		return reader.records[index], true
	}
	for _, record := range reader.records {
		if record.ID == id {
			return record, true
		}
	}
	return reviewEvidenceRecord{}, false
}

func reviewWitnessRawValue(record reviewEvidenceRecord, witness reviewSourceWitness) (any, error) {
	if !reviewReceiptEligibleResult(record) {
		return nil, fmt.Errorf("witness cannot use a plan, schema or tool directory result")
	}
	payload, isEnvelope := record.Value.(map[string]any)
	if witness.Kind == "source" {
		if !isEnvelope || payload["success"] != true {
			return nil, fmt.Errorf("source witness requires a successful tool result")
		}
		// These Wiki renderers return the actual searched/read text in Output,
		// including retrieval boundaries that are not document body fields.
		// Keep this adapter explicit; arbitrary tool/status output is not a source.
		wikiOutput := witness.JSONPointer == "/output" && (record.Tool == "wiki_search" || record.Tool == "wiki_read_page")
		if !wikiOutput && witness.JSONPointer != "/data" && !strings.HasPrefix(witness.JSONPointer, "/data/") {
			return nil, fmt.Errorf("source witness must point into successful result data (or exact /output of a successful Wiki search/read result)")
		}
		if wikiOutput {
			if _, ok := payload["output"].(string); !ok {
				return nil, fmt.Errorf("Wiki source output must be returned text")
			}
		} else if _, exists := payload["data"]; !exists {
			return nil, fmt.Errorf("source witness has no returned data")
		}
	} else {
		if !isEnvelope {
			return nil, fmt.Errorf("execution witness requires a tool result envelope")
		}
		if payload["success"] == true {
			if !strings.HasPrefix(witness.JSONPointer, "/execution_scope/") && witness.JSONPointer != "/execution_scope" &&
				witness.JSONPointer != "/success" {
				return nil, fmt.Errorf("successful execution witness may establish only execution status or scope")
			}
		} else if witness.JSONPointer != "/success" && witness.JSONPointer != "/error" &&
			!strings.HasPrefix(witness.JSONPointer, "/error/") &&
			witness.JSONPointer != "/execution_scope" && !strings.HasPrefix(witness.JSONPointer, "/execution_scope/") {
			return nil, fmt.Errorf("failed execution witness may establish only failure or scope metadata")
		}
	}
	value, err := reviewPointerValue(record.Value, witness.JSONPointer)
	if err != nil {
		return nil, fmt.Errorf("witness pointer does not resolve: %w", err)
	}
	return value, nil
}

func reviewReceiptEligibleResult(record reviewEvidenceRecord) bool {
	if record.Tool == "plan_task" || record.Tool == expandTaskToolsName || record.Tool == "describe_tools" {
		return false
	}
	payload, ok := record.Value.(map[string]any)
	if !ok {
		return false
	}
	data, _ := payload["data"].(map[string]any)
	return record.Tool != "structured_data" || data == nil || data["operation"] != "describe_tools"
}

func reviewQuoteMatchesValue(value any, quote string) bool {
	switch v := value.(type) {
	case string:
		return quote != "" && strings.Contains(v, quote)
	case json.Number, bool, nil, float64, float32, int, int64, uint64:
		encoded, err := json.Marshal(value)
		return err == nil && string(encoded) == quote
	default:
		if len(quote) > reviewReceiptContainerQuoteLimit {
			return false
		}
		encoded, err := json.Marshal(value)
		return err == nil && string(encoded) == quote
	}
}

func reviewWitnessVisibleInInput(input map[string]any, record reviewEvidenceRecord, witness reviewSourceWitness) bool {
	if input == nil {
		return false
	}
	if reviewWitnessVisibleInToolResults(input["source_tool_results"], witness) ||
		reviewWitnessVisibleInCitationEvidence(input["citation_source_evidence"], witness) ||
		reviewWitnessVisibleInDocumentCompanion(input["document_retrieval_companion"], witness) ||
		reviewWitnessVisibleInRowCompanion(input["structured_query_row_companion"], witness) {
		return true
	}
	// Image summaries are compact projections. Rebind them through the original
	// tool call and the exact source/image index before accepting a shown value.
	if reviewWitnessVisibleInIdentityView(input["document_image_observations"], record, witness) {
		return true
	}
	return reviewWitnessVisibleInIdentityView(input["document_table_execution_summary"], record, witness)
}

func reviewNormalizeJSON(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var normalized any
	if decoder.Decode(&normalized) != nil {
		return nil
	}
	return normalized
}

func reviewAsMap(value any) map[string]any {
	normalized, ok := reviewNormalizeJSON(value).(map[string]any)
	if !ok {
		return nil
	}
	return normalized
}

func reviewAsSlice(value any) []any {
	normalized, ok := reviewNormalizeJSON(value).([]any)
	if !ok {
		return nil
	}
	return normalized
}

func reviewWitnessVisibleInToolResults(value any, witness reviewSourceWitness) bool {
	for _, raw := range reviewAsSlice(value) {
		entry := reviewAsMap(raw)
		if entry == nil || entry["evidence_id"] != witness.EvidenceID {
			continue
		}
		content, ok := entry["content"]
		if !ok {
			continue
		}
		pointed, err := reviewPointerValue(content, witness.JSONPointer)
		if err == nil && reviewQuoteMatchesValue(pointed, witness.Quote) {
			return true
		}
	}
	return false
}

func reviewWitnessVisibleInCitationEvidence(value any, witness reviewSourceWitness) bool {
	view := reviewAsMap(value)
	if view == nil {
		return false
	}
	results := reviewAsSlice(view["source_results"])
	pointers := reviewAsSlice(view["source_pointers"])
	linked := map[int]bool{}
	resultIndex := linkedSourceResultIndex(results, witness.EvidenceID)
	if resultIndex < 0 {
		return false
	}
	for pointerIndex, raw := range pointers {
		row := reviewAsSlice(raw)
		if len(row) != 2 || !reviewIntegerEqual(row[0], resultIndex) || row[1] != witness.JSONPointer {
			continue
		}
		linked[pointerIndex] = true
	}
	if len(linked) == 0 {
		return false
	}
	for _, raw := range reviewAsSlice(view["items"]) {
		item := reviewAsMap(raw)
		if item == nil {
			continue
		}
		ref := reviewInt(item["source_pointer_ref"])
		if !linked[ref] {
			continue
		}
		if item["content_included"] == true {
			if content, ok := item["content"].(string); ok && strings.Contains(content, witness.Quote) {
				return true
			}
		}
	}
	return false
}

func linkedSourceResultIndex(results []any, evidenceID string) int {
	for index, raw := range results {
		row := reviewAsSlice(raw)
		if len(row) >= 3 && row[2] == evidenceID {
			return index
		}
	}
	return -1
}

func reviewWitnessVisibleInDocumentCompanion(value any, witness reviewSourceWitness) bool {
	view := reviewAsMap(value)
	if view == nil {
		return false
	}
	for _, raw := range reviewAsSlice(view["observations"]) {
		observation := reviewAsMap(raw)
		if observation == nil || observation["evidence_id"] != witness.EvidenceID || observation["body_included"] != true {
			continue
		}
		if observation["json_pointer"] != witness.JSONPointer {
			continue
		}
		for _, bodyRaw := range reviewAsSlice(view["bodies"]) {
			body := reviewAsMap(bodyRaw)
			if body != nil && body["body_id"] == observation["body_id"] {
				if content, ok := body["content"].(string); ok && strings.Contains(content, witness.Quote) {
					return true
				}
			}
		}
	}
	return false
}

func reviewWitnessVisibleInRowCompanion(value any, witness reviewSourceWitness) bool {
	view := reviewAsMap(value)
	if view == nil {
		return false
	}
	for _, raw := range reviewAsSlice(view["entries"]) {
		entry := reviewAsMap(raw)
		if entry == nil || entry["evidence_id"] != witness.EvidenceID {
			continue
		}
		// Row and metadata completeness are separate. A row projection may be
		// truncated while a metadata value remains visible (or metadata may be
		// partial while some exact values remain visible). Resolve the original
		// /data pointer into the displayed metadata itself; never treat either
		// completeness flag or an omission notice as source content.
		if strings.HasPrefix(witness.JSONPointer, "/data/") {
			local := strings.TrimPrefix(witness.JSONPointer, "/data")
			firstSegment := strings.TrimPrefix(local, "/")
			if slash := strings.IndexByte(firstSegment, '/'); slash >= 0 {
				firstSegment = firstSegment[:slash]
			}
			decodedFirst, validFirst := reviewDecodePointerSegment(firstSegment)
			if validFirst && decodedFirst != "rows" {
				if pointed, err := reviewPointerValue(entry["metadata"], local); err == nil && reviewQuoteMatchesValue(pointed, witness.Quote) {
					return true
				}
			}
		}
		base, _ := entry["source_json_pointer"].(string)
		if base == "" || !strings.HasPrefix(witness.JSONPointer, base+"/") {
			continue
		}
		local := strings.TrimPrefix(witness.JSONPointer, base)
		parts := strings.Split(strings.TrimPrefix(local, "/"), "/")
		if len(parts) < 2 {
			continue
		}
		parsedRowIndex, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil || parsedRowIndex < 0 || strconv.Itoa(parsedRowIndex) != parts[0] {
			continue
		}
		rowIndex := parsedRowIndex
		rows := reviewAsSlice(entry["rows"])
		if rowIndex < 0 || rowIndex >= len(rows) {
			continue
		}
		row := rows[rowIndex]
		columnName, validColumn := reviewDecodePointerSegment(parts[1])
		if !validColumn {
			continue
		}
		if entry["row_encoding"] == "positional" {
			if constants := reviewAsMap(entry["constant_fields"]); constants != nil && len(parts) == 2 {
				if value, exists := constants[columnName]; exists && reviewQuoteMatchesValue(value, witness.Quote) {
					return true
				}
			}
			columns := reviewAsSlice(entry["columns"])
			columnIndex := -1
			for i, column := range columns {
				if column == columnName {
					columnIndex = i
					break
				}
			}
			values := reviewAsSlice(row)
			if columnIndex < 0 || columnIndex >= len(values) || len(parts) != 2 {
				continue
			}
			if reviewQuoteMatchesValue(values[columnIndex], witness.Quote) {
				return true
			}
		} else if entry["row_encoding"] == "objects" || entry["row_encoding"] == "matrix" {
			pointed, err := reviewPointerValue(row, "/"+strings.Join(parts[1:], "/"))
			if err == nil && reviewQuoteMatchesValue(pointed, witness.Quote) {
				return true
			}
		}
	}
	return false
}

func reviewDecodePointerSegment(encoded string) (string, bool) {
	var decoded strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '~' {
			decoded.WriteByte(encoded[i])
			continue
		}
		if i+1 >= len(encoded) || (encoded[i+1] != '0' && encoded[i+1] != '1') {
			return "", false
		}
		i++
		if encoded[i] == '0' {
			decoded.WriteByte('~')
		} else {
			decoded.WriteByte('/')
		}
	}
	return decoded.String(), true
}

func reviewWitnessVisibleInIdentityView(value any, record reviewEvidenceRecord, witness reviewSourceWitness) bool {
	for _, raw := range reviewAsSlice(value) {
		entry := reviewAsMap(raw)
		if entry == nil {
			continue
		}
		// These modality summaries use a derived evidence ID. Rebind their
		// displayed content only through the original tool and call identity;
		// the raw pointer and quote were independently verified above.
		if entry["tool"] != record.Tool || entry["tool_call_id"] != record.ToolCallID {
			continue
		}
		if reviewIdentityViewValueVisible(entry["content"], record, witness) {
			return true
		}
	}
	return false
}

func reviewIdentityViewValueVisible(value any, record reviewEvidenceRecord, witness reviewSourceWitness) bool {
	content := reviewAsMap(value)
	if content == nil {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(witness.JSONPointer, "/data/"), "/")
	if len(parts) == 2 && parts[0] == "image_descriptions" {
		index, err := strconv.Atoi(parts[1])
		if err != nil || reviewInt(content["returned_image_index"]) != index {
			return false
		}
		data, _ := reviewPointerValue(record.Value, "/data")
		dataMap, _ := data.(map[string]any)
		reads := reviewAsSlice(dataMap["image_evidence"])
		boundRead := false
		for _, rawRead := range reads {
			read := reviewAsMap(rawRead)
			if read == nil || read["read_success"] != true || reviewInt(read["returned_image_index"]) != index {
				continue
			}
			if reviewImageIdentityMatches(read, content) {
				boundRead = true
				break
			}
		}
		if !boundRead {
			return false
		}
		if observation, ok := content["vision_observation"].(string); ok {
			return reviewQuoteMatchesValue(observation, witness.Quote)
		}
		return false
	}
	if len(parts) >= 4 && parts[0] == "image_geometry_evidence" && parts[2] == "evidence" {
		index, err := strconv.Atoi(parts[1])
		if err != nil || index < 0 || strconv.Itoa(index) != parts[1] {
			return false
		}
		figure, err := reviewPointerValue(record.Value, "/data/image_geometry_evidence/"+parts[1])
		figureMap, ok := figure.(map[string]any)
		if err != nil || !ok || !reviewImageIdentityMatches(figureMap, content) {
			return false
		}
		data, _ := reviewPointerValue(record.Value, "/data")
		dataMap, _ := data.(map[string]any)
		if !reviewImageProjectionBoundToSource(dataMap["image_evidence"], figureMap, content) {
			return false
		}
		local := "/original_geometry_evidence/" + strings.Join(parts[3:], "/")
		pointed, err := reviewPointerValue(content, local)
		return err == nil && reviewQuoteMatchesValue(pointed, witness.Quote)
	}
	return false
}

func reviewImageProjectionBoundToSource(reads any, source, projection map[string]any) bool {
	for _, raw := range reviewAsSlice(reads) {
		read := reviewAsMap(raw)
		if read == nil || read["read_success"] != true || !reviewImageIdentityMatches(source, read) ||
			reviewInt(read["returned_image_index"]) != reviewInt(projection["returned_image_index"]) {
			continue
		}
		return true
	}
	return false
}

func reviewImageIdentityMatches(source, projection map[string]any) bool {
	for _, key := range []string{"reference", "knowledge_id", "chunk_id", "page"} {
		sourceValue, sourceOK := source[key]
		projectedValue, projectedOK := projection[key]
		if !sourceOK || !projectedOK || !reviewJSONValuesEqual(sourceValue, projectedValue) {
			return false
		}
	}
	return true
}

func reviewJSONValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func reviewWitnessVisibleInTranscript(reader *reviewEvidenceReader, witness reviewSourceWitness, rawValue any) bool {
	if reader == nil || len(reader.transcript) == 0 {
		return false
	}
	validCalls := map[string]bool{}
	for _, message := range reader.transcript {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == reviewEvidenceToolName && call.ID != "" {
				validCalls[call.ID] = true
			}
		}
	}
	for _, message := range reader.transcript {
		if message.Role != "tool" || message.Name != reviewEvidenceToolName || !validCalls[message.ToolCallID] {
			continue
		}
		var response map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.Content))
		decoder.UseNumber()
		if decoder.Decode(&response) != nil || (response["status"] != "ok" && response["status"] != "partial") {
			continue
		}
		if response["status"] == "ok" && response["evidence_id"] == witness.EvidenceID && response["json_pointer"] == witness.JSONPointer {
			if text, ok := response["text"].(string); ok && strings.Contains(text, witness.Quote) {
				return true
			}
			if text, ok := response["serialized_json_segment"].(string); ok && strings.Contains(text, witness.Quote) {
				return true
			}
			if value, ok := response["value"]; ok && reviewQuoteMatchesValue(value, witness.Quote) {
				return true
			}
		}

		// A complete JSON parent read also exposes its exact child paths.
		// Partial serialized fragments cannot be reinterpreted as full values.
		if response["status"] == "ok" && response["evidence_id"] == witness.EvidenceID {
			base, hasPointer := response["json_pointer"].(string)
			if value, hasValue := response["value"]; hasPointer && hasValue && strings.HasPrefix(witness.JSONPointer, base+"/") {
				local := strings.TrimPrefix(witness.JSONPointer, base)
				if child, err := reviewPointerValue(value, local); err == nil && reviewQuoteMatchesValue(child, witness.Quote) {
					return true
				}
			}
		}
		for _, raw := range reviewAsSlice(response["matches"]) {
			match := reviewAsMap(raw)
			if match == nil || match["evidence_id"] != witness.EvidenceID || match["json_pointer"] != witness.JSONPointer {
				continue
			}
			if context, ok := match["context_text"].(string); ok && strings.Contains(context, witness.Quote) && reviewQuoteMatchesValue(rawValue, witness.Quote) {
				return true
			}
		}
	}
	// A child fully contained in verified pages is visible even when unrelated
	// siblings were not read. Exact paths, source bytes and read budgets still apply.
	return reviewWitnessVisibleInJSONPages(reader, witness)
}

func reviewIntegerEqual(value any, expected int) bool { return reviewInt(value) == expected }

func reviewInt(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	case float64:
		return int(number)
	default:
		return -1
	}
}
