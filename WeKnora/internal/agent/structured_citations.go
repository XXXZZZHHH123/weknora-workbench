package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

const structuredCitationDirectoryBytes = 12 * 1024
const maxCurrentStructuredCitations = 256

var answerEvidenceCitation = regexp.MustCompile(`<evidence\s+evidence_id="([A-Za-z0-9_.-]{1,128})"\s*/?>`)

// These private entries are created only while appending actual successful
// current tool executions. Saved message references and model-supplied flags
// never populate this map or acquire current citable status.
type structuredCitationObservation struct {
	Reference types.EvidenceReference
	CallID    string
	DataHash  string
	Directory map[string]any
}

func structuredCitationOperation(call types.ToolCall) string {
	switch call.ExecutionName() {
	case "structured_read_pdf_table":
		return "read_pdf_table"
	case "structured_query_pdf_table":
		return "query_pdf_table"
	case "structured_query_series":
		return "query_series"
	case "structured_analyze_table":
		return "analyze_table"
	case "structured_data":
		op, _ := call.ExecutionArgs()["operation"].(string)
		switch op {
		case "read_pdf_table", "query_pdf_table", "query_series", "analyze_table":
			return op
		}
	}
	return ""
}

// currentStructuredCitationView returns bounded navigation to the actual
// returned values. It never mutates ToolResult.Data, the execution witness or
// the durable tool transcript. Omitted citations are explicitly counted.
func (e *AgentEngine) currentStructuredCitationView(call types.ToolCall) string {
	if e == nil || e.replayingEvidenceHistory || e.config == nil || !e.config.EvidenceReferencesEnabled || !e.config.CitationsEnabled() || e.modelContext == nil || call.Result == nil || !call.Result.Success || structuredCitationOperation(call) == "" {
		return ""
	}
	if e.turnReview.StructuredCitationCalls == nil {
		e.turnReview.StructuredCitationCalls = map[string][]string{}
		e.turnReview.StructuredCitations = map[string]structuredCitationObservation{}
		e.turnReview.StructuredCitationNonce = uuid.NewString()
	}
	key := call.ID + "/" + reviewDigest(call.Result.Data)
	ids, existing := e.turnReview.StructuredCitationCalls[key]
	if !existing {
		ids = e.registerStructuredCitationResult(call)
		e.turnReview.StructuredCitationCalls[key] = ids
	}
	if len(ids) == 0 {
		return ""
	}
	rows := []any{}
	view := map[string]any{"schema_version": 1, "citations": rows, "citation_count": len(ids), "omitted_citation_count": len(ids), "projection_complete": false,
		"notice": "These handles cite this actual result, not an arbitrary document chunk. Select the exact cell for a source value, or a result citation for a calculation. Scope, source extraction quality and unknown units retain their stated limitations. Omitted entries are not absent data; narrow an existing read/query when necessary. Never copy observations from memory or turn a SQL label into a measured value."}
	for _, id := range ids {
		entry := e.turnReview.StructuredCitations[id].Directory
		trial := append(append([]any(nil), rows...), entry)
		view["citations"] = trial
		encoded, err := json.Marshal(view)
		if err != nil || len(encoded) > structuredCitationDirectoryBytes {
			break
		}
		rows = trial
	}
	view["citations"], view["omitted_citation_count"], view["projection_complete"] = rows, len(ids)-len(rows), len(ids) == len(rows)
	encoded, _ := json.Marshal(view)
	return "\n<structured_evidence_citations>" + string(encoded) + "</structured_evidence_citations>"
}

