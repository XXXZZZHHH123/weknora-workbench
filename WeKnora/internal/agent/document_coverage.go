package agent

import (
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const documentComparisonGuidance = `When a task requires comparison across documents or periods, tie each claim to the period, physical page if verified, and original sentence or table heading. Check that the same period is not assigned to mutually exclusive groups. Distinguish separate tables in one document by their definitions, units, and time granularity. A navigation excerpt or a search hit does not establish coverage of the requested facts. If evidence is still missing when work ends, name the specific document or period and the comparison that remains unresolved. Use plain language for readers and keep internal identifiers and offsets out of the answer.`

type documentCoverageEvidence struct {
	ChunkID  string `json:"chunk_id,omitempty"`
	Revision int    `json:"content_revision"`
	Kind     string `json:"kind"`
	Pages    []int  `json:"physical_pages,omitempty"`
}

type documentCoverageRow struct {
	KnowledgeID     string                           `json:"knowledge_id"`
	Title           string                           `json:"title,omitempty"`
	FileName        string                           `json:"file_name,omitempty"`
	DateEvidence    *agenttools.DocumentDateEvidence `json:"verified_date_evidence,omitempty"`
	DocumentDates   any                              `json:"document_date_metadata,omitempty"`
	ReadHints       []int                            `json:"planned_read_pages,omitempty"`
	UserPages       []int                            `json:"user_requested_pages,omitempty"`
	Status          string                           `json:"status"`
	LastSearch      string                           `json:"last_search_status,omitempty"`
	PageResults     []map[string]any                 `json:"latest_physical_page_results,omitempty"`
	Evidence        []documentCoverageEvidence       `json:"evidence,omitempty"`
	OmittedEvidence bool                             `json:"additional_evidence_omitted,omitempty"`
	imageRead       bool
	bodyReturned    bool
	observedPages   []int
}

type documentCoverageView struct {
	Bound             []*documentCoverageRow `json:"bound_documents"`
	Navigation        []map[string]any       `json:"navigation_candidates,omitempty"`
	DirectoryNextPage []map[string]any       `json:"directory_continuations,omitempty"`
	Notice            string                 `json:"notice"`
}

func (e *AgentEngine) documentCoverage(state *types.AgentState) *documentCoverageView {
	if state == nil || e.toolRegistry == nil || !e.plansCoverageByDocument() {
		return nil
	}
	view := &documentCoverageView{Notice: "Actual authorized binding and returned evidence only. Navigation and search misses establish neither relevance nor absence. The model must judge whether the original request needs additional documents, and whether the actual text or table supports the requested facts."}
	bound := map[string]*documentCoverageRow{}
	for _, selected := range agenttools.BoundDocumentSelections(e.toolRegistry) {
		row := &documentCoverageRow{
			KnowledgeID: selected.KnowledgeID, Title: selected.Title, FileName: selected.FileName,
			DateEvidence: selected.Date, ReadHints: selected.Pages, Status: "not_attempted",
		}
		if selected.Page > 0 {
			row.ReadHints = []int{selected.Page}
		}
		if selected.UserPages != nil {
			row.UserPages = selected.UserPages.Pages
		}
		view.Bound = append(view.Bound, row)
		bound[row.KnowledgeID] = row
	}
	navigated := map[string]bool{}
	type directoryKey struct{ kb, filter string }
	var directories []directoryKey
	continuations := map[directoryKey]map[string]any{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			operation, native := e.sourceProgressOperation(call)
			if !native || call.Result.PreflightRejected {
				continue
			}
			result, data := call.Result, call.Result.Data
			if result.Success && (operation == agenttools.ToolSearchKnowledge || operation == agenttools.ToolListDocuments) {
				for _, navigation := range sourceNavigationRows(data["documents"]) {
					id := sourceNavigationString(navigation, "knowledge_id")
					if id == "" {
						continue
					}
					if row := bound[id]; row != nil {
						navigated[id] = true
						if date := discoveryReadFields(navigation, "metadata")["metadata"]; date != nil {
							row.DocumentDates = date
						}
						continue
					}
					if navigated[id] {
						continue
					}
					navigated[id] = true
					entry := discoveryReadFields(navigation, "knowledge_id", "knowledge_base_id", "title", "file_name", "metadata")
					view.Navigation = append(view.Navigation, entry)
					if len(view.Navigation) > 12 {
						view.Navigation = view.Navigation[1:]
					}
				}
			}
			switch operation {
			case agenttools.ToolListDocuments:
				if !result.Success {
					continue
				}
				kb := sourceNavigationString(data, "knowledge_base_id")
				key := directoryKey{kb, sourceNavigationString(data, "keyword")}
				if _, seen := continuations[key]; !seen {
					directories = append(directories, key)
				}
				continuations[key] = nil
				if next := data["next_page"]; next != nil {
					continuations[key] = discoveryReadFields(data, "knowledge_base_id", "keyword", "next_page", "page_size")
				}
			case agenttools.ToolSearchKnowledge:
				for _, status := range sourceNavigationRows(data["document_coverage"]) {
					if row := bound[sourceNavigationString(status, "knowledge_id")]; row != nil {
						row.LastSearch = sourceNavigationString(status, "status")
						if row.LastSearch != "hit" {
							row.Status = row.LastSearch
						}
					}
				}
				if !result.Success {
					continue
				}
				// Explicit page searches run through the native reader.
				if row := bound[sourceNavigationString(data, "knowledge_id")]; row != nil &&
					len(sourceNavigationRows(data["chunks"])) > 0 && agenttools.IsCurrentDocumentRead(e.toolRegistry, result) {
					row.observeRead(data)
				}
				for _, hit := range sourceNavigationRows(data["results"]) {
					if sourceNavigationString(hit, "content") == "" {
						continue
					}
					origins := append([]map[string]any{hit}, sourceNavigationRows(hit["origins"])...)
					for _, origin := range origins {
						id := sourceNavigationString(origin, "knowledge_id")
						if row := bound[id]; row != nil && origin["navigation_only"] != true &&
							agenttools.IsCurrentDocumentSearchChunk(e.toolRegistry, result, id) {
							row.addEvidence(origin, "search_body")
						}
					}
				}
			case agenttools.ToolReadDocument:
				id := sourceNavigationString(data, "knowledge_id")
				if id == "" {
					id = sourceNavigationString(call.ExecutionArgs(), "id")
				}
				row := bound[id]
				if row == nil {
					continue
				}
				if !result.Success {
					row.Status = "failed"
					continue
				}
				if !agenttools.IsCurrentDocumentRead(e.toolRegistry, result) {
					continue
				}
				row.observeRead(data)
			}
		}
	}
	for _, key := range directories {
		if entry := continuations[key]; entry != nil && len(view.DirectoryNextPage) < 8 {
			view.DirectoryNextPage = append(view.DirectoryNextPage, entry)
		}
	}
	for _, row := range view.Bound {
		switch {
		case row.imageRead:
			row.Status = "original_image_or_geometry_read"
		case row.bodyReturned:
			row.Status = "body_returned"
		case row.Status == "not_attempted" && navigated[row.KnowledgeID]:
			row.Status = "navigation_only"
		}
	}
	return view
}

