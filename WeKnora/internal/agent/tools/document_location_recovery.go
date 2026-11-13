package tools

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// A PDF's text view and published table view can have different location
// metadata. A missing text-page mapping is not proof that both lack it.
// This hint issues no table IDs, page numbers, or permission grants.
func annotatePDFLocationRecovery(knowledge *types.Knowledge, result *types.ToolResult) {
	if knowledge == nil || result == nil || result.Data == nil || !strings.EqualFold(strings.TrimPrefix(knowledge.FileType, "."), "pdf") {
		return
	}
	if result.Success && (result.Data["page"] != nil || result.Data["all_pages_succeeded"] == true) {
		return
	}
	if result.Success && result.Data["requested_pages"] == nil {
		if rows, ok := result.Data["chunks"].([]map[string]any); ok {
			for _, row := range rows {
				content, _ := row["content"].(string)
				if documentPageMarker.MatchString(content) || row["page"] != nil || row["physical_page"] != nil {
					return // This view already has a physical navigation channel.
				}
			}
		}
	}
	result.Data["location_recovery"] = map[string]any{
		"knowledge_base_id": knowledge.KnowledgeBaseID,
		"knowledge_id":      knowledge.ID,
		"file_name":         knowledge.FileName,
		"file_hash":         knowledge.FileHash,
		"text_view_does_not_establish_requested_physical_location": true,
		"published_table_location_checked_by_this_read":            false,
		"notice": "If physical location is required, discover the currently available structured PDF tools (structured_capabilities / expand_task_tools). In this same authorized knowledge base, list_pdf_tables can discover published tables; verify the original file identity/version against this document before choosing a real table_id. read_pdf_table preserves per-cell source_cells page coordinates, including rows merged across pages. No table existence, publication status, page number or permission is established by this hint. Keep physical pages distinct from printed labels; never infer pages from chunk or row ordinals. Return only verified location evidence and retain the user's explicit source/page limits.",
	}
}