func (e *AgentEngine) registerStructuredCitationResult(call types.ToolCall) []string {
	data := call.Result.Data
	contract, err := structuredresult.ValidatePayload(data)
	if err != nil || contract == nil || data["status"] != "ok" {
		return nil
	}
	source, ok := data["source"].(map[string]any)
	scope, _ := data["source_context"].(map[string]any)
	kb, knowledge := citationString(scope["knowledge_base_id"]), citationString(source["knowledge_id"])
	if !ok || kb == "" || knowledge == "" || citationString(source["sha256"]) != contract.SourceSnapshot.SourceSHA256 || citationString(source["knowledge_base_id"]) != "" && citationString(source["knowledge_base_id"]) != kb {
		return nil
	}
	version := contract.SourceSnapshot.Versions
	snapshot := types.EvidenceSourceSnapshot{KnowledgeBaseID: kb, KnowledgeID: knowledge, SourceSHA256: contract.SourceSnapshot.SourceSHA256,
		KnowledgeFileHash: citationString(source["file_hash"]), TableID: citationString(data["table_id"]),
		TableRecordSHA256: version["record_sha256"], BundleSHA256: version["bundle_sha256"], PlanSHA256: version["plan_sha256"],
		FileName: contract.SourceSnapshot.File, Title: contract.SourceSnapshot.File}
	if tenant := citationInt(scope["knowledge_base_tenant_id"]); tenant > 0 {
		snapshot.TenantID = uint64(tenant)
	}
	rows, ok := data["rows"].([]any)
	if !ok || len(rows) == 0 {
		return nil
	}
	ids := []string{}
	add := func(kind string, selector map[string]any, values map[string]any, resultColumns []string, resultRows []any, unit map[string]any, cells []any) {
		if len(e.turnReview.StructuredCitations) >= maxCurrentStructuredCitations {
			return
		}
		locators := structuredCellLocators(cells, snapshot.SourceSHA256)
		if kind == types.EvidenceKindRawCell && len(locators) == 0 {
			locators = structuredSheetLocators(selector, snapshot.SourceSHA256)
		}
		selectedSource := snapshot
		selectedSource.Locators = locators
		manifest := map[string]any{"schema_version": 1, "tool": call.ExecutionName(), "tool_call_id": call.ID, "result_contract": data["result_contract"],
			"selector": selector, "result": map[string]any{"columns": resultColumns, "rows": resultRows}, "unit": unit, "source_cells": cells,
			"quality":     map[string]any{"extraction_verified": data["extraction_verified"], "input_complete": data["input_complete"], "output_truncated": data["output_truncated"], "coverage": data["coverage"]},
			"computation": map[string]any{"sql": data["sql"], "execution": contract.Execution, "input_count": data["input_count"], "provenance": data["provenance"], "inputs": data["inputs"]}}
		encoded, err := json.Marshal(manifest)
		if err != nil || len(encoded) > types.MaxEvidenceManifestBytes {
			return
		}
		id := "ev_" + reviewDigest([]any{e.turnReview.StructuredCitationNonce, call.ID, contract.ResultHash, selector})
		summary := "实际返回结果"
		if selector != nil {
			summary = citationString(selector["column_name"])
		}
		ref := types.EvidenceReference{ID: id, Kind: kind, ResultID: contract.ResultID, ResultHash: contract.ResultHash, Title: snapshot.FileName, Summary: summary,
			VersionStatus: "snapshot", Sources: []types.EvidenceSourceSnapshot{selectedSource}, Locators: locators, Manifest: encoded}
		handle := e.modelContext.RegisterEvidenceCitation(modelcontext.EvidenceCitation{ID: id, Kind: kind, Title: snapshot.FileName})
		if handle == "" {
			return
		}
		entry := map[string]any{"citation_handle": handle, "kind": kind, "selector": selector, "result_id": contract.ResultID, "value": values, "unit": unit, "evidence_level": contract.Capabilities.Level}
		entry["evidence_id"] = structuredCitationReaderID(call)
		entry["json_pointer"] = structuredCitationResultPointer(data, selector)
		e.turnReview.StructuredCitations[id] = structuredCitationObservation{Reference: ref, CallID: call.ID, DataHash: reviewDigest(data), Directory: entry}
		ids = append(ids, id)
	}
	columnMetadata, _ := data["result_contract"].(map[string]any)
	metadataRows, _ := columnMetadata["column_metadata"].([]any)
	columnUnit := func(index int) map[string]any {
		if index >= 0 && index < len(metadataRows) {
			if m, ok := metadataRows[index].(map[string]any); ok {
				if u, ok := m["unit"].(map[string]any); ok {
					return u
				}
			}
		}
		return map[string]any{"status": "unknown"}
	}
	// A bounded whole-result handle lets the model cite a table or computation
	// with many output cells without manufacturing hundreds of references.
	parentColumns := []string{}
	parentRows := []any{}
	parentCells := []any{}
	if data["kind"] == "structured_analysis" {
		parentColumns, _ = structuredAnalysisColumns(data["columns"], rows)
		parentRows = rows
	} else if data["kind"] == "structured_source_inspection" {
		columns, _ := data["columns"].([]any)
		for _, raw := range columns {
			if col, ok := raw.(map[string]any); ok {
				parentColumns = append(parentColumns, citationString(col["name"]))
			}
		}
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			values, _ := row["values"].(map[string]any)
			spans, _ := row["source_cells"].(map[string]any)
			matrix := []any{}
			for _, name := range parentColumns {
				matrix = append(matrix, values[name])
				cells, _ := spans[name].([]any)
				parentCells = append(parentCells, cells...)
			}
			parentRows = append(parentRows, matrix)
		}
	} else if data["rows_are"] == "observations" {
		parentColumns = []string{"date", "value"}
		for _, raw := range rows {
			if row, ok := raw.(map[string]any); ok {
				parentRows = append(parentRows, []any{row["date"], row["value"]})
			}
		}
	}
	if len(parentColumns) > 0 && len(parentRows) > 0 {
		kind := types.EvidenceKindStructuredResult
		if data["kind"] != "structured_analysis" {
			kind = types.EvidenceKindRawCell
		}
		add(kind, map[string]any{"scope": "returned_result", "column_name": "本次返回数据"}, nil, parentColumns, parentRows, map[string]any{"status": "per_column", "column_metadata": metadataRows}, parentCells)
	}
	// Raw PDF coordinates retain the original spans. SQL outputs are result
	// cells, never falsely registered as original PDF cells.
	if data["kind"] == "structured_source_inspection" && data["source_kind"] == "pdf_table" {
		columns, _ := data["columns"].([]any)
		for ri, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			values, _ := row["values"].(map[string]any)
			spans, _ := row["source_cells"].(map[string]any)
			for ci, rawColumn := range columns {
				col, ok := rawColumn.(map[string]any)
				if !ok {
					continue
				}
				name := citationString(col["name"])
				value, exists := values[name]
				if !exists {
					continue
				}
				cells, _ := spans[name].([]any)
				if len(cells) == 0 {
					continue
				}
				selector := map[string]any{"row_index": ri, "row_offset": citationInt(data["offset"]), "column_index": ci, "column_name": name}
				add(types.EvidenceKindRawCell, selector, map[string]any{name: value}, []string{name}, []any{[]any{value}}, columnUnit(ci), cells)
			}
		}
	} else if data["kind"] == "structured_analysis" {
		columns, ok := structuredAnalysisColumns(data["columns"], rows)
		if !ok {
			return ids
		}
		for ri, raw := range rows {
			row, ok := raw.([]any)
			if !ok || len(row) != len(columns) {
				continue
			}
			for ci, name := range columns {
				selector := map[string]any{"row_index": ri, "column_index": ci, "column_name": name}
				add(types.EvidenceKindStructuredResult, selector, map[string]any{name: row[ci]}, []string{name}, []any{[]any{row[ci]}}, columnUnit(ci), nil)
			}
		}
	} else if data["rows_are"] == "observations" {
		series, _ := data["series"].(map[string]any)
		for ri, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			value, exists := row["value"]
			if !exists {
				continue
			}
			values := map[string]any{"value": value}
			if date, ok := row["date"].(string); ok {
				values["date"] = date
			}
			unit := map[string]any{"status": "unknown"}
			for index, column := range contract.Columns {
				if column.Name == "value" {
					unit = columnUnit(index)
					break
				}
			}
			selector := map[string]any{"row_index": ri, "column_name": "value", "source_cell": row["source_cell"], "date_cell": row["date_cell"], "sheet": series["sheet"]}
			add(types.EvidenceKindRawCell, selector, values, []string{"date", "value"}, []any{[]any{row["date"], value}}, unit, nil)
		}
	}
	return ids
}

