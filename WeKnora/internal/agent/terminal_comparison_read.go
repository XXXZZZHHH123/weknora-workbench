package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

type finalTableReadings struct {
	observations  []coverageEvidence
	focusedFields []string
	inspected     coverageEvidence
}

type finalTableSelection struct {
	source                 [2]string
	ref, chunk, row, field string
	page                   int
	labels                 []any
}

// A focused native read can locate the exact field for a broader model
// goal. The locator can identify an actual workbook cell to deliver, but
// supplies no observation for the document until its image was read.
func (e *AgentEngine) finalTableSelections(ctx context.Context, target taskCoverageTarget, reads []coverageAttempt) []finalTableSelection {
	from, to, grain := coverageTargetDateRange(target)
	var selected []finalTableSelection
	for _, attempt := range reads {
		data := attempt.result.Data
		library, id := sourceNavigationString(data, "knowledge_base_id"), sourceNavigationString(data, "knowledge_id")
		if !e.coverageEvidenceAllowed(ctx, &target, coverageEvidence{SourceDomain: "knowledge", SourceID: id, KnowledgeBaseID: library}) {
			continue
		}
		for _, table := range sourceNavigationRows(data["table_candidates"]) {
			if table["title"] != target.Table {
				continue
			}
			item := finalTableSelection{source: [2]string{library, id}, ref: sourceNavigationString(table, "figure_id"),
				chunk: sourceNavigationString(table, "body_chunk_id"), page: citationInt(table["page"]), labels: reviewAsSlice(table["row_labels"])}
			if item.ref == "" || item.chunk == "" || item.page <= 0 {
				continue
			}
			columns := reviewAsSlice(table["column_headers"])
			query := sourceNavigationString(attempt.Args, "query")
			for _, raw := range item.labels {
				row, _ := raw.(string)
				if !comparisonNativeRowPeriod(row, from, to, grain) {
					continue
				}
				before, after, hasRow := strings.Cut(query, row)
				before, after = strings.TrimSpace(before), strings.TrimSpace(after)
				focusedField := ""
				if hasRow && (before == "" || after == "") {
					focusedField = before + after
				}
				for _, raw := range columns {
					field, _ := raw.(string)
					if field != "" && (field == target.Metric || field == focusedField) {
						item.row, item.field = row, field
						selected = append(selected, item)
					}
				}
			}
		}
	}
	return selected
}

func (e *AgentEngine) finalVisualTableReadings(ctx context.Context, target taskCoverageTarget, record coverageRecord, attempts []coverageAttempt) finalTableReadings {
	var reads []coverageAttempt
	for _, attempt := range attempts {
		if attempt.Operation == agenttools.ToolReadDocument && attempt.Success && agenttools.IsCurrentDocumentRead(e.toolRegistry, attempt.result) {
			reads = append(reads, attempt)
		}
	}
	selections := e.finalTableSelections(ctx, target, reads)
	var result finalTableReadings
	for _, selection := range selections {
		if !slices.Contains(result.focusedFields, selection.field) {
			result.focusedFields = append(result.focusedFields, selection.field)
		}
	}
	for _, attempt := range reads {
		data := attempt.result.Data
		descriptions := reviewAsSlice(data["image_descriptions"])
		if len(descriptions) == 0 {
			continue
		}
		for _, read := range sourceNavigationRows(data["image_evidence"]) {
			index := coverageOffset(read["returned_image_index"])
			ref, id, chunk := sourceNavigationString(read, "reference"), sourceNavigationString(read, "knowledge_id"), sourceNavigationString(read, "chunk_id")
			page := citationInt(read["page"])
			if read["read_success"] != true || index == nil || *index < 0 || *index >= len(descriptions) ||
				ref == "" || id == "" || id != data["knowledge_id"] || chunk == "" || page <= 0 ||
				!coverageContainsString(data["chunk_ids"], chunk) {
				continue
			}
			if _, stored := types.ParseResourcePath(ref); !stored {
				if _, valid := reviewOriginalMediaCatalogReference(data, read, ref); !valid {
					continue
				}
			}
			if citationInt(data["page"]) != page && !slices.ContainsFunc(sourceNavigationRows(data["image_catalog"]), func(image map[string]any) bool {
				return image["page_verified"] == true && reviewImageIdentityMatches(read, image)
			}) {
				continue
			}
			description, _ := descriptions[*index].(string)
			if description == "" {
				continue
			}
			item := coverageEvidence{
				Kind: "body", Operation: attempt.Operation, SourceDomain: "knowledge", SourceID: id, KnowledgeID: id,
				SourceTitle: sourceNavigationString(data, "knowledge_title"), KnowledgeBaseID: sourceNavigationString(data, "knowledge_base_id"),
				ChunkID: chunk, Object: ref, Page: page, Sheet: target.Table, Bound: true,
				EvidenceID: attempt.EvidenceID, ToolCallID: attempt.ToolCallID, JSONPointer: fmt.Sprintf("/data/image_descriptions/%d", *index),
				Revision: record.Revision, AuthScope: record.AuthScope, ContentHash: candidateTextDigest(description),
			}
			sourceMetadata := reviewAsMap(reviewAsMap(reviewAsMap(data["document"])["metadata"])["_content_processing_v1"])
			item.SnapshotHash = sourceNavigationString(sourceMetadata, "source_hash")
			if item.SourceTitle == "" || !e.coverageEvidenceAllowed(ctx, &target, item) {
				continue
			}
			for _, selection := range selections {
				if selection.source != comparisonPhysicalSource(item) || selection.ref != ref || selection.chunk != chunk || selection.page != page {
					continue
				}
				if value := finalVisualTableValue(description, selection.labels, selection.row, selection.field); value != "" {
					fact := item
					fact.Metric, fact.Column = selection.field, selection.field
					fact.Date, fact.RowLabel, fact.Period = selection.row, selection.row, selection.row
					fact.Raw, fact.ReportedValue = value, value
					result.observations = append(result.observations, fact)
				}
			}
		}
	}
	return result
}

func finalVisualTableValue(description string, rowLabels []any, row, field string) string {
	if value, ok := comparisonTableIntersection(description, field, row, false); ok {
		return value
	}
	// Visual descriptions may return a row heading followed by labeled
	// values. Read only a complete labeled value within the named row.
	activeRow, result := "", ""
	for _, line := range strings.Split(documentSourceProse(description), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if bullet := strings.TrimPrefix(line, "- "); bullet != line {
			label, value, ok := strings.Cut(strings.Replace(bullet, "：", ":", 1), ":")
			if !ok || activeRow != row || strings.Trim(strings.TrimSpace(label), "*`") != field {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), "*`")
			if documentTableNumber.MatchString(value) {
				if result != "" && result != value {
					return ""
				}
				result = value
			}
			continue
		}
		activeRow = ""
		for _, raw := range rowLabels {
			label, _ := raw.(string)
			if label == "" || !strings.Contains(line, label) {
				continue
			}
			if activeRow != "" && activeRow != label {
				activeRow = ""
				break
			}
			activeRow = label
		}
	}
	return result
}