func (row *documentCoverageRow) observeRead(data map[string]any) {
	row.PageResults = nil
	for _, page := range sourceNavigationRows(data["page_results"]) {
		row.PageResults = append(row.PageResults, discoveryReadFields(page, "page", "success", "page_complete", "truncated"))
	}
	if data["truncated"] == true || data["pages_complete"] == false || data["page_complete"] == false {
		row.Status = "read_incomplete"
	}
	for _, chunk := range sourceNavigationRows(data["chunks"]) {
		if sourceNavigationString(chunk, "content") != "" {
			row.addEvidence(chunk, "read_body")
		}
	}
	for _, image := range sourceNavigationRows(data["image_evidence"]) {
		if sourceNavigationString(image, "knowledge_id") != row.KnowledgeID {
			continue
		}
		if image["read_success"] == true {
			row.addEvidence(image, "original_image_returned")
		}
		if image["geometry_read_success"] == true {
			row.addEvidence(image, "original_geometry_read")
		}
	}
	if !row.bodyReturned && !row.imageRead && row.Status != "read_incomplete" {
		row.Status = "no_match"
	}
}

func (r *documentCoverageRow) addEvidence(row map[string]any, kind string) {
	evidence := documentCoverageEvidence{ChunkID: sourceNavigationString(row, "chunk_id"), Revision: citationInt(row["content_revision"]), Kind: kind}
	if kind == "search_body" || kind == "read_body" {
		r.bodyReturned = true
		locators, err := json.Marshal(row["source_locators"])
		var locations types.SourceLocators
		if err == nil && json.Unmarshal(locators, &locations) == nil {
			for _, locator := range locations {
				if locator.Mapping != "exact" {
					continue
				}
				var page int
				switch locator.Type {
				case types.SourceLocatorPDF:
					page = locator.Page
				case types.SourceLocatorSlide:
					page = locator.Slide
				}
				if page > 0 && !slices.Contains(evidence.Pages, page) {
					evidence.Pages = append(evidence.Pages, page)
				}
			}
		}
		if row["content_offset_space"] == "physical_page_chunk_unicode_runes" {
			if page := citationInt(row["page"]); page > 0 && !slices.Contains(evidence.Pages, page) {
				evidence.Pages = append(evidence.Pages, page)
			}
		}
	} else {
		r.imageRead = true
		// An image's display hint is not proof of a physical page. Prefer
		// the matching actual body locator when the image carries that ID.
		for _, source := range r.Evidence {
			if evidence.ChunkID != "" && source.ChunkID == evidence.ChunkID {
				evidence.Pages = append([]int(nil), source.Pages...)
				break
			}
		}
	}
	slices.Sort(evidence.Pages)
	for _, page := range evidence.Pages {
		if !slices.Contains(r.observedPages, page) {
			r.observedPages = append(r.observedPages, page)
		}
	}
	for _, prior := range r.Evidence {
		if prior.ChunkID == evidence.ChunkID && prior.Revision == evidence.Revision &&
			prior.Kind == evidence.Kind && slices.Equal(prior.Pages, evidence.Pages) {
			return
		}
	}
	if len(r.Evidence) >= 6 {
		r.OmittedEvidence = true
		r.Evidence = r.Evidence[1:]
	}
	r.Evidence = append(r.Evidence, evidence)
}