func structuredCellLocators(cells []any, digest string) types.SourceLocators {
	locators := types.SourceLocators{}
	seen := map[string]bool{}
	for _, raw := range cells {
		cell, ok := raw.(map[string]any)
		if !ok || citationString(cell["source_sha256"]) != digest {
			continue
		}
		page := citationInt(cell["page"])
		if page < 1 {
			continue
		}
		loc := types.SourceLocator{Type: types.SourceLocatorPDF, Page: page, SourceHash: digest, Mapping: "page"}
		box, _ := cell["bbox"].([]any)
		if cell["origin"] == "manual_transcription" {
			box = nil // transcription cannot invent an original cell rectangle
		}
		if len(box) != 4 {
			box, _ = cell["fragment_bbox"].([]any)
			if len(box) == 4 {
				loc.Mapping = "region"
			}
		} else {
			loc.Mapping = "exact"
		}
		if len(box) == 4 {
			valid := true
			for _, v := range box {
				n, ok := citationFloat(v)
				if !ok || n < 0 || n > 1 {
					valid = false
					break
				}
				loc.BBox = append(loc.BBox, n)
			}
			if !valid || loc.BBox[0] > loc.BBox[2] || loc.BBox[1] > loc.BBox[3] {
				loc.BBox = nil
				loc.Mapping = "page"
			}
		}
		key := reviewDigest(loc)
		if !seen[key] {
			seen[key] = true
			locators = append(locators, loc)
		}
	}
	return locators
}

func citationFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		v, err := n.Float64()
		return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	return 0, false
}

var structuredEvidenceCellAddress = regexp.MustCompile(`^\$?([A-Za-z]{1,3})\$?([1-9][0-9]{0,6})$`)

func structuredSheetLocators(selector map[string]any, digest string) types.SourceLocators {
	sheet := citationString(selector["sheet"])
	if sheet == "" {
		return nil
	}
	locators := types.SourceLocators{}
	seen := map[string]bool{}
	for _, key := range []string{"source_cell", "date_cell"} {
		address := citationString(selector[key])
		match := structuredEvidenceCellAddress.FindStringSubmatch(address)
		if len(match) != 3 || seen[address] {
			continue
		}
		seen[address] = true
		column := 0
		for _, letter := range strings.ToUpper(match[1]) {
			column = column*26 + int(letter-'A'+1)
		}
		var row int
		if _, err := fmt.Sscan(match[2], &row); err != nil || row > 1048576 || column > 16384 {
			continue
		}
		locators = append(locators, types.SourceLocator{Type: types.SourceLocatorSheet, Mapping: "exact", SourceHash: digest, Sheet: sheet, RowStart: row, RowEnd: row, ColumnStart: column, ColumnEnd: column})
	}
	return locators
}

func uniqueCandidateEvidenceIDs(candidate string) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, match := range answerEvidenceCitation.FindAllStringSubmatch(candidate, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			ids = append(ids, match[1])
		}
	}
	return ids
}

func (e *AgentEngine) currentStructuredObservation(id string, state *types.AgentState) (structuredCitationObservation, bool) {
	if e == nil || state == nil || e.modelContext == nil || e.modelContext.CitableEvidenceHandle(id) == "" {
		return structuredCitationObservation{}, false
	}
	observation, ok := e.turnReview.StructuredCitations[id]
	if !ok {
		return observation, false
	}
	found := false
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.ID != observation.CallID {
				continue
			}
			if call.Result == nil || !call.Result.Success || structuredCitationOperation(call) == "" || reviewDigest(call.Result.Data) != observation.DataHash {
				return observation, false
			}
			found = true
		}
	}
	return observation, found
}

func (e *AgentEngine) currentCandidateEvidenceReferences(candidate string, state *types.AgentState) types.EvidenceReferences {
	refs := types.EvidenceReferences{}
	for _, id := range uniqueCandidateEvidenceIDs(candidate) {
		if observation, ok := e.currentStructuredObservation(id, state); ok {
			ref := observation.Reference
			ref.CandidateHash = candidateTextDigest(candidate)
			ref.ClaimBindings = structuredInlineBindings(candidate, id)
			refs = append(refs, ref)
		}
	}
	return refs
}

func structuredInlineBindings(candidate, id string) json.RawMessage {
	occurrences := []any{}
	for _, match := range answerEvidenceCitation.FindAllStringSubmatchIndex(candidate, -1) {
		if candidate[match[2]:match[3]] != id {
			continue
		}
		start := utf8.RuneCountInString(candidate[:match[0]])
		end := start + utf8.RuneCountInString(candidate[match[0]:match[1]])
		prefix := []rune(candidate[:match[0]])
		from := max(0, len(prefix)-240)
		occurrences = append(occurrences, map[string]any{"start_rune": start, "end_rune": end, "context_start_rune": from, "adjacent_context": string(prefix[from:])})
	}
	encoded, _ := json.Marshal(map[string]any{"protocol": "inline_evidence_v1", "candidate_sha256": candidateTextDigest(candidate), "occurrences": occurrences, "notice": "The model chose the inline citation. These positions bind the exact final text; adjacent context is navigation, not automatic semantic proof."})
	return encoded
}

