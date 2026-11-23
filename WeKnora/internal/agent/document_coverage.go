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

const documentComparisonGuidance = `When a task requires comparison across documents or periods, tie each claim to the period, physical page if verified, and original sentence or table heading. Check that the same period is not assigned to mutually exclusive groups. Distinguish separate tables in one document by their definitions, units, and time granularity. A navigation excerpt, a literal scan completion, or text from another physical page does not establish coverage. For every requested location, verify the exact fact in the original sentence or table, including its period and unit. A page marked as returned is still awaiting this fact check. Where the page is known, prefer a physical-page read for follow-up; otherwise use real navigation within the bound document. If evidence is still missing when work ends, name the specific document or period and the comparison that remains unresolved. Use plain language for readers and keep internal identifiers and offsets out of the answer.`

type documentCoverageEvidence struct {
	ChunkID  string `json:"chunk_id,omitempty"`
	Revision int    `json:"content_revision"`
	Kind     string `json:"kind"`
	Pages    []int  `json:"physical_pages,omitempty"`
}

type documentCoverageRow struct {
	KnowledgeBaseID string                           `json:"knowledge_base_id"`
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
	readPages       []int
	requiredPages   []int
	navigation      []map[string]any
}

type documentCoverageLocation struct {
	agenttools.DocumentCoverageRequirement
	Page       int    `json:"physical_page,omitempty"`
	Status     string `json:"status"`
	LastSearch string `json:"last_search_status,omitempty"`
	LastRead   string `json:"last_read_status,omitempty"`
	row        *documentCoverageRow
}

type documentCoverageView struct {
	Locations         []*documentCoverageLocation `json:"required_locations,omitempty"`
	Bound             []*documentCoverageRow      `json:"bound_documents"`
	Navigation        []map[string]any            `json:"navigation_candidates,omitempty"`
	DirectoryNextPage []map[string]any            `json:"directory_continuations,omitempty"`
	Notice            string                      `json:"notice"`
}