// Review the bodies behind current evidence, including uncited older and
// newer versions. Use the existing independently verified source views.
func (e *AgentEngine) documentCoverageReviewBodies(state *types.AgentState, conversation []chat.Message, coverage *documentCoverageView) map[string]any {
	type sourceIdentity struct {
		document, chunk string
		revision        int
	}
	eligible := map[sourceIdentity]bool{}
	for _, row := range coverage.Bound {
		for _, evidence := range row.Evidence {
			if evidence.Kind == "search_body" || evidence.Kind == "read_body" {
				eligible[sourceIdentity{row.KnowledgeID, evidence.ChunkID, evidence.Revision}] = true
			}
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	views, valid := e.currentReadDocumentReviewEvidence(state, conversation)
	if !valid {
		return map[string]any{"verified_body_views_unavailable": true}
	}
	var retained []map[string]any
	for _, view := range views {
		if eligible[sourceIdentity{citationString(view["knowledge_id"]), citationString(view["chunk_id"]), citationInt(view["content_revision"])}] {
			retained = append(retained, view)
		}
	}
	// Share exact bodies before enforcing the budget. Omitted evidence cannot
	// be used by the reviewer to infer absence or a failed source read.
	omitted := len(views) - len(retained)
	for len(retained) > 0 {
		groups, bodies, valid := compactDocumentReviewBodies(retained)
		if !valid {
			break
		}
		input := map[string]any{"documents": groups, "shared_bodies": bodies, "omitted_views": omitted}
		if sourceReviewProjectionSize(input) <= 11*1024 {
			return input
		}
		// Drop surplus views from the most represented document first.
		counts := map[string]int{}
		for _, view := range retained {
			counts[citationString(view["knowledge_id"])]++
		}
		drop := 0
		for i, view := range retained {
			if counts[citationString(view["knowledge_id"])] > counts[citationString(retained[drop]["knowledge_id"])] {
				drop = i
			}
		}
		retained = slices.Delete(retained, drop, drop+1)
		omitted++
	}
	return map[string]any{"omitted_views": omitted, "verified_body_views_unavailable": true}
}

func (v *documentCoverageView) gapNotice() string {
	if v == nil {
		return ""
	}
	var gaps []string
	for _, row := range v.Bound {
		label := row.Title
		if label == "" {
			label = row.FileName
		}
		if label == "" {
			continue
		}
		label = "《" + html.EscapeString(strings.Join(strings.Fields(label), " ")) + "》"
		var missingPages []string
		for _, page := range row.UserPages {
			if !slices.Contains(row.observedPages, page) {
				missingPages = append(missingPages, strconv.Itoa(page))
			}
		}
		switch {
		case len(missingPages) > 0:
			gaps = append(gaps, fmt.Sprintf("%s第 %s 页尚未取得可核对的内容", label, strings.Join(missingPages, "、")))
		case row.Status == "not_attempted" || row.Status == "navigation_only":
			gaps = append(gaps, label+"尚未读到正文")
		case row.Status == "failed":
			gaps = append(gaps, label+"本轮读取失败")
		case row.Status == "budget_truncated" || row.Status == "limit_truncated" || row.Status == "read_incomplete":
			gaps = append(gaps, label+"因本轮读取或展示限额未取得完整结果")
		case row.Status == "no_match":
			gaps = append(gaps, label+"本轮检索未返回相关内容")
		}
	}
	if len(gaps) == 0 {
		return ""
	}
	return "本轮取证仍有缺口，" + strings.Join(gaps, "。") + "。"
}