func (e *AgentEngine) structuredCitationReviewDirectory(candidate string, state *types.AgentState) map[string]any {
	items := []any{}
	ids := uniqueCandidateEvidenceIDs(candidate)
	omitted := 0
	for _, id := range ids {
		observation, ok := e.currentStructuredObservation(id, state)
		if !ok {
			items = append(items, map[string]any{"id": id, "status": "unknown_or_changed"})
			continue
		}
		entry := map[string]any{"id": id, "citation_handle": e.modelContext.CitableEvidenceHandle(id), "kind": observation.Reference.Kind, "result_hash": observation.Reference.ResultHash, "source": observation.Reference.Sources, "manifest": json.RawMessage(observation.Reference.Manifest), "binding": json.RawMessage(structuredInlineBindings(candidate, id)), "evidence_id": observation.Directory["evidence_id"], "json_pointer": observation.Directory["json_pointer"]}
		trial := append(append([]any(nil), items...), entry)
		encoded, _ := json.Marshal(trial)
		if len(encoded) > structuredCitationDirectoryBytes {
			omitted++
			continue
		}
		items = trial
	}
	return map[string]any{"items": items, "cited_count": len(ids), "omitted_count": omitted, "projection_complete": omitted == 0, "notice": "Current execution-backed table/result citations. Raw cells and computations are distinct. A dependency-only calculation does not claim source-cell lineage; same-page chunks cannot lend it evidence."}
}

func structuredCitationReaderID(call types.ToolCall) string {
	payload := sourceReviewResultPayload(call, nativeGeometryImageIndexes(call))
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return reviewEvidenceID(chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(encoded)})
}

func structuredCitationResultPointer(data, selector map[string]any) string {
	if selector == nil || selector["scope"] == "returned_result" {
		return "/data/rows"
	}
	pointer := "/data/rows/" + strconv.Itoa(citationInt(selector["row_index"]))
	if data["kind"] == "structured_analysis" {
		return pointer + "/" + strconv.Itoa(citationInt(selector["column_index"]))
	}
	name := strings.ReplaceAll(strings.ReplaceAll(citationString(selector["column_name"]), "~", "~0"), "/", "~1")
	if data["kind"] == "structured_source_inspection" {
		return pointer + "/values/" + name
	}
	return pointer + "/" + name
}

func (e *AgentEngine) sortedCurrentStructuredCitationIDs() []string {
	ids := make([]string, 0, len(e.turnReview.StructuredCitations))
	for id := range e.turnReview.StructuredCitations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (e *AgentEngine) finalAnswerEvidenceReferences(state *types.AgentState) types.EvidenceReferences {
	if e == nil || e.config == nil || !e.config.EvidenceReferencesEnabled || !e.config.CitationsEnabled() || state == nil {
		return nil
	}
	approved := e.candidateApprovalCurrent(state.FinalAnswer, state)
	if !approved {
		_, approved = e.numericWarningDelivery(state.FinalAnswer, state)
	}
	if !approved {
		return nil
	}
	refs := e.currentCandidateEvidenceReferences(state.FinalAnswer, state)
	refs = append(refs, e.finalChartEvidenceReferences(state, refs)...)
	if len(refs) > types.MaxMessageEvidenceReferences {
		return nil
	}
	return refs
}

// Refuse an undeliverable citation set before accepting its candidate. A
// bounded persistent store must not silently drop live tags at completion.
func (e *AgentEngine) candidateEvidenceBudgetFits(candidate string, state *types.AgentState) bool {
	if e == nil || e.config == nil || !e.config.EvidenceReferencesEnabled || !e.config.CitationsEnabled() || state == nil {
		return true
	}
	refs := e.currentCandidateEvidenceReferences(candidate, state)
	view := *state
	view.FinalAnswer = candidate
	refs = append(refs, e.finalChartEvidenceReferences(&view, refs)...)
	if len(refs) > types.MaxMessageEvidenceReferences {
		return false
	}
	total := 0
	for _, ref := range refs {
		if ref.ValidateStored() != nil {
			return false
		}
		encoded, err := json.Marshal(ref.StorageEnvelope())
		if err != nil {
			return false
		}
		total += len(encoded)
		if total > types.MaxMessageEvidenceBytes {
			return false
		}
	}
	return true
}