func (e *AgentEngine) documentCoverage(state *types.AgentState) *documentCoverageView {
	if state == nil || e.toolRegistry == nil || e.taskPlan == nil {
		return nil
	}
	active := false
	for _, target := range e.taskPlan.CoverageTargets {
		active = active || target.ByDocument || len(target.RequiredLocations) > 0
	}
	if !active {
		return nil
	}
	view := &documentCoverageView{Notice: "Actual authorized binding and returned evidence only. Navigation and search misses establish neither relevance nor absence. The model must judge whether the original request needs additional documents, and whether the actual text or table supports the requested facts."}
	bound := map[string]*documentCoverageRow{}
	for _, selected := range agenttools.BoundDocumentSelections(e.toolRegistry) {
		relevant := slices.ContainsFunc(selected.Coverage, func(binding agenttools.DocumentCoverageBinding) bool {
			return binding.Target >= 0 && binding.Target < len(e.taskPlan.CoverageTargets) &&
				(e.taskPlan.CoverageTargets[binding.Target].ByDocument || binding.Location != nil)
		})
		if !relevant {
			continue
		}
		row := &documentCoverageRow{
			KnowledgeBaseID: selected.KnowledgeBaseID, KnowledgeID: selected.KnowledgeID, Title: selected.Title, FileName: selected.FileName,
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
	for _, requirement := range agenttools.BoundDocumentCoverageRequirements(e.toolRegistry) {
		row := bound[requirement.KnowledgeID]
		pages := requirement.Pages
		if len(pages) == 0 {
			pages = []int{0}
		}
		for _, page := range pages {
			status := "not_attempted"
			if row == nil {
				status = "source_not_bound"
			} else if page > 0 && !slices.Contains(row.requiredPages, page) {
				row.requiredPages = append(row.requiredPages, page)
			}
			view.Locations = append(view.Locations, &documentCoverageLocation{
				DocumentCoverageRequirement: requirement, Page: page, Status: status, row: row,
			})
		}
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
					if row := bound[id]; row != nil && row.sameDocument(navigation) {
						navigated[id] = true
						row.navigation = append(row.navigation, sourceNavigationRows(navigation["navigation_matches"])...)
						if len(row.navigation) > 4 {
							row.navigation = row.navigation[len(row.navigation)-4:]
						}
						if date := discoveryReadFields(navigation, "metadata")["metadata"]; date != nil {
							row.DocumentDates = date
						}
						continue
					}
					if navigated[id] {
						continue
					}
					navigated[id] = true
					entry := discoveryReadFields(navigation, "knowledge_id", "knowledge_base_id", "title", "file_name", "metadata", "navigation_matches")
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
					if _, locationStatus := status["location_index"]; locationStatus {
						for _, location := range view.Locations {
							if location.row != nil && location.row.sameDocument(status) &&
								location.TargetIndex == coverageInt(status["target_index"]) &&
								location.LocationIndex == coverageInt(status["location_index"]) &&
								location.Page == coverageInt(status["page"]) {
								location.LastSearch = sourceNavigationString(status, "status")
							}
						}
						continue
					}
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
					row.sameDocument(data) && len(sourceNavigationRows(data["chunks"])) > 0 && agenttools.IsCurrentDocumentRead(e.toolRegistry, result) {
					row.observeRead(data)
				}
				for _, hit := range sourceNavigationRows(data["results"]) {
					if sourceNavigationString(hit, "content") == "" {
						continue
					}
					for _, origin := range citationRowsWithSearchOrigins(hit) {
						id := sourceNavigationString(origin, "knowledge_id")
						if row := bound[id]; row != nil && row.sameDocument(origin) &&
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
					row.observePageResults(data, call.ExecutionArgs())
					continue
				}
				if !row.sameDocument(data) || !agenttools.IsCurrentDocumentRead(e.toolRegistry, result) {
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
	for _, location := range view.Locations {
		if row := location.row; row != nil {
			switch {
			case location.Page == 0 && (row.bodyReturned || row.imageRead):
				location.Status = "page_unknown_fact_needs_review"
			case location.Page > 0 && slices.Contains(row.observedPages, location.Page):
				location.Status = "page_returned_fact_needs_review"
			case location.LastSearch != "" && location.LastSearch != "hit":
				location.Status = location.LastSearch
			case navigated[row.KnowledgeID]:
				location.Status = "navigation_only"
			}
			for _, result := range row.PageResults {
				if location.Page > 0 && coverageInt(result["page"]) == location.Page {
					switch {
					case result["success"] != true:
						location.LastRead = "failed"
					case result["page_complete"] != true || result["truncated"] == true:
						location.LastRead = "read_incomplete"
					default:
						location.LastRead = "page_read_complete"
					}
				}
			}
		}
	}
	return view
}

// The read and search adapters provide the authorized document identity.
// Some older read results put the library only in the enclosing result.
func (row *documentCoverageRow) sameDocument(data map[string]any) bool {
	return sourceNavigationString(data, "knowledge_id") == row.KnowledgeID &&
		citationString(data["knowledge_base_id"]) == row.KnowledgeBaseID
}

func (row *documentCoverageRow) observePageResults(data, args map[string]any) {
	results := sourceNavigationRows(data["page_results"])
	if len(results) == 0 {
		if page := coverageInt(data["page"]); page > 0 {
			results = []map[string]any{{"page": page, "success": len(sourceNavigationRows(data["chunks"])) > 0,
				"page_complete": data["page_complete"], "truncated": data["truncated"], "next_offset": data["next_offset"]}}
		} else if page := coverageInt(args["page"]); page > 0 {
			results = []map[string]any{{"page": page, "success": false}}
		} else if encoded, err := json.Marshal(args["pages"]); err == nil {
			var pages []int
			if json.Unmarshal(encoded, &pages) == nil {
				for _, page := range pages {
					results = append(results, map[string]any{"page": page, "success": false})
				}
			}
		}
	}
	for _, result := range results {
		page := coverageInt(result["page"])
		if page <= 0 {
			continue
		}
		row.PageResults = slices.DeleteFunc(row.PageResults, func(prior map[string]any) bool {
			return coverageInt(prior["page"]) == page
		})
		row.PageResults = append(row.PageResults, discoveryReadFields(result, "page", "success", "page_complete", "truncated", "next_offset"))
		if len(row.PageResults) > 8+len(row.requiredPages) {
			for i, result := range row.PageResults {
				if !slices.Contains(row.requiredPages, coverageInt(result["page"])) {
					row.PageResults = slices.Delete(row.PageResults, i, i+1)
					break
				}
			}
		}
	}
}

func (row *documentCoverageRow) observeRead(data map[string]any) {
	row.observePageResults(data, nil)
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
	evidence := documentCoverageEvidence{ChunkID: sourceNavigationString(row, "chunk_id"), Revision: coverageInt(row["content_revision"]), Kind: kind}
	if kind == "search_body" || kind == "read_body" {
		r.bodyReturned = true
		if row["content_offset_space"] == "physical_page_chunk_unicode_runes" {
			if page := coverageInt(row["page"]); page > 0 {
				evidence.Pages = append(evidence.Pages, page)
			}
		} else if locators, err := json.Marshal(row["source_locators"]); err == nil {
			var locations types.SourceLocators
			_ = json.Unmarshal(locators, &locations)
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
		if kind == "read_body" && !slices.Contains(r.readPages, page) {
			r.readPages = append(r.readPages, page)
		}
	}
	for _, prior := range r.Evidence {
		if prior.ChunkID == evidence.ChunkID && prior.Revision == evidence.Revision &&
			prior.Kind == evidence.Kind && slices.Equal(prior.Pages, evidence.Pages) {
			return
		}
	}
	r.Evidence = append(r.Evidence, evidence)
	if len(r.Evidence) > 6 {
		// Keep the only body for each required page even under a long read.
		for i, candidate := range r.Evidence {
			protect := false
			for _, page := range r.requiredPages {
				if slices.Contains(candidate.Pages, page) && !slices.ContainsFunc(r.Evidence, func(other documentCoverageEvidence) bool {
					return (other.ChunkID != candidate.ChunkID || other.Revision != candidate.Revision || other.Kind != candidate.Kind) &&
						(other.Kind == "read_body" || candidate.Kind != "read_body") && slices.Contains(other.Pages, page)
				}) {
					protect = true
					break
				}
			}
			if !protect {
				r.OmittedEvidence = true
				r.Evidence = slices.Delete(r.Evidence, i, i+1)
				break
			}
		}
	}
}

// Review the bodies behind current evidence, including uncited older and
// newer versions. Use the existing independently verified source views.
func (e *AgentEngine) documentCoverageReviewBodies(state *types.AgentState, conversation []chat.Message, coverage *documentCoverageView) map[string]any {
	type sourceIdentity struct {
		kb, document, chunk string
		revision            int
	}
	eligible := map[sourceIdentity]documentCoverageEvidence{}
	for _, row := range coverage.Bound {
		for _, evidence := range row.Evidence {
			if evidence.Kind == "search_body" || evidence.Kind == "read_body" {
				eligible[sourceIdentity{row.KnowledgeBaseID, row.KnowledgeID, evidence.ChunkID, evidence.Revision}] = evidence
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
	type reviewPage struct {
		kb, document string
		page         int
	}
	pagesFor := func(view map[string]any) []reviewPage {
		key := sourceIdentity{citationString(view["knowledge_base_id"]), citationString(view["knowledge_id"]),
			citationString(view["chunk_id"]), coverageInt(view["content_revision"])}
		evidence := eligible[key]
		var pages []reviewPage
		for _, location := range coverage.Locations {
			if location.KnowledgeID == key.document && location.KnowledgeBaseID == key.kb &&
				(location.Page == 0 || slices.Contains(evidence.Pages, location.Page)) {
				// A page-clipped view carries only its own physical page.
				if page := coverageInt(view["page"]); location.Page == 0 || page == 0 || page == location.Page {
					value := reviewPage{key.kb, key.document, location.Page}
					if !slices.Contains(pages, value) {
						pages = append(pages, value)
					}
				}
			}
		}
		return pages
	}
	var retained []map[string]any
	for _, view := range views {
		if _, ok := eligible[sourceIdentity{citationString(view["knowledge_base_id"]), citationString(view["knowledge_id"]),
			citationString(view["chunk_id"]), coverageInt(view["content_revision"])}]; ok {
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
		// Remove a redundant view before discarding the sole view of a
		// requested physical page or the only body from a document.
		counts := map[reviewPage]int{}
		byView := make([][]reviewPage, len(retained))
		for i, view := range retained {
			byView[i] = pagesFor(view)
			doc := reviewPage{citationString(view["knowledge_base_id"]), citationString(view["knowledge_id"]), -1}
			byView[i] = append(byView[i], doc)
			for _, page := range byView[i] {
				counts[page]++
			}
		}
		drop, bestLoss, largest := 0, len(counts)*2+1, 0
		for i, view := range retained {
			loss := 0
			for _, page := range byView[i] {
				if counts[page] == 1 {
					loss++
					if page.page >= 0 {
						loss++
					}
				}
			}
			size := len(citationString(view["content"]))
			if loss < bestLoss || loss == bestLoss && size > largest {
				drop, bestLoss, largest = i, loss, size
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
	for _, location := range v.Locations {
		if location.Status == "page_returned_fact_needs_review" || location.Status == "page_unknown_fact_needs_review" {
			continue
		}
		label := coverageLabel(location.row)
		if row := location.row; row != nil && row.Title != "" && row.FileName != "" &&
			slices.ContainsFunc(v.Bound, func(other *documentCoverageRow) bool { return other != row && other.Title == row.Title }) {
			label = "《" + cleanCoverageText(row.FileName) + "》"
		}
		if period := cleanCoverageText(location.Period); period != "" {
			label += period + " "
		}
		if location.Page > 0 {
			label += fmt.Sprintf("第 %d 页", location.Page)
		}
		fact := cleanCoverageText(location.Fact)
		if location.row == nil {
			gaps = append(gaps, fmt.Sprintf("%s尚未找到可核对“%s”的资料", label, fact))
		} else {
			gaps = append(gaps, fmt.Sprintf("%s尚未取得可核对“%s”的内容", label, fact))
		}
	}
	for _, row := range v.Bound {
		label := coverageLabel(row)
		if label == "" {
			continue
		}
		var missingPages []string
		for _, page := range row.UserPages {
			if !slices.Contains(row.observedPages, page) && !slices.Contains(row.requiredPages, page) {
				missingPages = append(missingPages, strconv.Itoa(page))
			}
		}
		switch {
		case len(missingPages) > 0:
			gaps = append(gaps, fmt.Sprintf("%s第 %s 页尚未取得可核对的内容", label, strings.Join(missingPages, "、")))
		case slices.ContainsFunc(v.Locations, func(location *documentCoverageLocation) bool { return location.row == row }):
			continue
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

func coverageLabel(row *documentCoverageRow) string {
	if row == nil {
		return ""
	}
	label := row.Title
	if label == "" {
		label = row.FileName
	}
	if label == "" {
		return ""
	}
	return "《" + cleanCoverageText(label) + "》"
}

func coverageInt(value any) int {
	if number, ok := value.(float64); ok && number >= 0 && float64(int(number)) == number {
		return int(number)
	}
	return citationInt(value)
}

func cleanCoverageText(text string) string {
	return html.EscapeString(strings.Join(strings.Fields(text), " "))
}
