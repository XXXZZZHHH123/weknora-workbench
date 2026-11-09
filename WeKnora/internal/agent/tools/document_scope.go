package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const ToolBindDocumentScope = "bind_document_scope"

// The QA model supplies source selection using its complete conversation and
// real authorized metadata. No question text is classified by this package.
type DocumentScopePlan struct {
	Sources []DocumentSourceSelection `json:"sources"`
	Reason  string                    `json:"selection_reason"`
}

func (p *DocumentScopePlan) UnmarshalJSON(data []byte) error {
	type planFields DocumentScopePlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fields planFields
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("来源计划包含多余内容")
	}
	*p = DocumentScopePlan(fields)
	return nil
}

type DocumentSourceSelection struct {
	KnowledgeID string                  `json:"knowledge_id"`
	FileName    string                  `json:"expected_file_name,omitempty"`
	Title       string                  `json:"expected_title,omitempty"`
	Page        int                     `json:"page,omitempty"`
	Pages       []int                   `json:"pages,omitempty"`
	UserPages   *DocumentPageConstraint `json:"user_page_constraint,omitempty"`
	Date        *DocumentDateEvidence   `json:"date_evidence,omitempty"`
}

// DocumentPageConstraint is a model-interpreted user restriction, distinct from
// the model's revisable reading hints. The exact quote must come from a trusted
// user message; it is not classified by keywords or page-number patterns.
type DocumentPageConstraint struct {
	Pages     []int  `json:"pages"`
	UserQuote string `json:"user_quote"`
}

type DocumentDateEvidence struct {
	Date        string `json:"date"`
	SourceField string `json:"source_field"`
	SourceText  string `json:"source_text"`
}

const documentScopePlanSchema = `{"type":"object","additionalProperties":false,"required":["sources","selection_reason"],"properties":{
"selection_reason":{"type":"string","minLength":1,"maxLength":2000,"description":"Explain source restrictions inferred from the complete user request and context. Ambiguous source dates/versions require clarification, not choosing the first candidate."},
"sources":{"type":"array","minItems":1,"maxItems":10,"items":{"type":"object","additionalProperties":false,"required":["knowledge_id"],"properties":{
"knowledge_id":{"type":"string","minLength":1,"description":"Actual authorized doc://N document handle from search/list metadata"},
"expected_file_name":{"type":"string","description":"Exact file_name from real source metadata; at least file_name or title is required"},
"expected_title":{"type":"string","description":"Exact title from real source metadata"},
"page":{"type":"integer","minimum":1,"description":"Revisable model-selected physical-page read hint, not a user restriction or chunk offset; use page or pages, not both"},
"pages":{"type":"array","minItems":1,"maxItems":8,"uniqueItems":true,"items":{"type":"integer","minimum":1},"description":"Revisable model-selected physical-page hints for one document; keep one source entry per document"},
"user_page_constraint":{"type":"object","additionalProperties":false,"required":["pages","user_quote"],"properties":{"pages":{"type":"array","minItems":1,"maxItems":64,"uniqueItems":true,"items":{"type":"integer","minimum":1}},"user_quote":{"type":"string","minLength":1,"maxLength":2000}},"description":"Only when the user explicitly restricts physical pages: model interprets the full request, lists all allowed pages and quotes the exact user text. A verified restriction persists for this turn and cannot be removed by a read plan."},
"date_evidence":{"type":"object","additionalProperties":false,"required":["date","source_field","source_text"],"description":"When the question constrains a source date, include actual date proof; an upload timestamp is not a source date.","properties":{
"date":{"type":"string","description":"Verified source date, YYYY-MM-DD"},"source_field":{"type":"string","enum":["file_name","title","metadata.report_date","metadata.published_date","metadata.date"]},"source_text":{"type":"string","minLength":1,"description":"Exact date substring in that actual source field"}}}
}}}}}`

type RequestedDocumentConstraint struct {
	mu           sync.RWMutex
	sources      map[string]DocumentSourceSelection
	order        []string
	userMessages []string
	userPages    map[string]*DocumentPageConstraint
}

func NewDocumentScopeState(userMessages ...string) *RequestedDocumentConstraint {
	return &RequestedDocumentConstraint{userMessages: append([]string(nil), userMessages...), userPages: map[string]*DocumentPageConstraint{}}
}

// IsCurrentDocumentRead checks a previously executed read against the current
// binding. Reusing a read must not bypass a later document/page restriction.
// Registries without a document binder keep their existing legacy behavior.
func IsCurrentDocumentRead(registry *ToolRegistry, result *types.ToolResult) bool {
	if registry == nil || result == nil || !result.Success {
		return false
	}
	tool, err := registry.GetTool(ToolBindDocumentScope)
	if err != nil {
		return true
	}
	binder, ok := tool.(*bindDocumentScopeTool)
	if !ok || binder.state == nil {
		return false
	}
	encoded, err := json.Marshal(result.Data["document_scope"])
	if err != nil {
		return false
	}
	var scope struct {
		Matched     bool   `json:"matched"`
		KnowledgeID string `json:"knowledge_id"`
		Page        int    `json:"page"`
		Pages       []int  `json:"pages"`
	}
	if json.Unmarshal(encoded, &scope) != nil || !scope.Matched {
		return false
	}
	selected, err := binder.state.selection(scope.KnowledgeID)
	if err != nil {
		return false
	}
	if selected.UserPages == nil {
		return true
	}
	pages := scope.Pages
	if scope.Page > 0 {
		pages = append(pages, scope.Page)
	}
	if len(pages) == 0 {
		return false
	}
	for _, page := range pages {
		if !slices.Contains(selected.UserPages.Pages, page) {
			return false
		}
	}
	return true
}

func (r *RequestedDocumentConstraint) selection(id string) (DocumentSourceSelection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.sources) == 0 {
		return DocumentSourceSelection{}, fmt.Errorf("尚未验证文档来源计划。请根据完整问题、上下文和真实授权目录提交 source_scope 或调用 bind_document_scope；不能把检索候选直接当指定报告的证据")
	}
	source, ok := r.sources[id]
	if !ok {
		return source, fmt.Errorf("该文档不在已验证来源计划中，不能用另一报告的同名图替代；比较或更改来源须明确提交完整来源计划")
	}
	return cloneDocumentSelection(source), nil
}

// An explicit read target is already a model source choice. Its caller must
// resolve and authorize the real document before passing it here. Bind only an
// empty turn boundary, atomically: a read never replaces an existing plan or
// broadens frozen user page restrictions. Search candidates alone do not call
// this method and remain navigation metadata.
func (r *RequestedDocumentConstraint) selectionForRead(knowledge *types.Knowledge) (DocumentSourceSelection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if knowledge == nil || knowledge.ID == "" {
		return DocumentSourceSelection{}, fmt.Errorf("读取目标缺少真实文档身份")
	}
	if len(r.sources) == 0 {
		if knowledge.FileName == "" && knowledge.Title == "" {
			return DocumentSourceSelection{}, fmt.Errorf("读取目标缺少实际文件元数据；请核对真实文档来源")
		}
		if len(r.userPages) > 0 && r.userPages[knowledge.ID] == nil {
			return DocumentSourceSelection{}, fmt.Errorf("读取目标不在用户已限定的文档页范围内")
		}
		selected := DocumentSourceSelection{KnowledgeID: knowledge.ID, FileName: knowledge.FileName, Title: knowledge.Title,
			UserPages: cloneDocumentPageConstraint(r.userPages[knowledge.ID])}
		r.sources = map[string]DocumentSourceSelection{knowledge.ID: selected}
		r.order = []string{knowledge.ID}
	}
	selected, ok := r.sources[knowledge.ID]
	if !ok {
		return DocumentSourceSelection{}, fmt.Errorf("该文档不在已验证来源计划中，不能用另一报告的同名图替代；比较或更改来源须明确提交完整来源计划")
	}
	if selected.FileName != "" && selected.FileName != knowledge.FileName || selected.Title != "" && selected.Title != knowledge.Title {
		return DocumentSourceSelection{}, fmt.Errorf("当前文档身份元数据与已验证来源不一致；请核对当前版本后重新提交来源计划")
	}
	if err := validateDocumentDateEvidence(knowledge, selected.Date); err != nil {
		return DocumentSourceSelection{}, err
	}
	return cloneDocumentSelection(selected), nil
}
func (r *RequestedDocumentConstraint) selectedIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}
func (r *RequestedDocumentConstraint) bind(ctx context.Context, plan DocumentScopePlan, service interfaces.KnowledgeService, targets types.SearchTargets) ([]map[string]any, error) {
	if strings.TrimSpace(plan.Reason) == "" || utf8.RuneCountInString(plan.Reason) > 2000 || len(plan.Sources) == 0 || len(plan.Sources) > 10 {
		return nil, fmt.Errorf("来源计划需要选择依据并包含1至10个真实文档；口径不明确时先澄清")
	}
	validated := map[string]DocumentSourceSelection{}
	order := []string{}
	metadata := []map[string]any{}
	for _, selection := range plan.Sources {
		if _, exists := validated[selection.KnowledgeID]; exists {
			return nil, fmt.Errorf("来源计划不能重复同一文档；同一来源的多个物理页请合并为一个条目的 pages 数组")
		}
		knowledge, err := authorizeKnowledgeInSearchTargets(ctx, targets, selection.KnowledgeID, service)
		if err != nil {
			return nil, err
		}
		if documentIsRetired(knowledge) {
			return nil, fmt.Errorf("指定文档已删除、正在删除或已归档，不能作为查询来源")
		}
		if selection.FileName == "" && selection.Title == "" {
			return nil, fmt.Errorf("来源计划必须提供真实目录的完整文件名或标题，不能只猜文档ID")
		}
		if selection.FileName != "" && selection.FileName != knowledge.FileName || selection.Title != "" && selection.Title != knowledge.Title {
			return nil, fmt.Errorf("来源计划文件名或标题与实际文档不一致；请重新核对真实目录")
		}
		if err := validateDocumentPageHints(selection); err != nil {
			return nil, err
		}
		if selection.UserPages != nil {
			if err := r.validateUserPageConstraint(selection.UserPages); err != nil {
				return nil, err
			}
		}
		selection = cloneDocumentSelection(selection)
		if err := validateDocumentDateEvidence(knowledge, selection.Date); err != nil {
			return nil, err
		}
		if selection.Date != nil {
			frozen := *selection.Date
			selection.Date = &frozen
		}
		validated[knowledge.ID] = selection
		order = append(order, knowledge.ID)
		row := documentScopeMetadata(knowledge)
		if selection.Page > 0 {
			row["read_hint_page"] = selection.Page
		}
		if len(selection.Pages) > 0 {
			row["read_hint_pages"] = append([]int(nil), selection.Pages...)
		}
		if selection.Date != nil {
			proof := *selection.Date
			row["verified_date_evidence"] = &proof
		}
		metadata = append(metadata, row)
	}
	r.mu.Lock()
	// Only commit after every source/constraint has passed validation. A later
	// model read plan may change hints, but cannot broaden a user restriction.
	for id, selection := range validated {
		if frozen := r.userPages[id]; frozen != nil {
			if selection.UserPages != nil && !sameDocumentPages(frozen.Pages, selection.UserPages.Pages) {
				r.mu.Unlock()
				return nil, fmt.Errorf("来源计划不能改变当前用户已限定的物理页；请在该范围内重新规划读取")
			}
			selection.UserPages = cloneDocumentPageConstraint(frozen)
		}
		if selection.UserPages != nil {
			for _, hint := range documentSelectionHints(selection) {
				if !slices.Contains(selection.UserPages.Pages, hint) {
					r.mu.Unlock()
					return nil, fmt.Errorf("读取计划页码超出用户明确限定的物理页")
				}
			}
		}
		validated[id] = selection
	}
	if r.userPages == nil {
		r.userPages = map[string]*DocumentPageConstraint{}
	}
	for id, selection := range validated {
		if selection.UserPages != nil {
			r.userPages[id] = cloneDocumentPageConstraint(selection.UserPages)
		}
	}
	for i, id := range order {
		if constraint := validated[id].UserPages; constraint != nil {
			metadata[i]["user_page_constraint"] = cloneDocumentPageConstraint(constraint)
		}
	}
	r.sources, r.order = validated, order
	r.mu.Unlock()
	return metadata, nil
}

// Validate explicit date parameters against actual metadata, never the question.
func validateDocumentDateEvidence(k *types.Knowledge, evidence *DocumentDateEvidence) error {
	if evidence == nil {
		return nil
	}
	date, err := time.Parse("2006-01-02", evidence.Date)
	if err != nil {
		return fmt.Errorf("来源日期证据必须为合法YYYY-MM-DD日期")
	}
	field := ""
	switch evidence.SourceField {
	case "file_name":
		field = k.FileName
	case "title":
		field = k.Title
	case "metadata.report_date", "metadata.published_date", "metadata.date":
		metadata, _ := k.Metadata.Map()
		field, _ = metadata[strings.TrimPrefix(evidence.SourceField, "metadata.")].(string)
	default:
		return fmt.Errorf("来源日期证据字段不受支持")
	}
	spellings := []string{date.Format("2006-01-02"), date.Format("20060102"), date.Format("2006/01/02"), date.Format("2006.01.02"), fmt.Sprintf("%d年%d月%d日", date.Year(), int(date.Month()), date.Day())}
	verified := false
	for _, spelling := range spellings {
		if evidence.SourceText == spelling {
			verified = true
			break
		}
	}
	if !verified || !strings.Contains(field, evidence.SourceText) {
		return fmt.Errorf("声明的来源日期与实际文件名、标题或来源元数据不一致；不能用上传日期或其他报告代替")
	}
	return nil
}
func documentScopeMetadata(k *types.Knowledge) map[string]any {
	row := map[string]any{"knowledge_id": k.ID, "knowledge_base_id": k.KnowledgeBaseID, "title": k.Title, "file_name": k.FileName, "file_type": k.FileType, "type": k.Type, "parse_status": k.ParseStatus}
	if metadata, err := k.Metadata.Map(); err == nil {
		dates := map[string]any{}
		for _, key := range []string{"report_date", "published_date", "date"} {
			if value, ok := metadata[key]; ok {
				dates[key] = value
			}
		}
		if len(dates) > 0 {
			row["metadata"] = dates
		}
	}
	return row
}
func withDocumentScopeParameter(tool BaseTool) BaseTool {
	var schema, parameter map[string]any
	if json.Unmarshal(tool.schema, &schema) != nil || json.Unmarshal([]byte(documentScopePlanSchema), &parameter) != nil {
		return tool
	}
	properties, _ := schema["properties"].(map[string]any)
	properties["source_scope"] = parameter
	tool.schema, _ = json.Marshal(schema)
	if tool.name == ToolReadDocument {
		tool.description += "\nChoose the actual read id from authorized metadata and the complete question/context. When this turn has no source binding, this explicit read target is verified against real authorized document metadata in the same call; a separate bind_document_scope call is unnecessary. An existing source plan or user document/page restriction remains binding. To change sources, compare multiple documents, or declare explicit source-date/user-page constraints, provide source_scope or bind_document_scope with the actual metadata and constraints. Never guess an ID or substitute a same-name source."
	} else {
		tool.description += "\nWhen this workbench requires a document source plan, inspect authorized document metadata first, then pass source_scope or bind_document_scope using the complete question/context: selected actual sources, exact metadata, requested pages/date proof and selection reason. Candidates are not content evidence until verified. Comparisons explicitly select multiple documents; do not decide scope from keywords."
	}
	return tool
}

type bindDocumentScopeTool struct {
	BaseTool
	state   *RequestedDocumentConstraint
	service interfaces.KnowledgeService
	targets types.SearchTargets
}

func (t *bindDocumentScopeTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var plan DocumentScopePlan
	if json.Unmarshal(args, &plan) != nil {
		return &types.ToolResult{Success: false, Error: "来源计划格式无效"}, nil
	}
	metadata, err := t.state.bind(ctx, plan, t.service, t.targets)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	data := map[string]any{"display_type": "document_info", "documents": metadata, "source_plan_validated": true, "notice": "已核验实际文件身份及声明的日期证据；这是来源选择，不是图表内容。page/pages 是可调整的读取计划；仅 user_page_constraint 是已验证用户页码限制。请读取实际证据，不混用其他报告。"}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}, nil
}

// Install a per-turn model-planned boundary. The full question/context stays in
// the normal QA messages and is never passed through a keyword classifier.
func PrepareDocumentScope(registry *ToolRegistry, userMessages ...string) {
	var service interfaces.KnowledgeService
	var targets types.SearchTargets
	searchTool, _ := registry.GetTool(ToolSearchKnowledge)
	readTool, _ := registry.GetTool(ToolReadDocument)
	search, _ := searchTool.(*SearchKnowledgeTool)
	read, _ := readTool.(*ReadDocumentTool)
	if search != nil {
		service, targets = search.knowledgeService, search.searchTargets
	} else if read != nil {
		service, targets = read.knowledgeService, read.searchTargets
	}
	if service == nil {
		return
	}
	state := NewDocumentScopeState(userMessages...)
	if existing, err := registry.GetTool(ToolBindDocumentScope); err == nil {
		if binder, ok := existing.(*bindDocumentScopeTool); ok {
			binder.state = state
			binder.service = service
			binder.targets = targets
		}
	} else {
		registry.RegisterTool(&bindDocumentScopeTool{BaseTool: NewBaseTool(ToolBindDocumentScope, "Select document sources from real authorized metadata using the full question and context. Submit exact IDs/file names/titles, date proof when constrained, revisable physical-page hints and a reason. User-imposed page limits must be separately provided in user_page_constraint with an exact user quote; model read hints never become user restrictions. Clarify ambiguous dates/versions rather than silently choosing a candidate. Comparisons explicitly include all compared sources. This validates source identity, not chart facts.", json.RawMessage(documentScopePlanSchema)), state: state, service: service, targets: targets})
	}
	if search != nil {
		search.WithDocumentConstraint(state)
	}
	if read != nil {
		read.WithDocumentConstraint(state)
	}
}

func (t *SearchKnowledgeTool) documentCandidates(ctx context.Context, results []*searchResultWithMeta, query string) *types.ToolResult {
	seen := map[string]bool{}
	documents := []map[string]any{}
	for _, result := range results {
		if seen[result.KnowledgeID] {
			continue
		}
		seen[result.KnowledgeID] = true
		knowledge, err := authorizeKnowledgeInSearchTargets(ctx, t.searchTargets, result.KnowledgeID, t.knowledgeService)
		if err != nil || documentIsRetired(knowledge) {
			continue
		}
		documents = append(documents, documentScopeMetadata(knowledge))
	}
	data := map[string]any{"display_type": "document_info", "documents": documents, "query": query, "requires_source_scope": true, "notice": "这些是实际授权文档候选元数据，不是内容证据。根据完整问题和上下文选择真实来源，歧义时澄清。可直接用 read_document 读取明确选择的文档ID，首次读取在同一调用核对真实文件身份和权限；已有来源计划不可被读取目标覆盖。比较或显式日期、用户页码限制使用 source_scope 或 bind_document_scope 声明完整范围，不得用其他报告的同名图替代。"}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: true, Data: data, Output: string(encoded)}
}
func documentIsRetired(k *types.Knowledge) bool {
	if k == nil || k.DeletedAt.Valid {
		return true
	}
	for _, status := range []string{k.ParseStatus, k.EnableStatus} {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case types.ParseStatusDeleting, "deleted", "archived":
			return true
		}
	}
	return false
}
func restrictDocumentTargets(ctx context.Context, targets types.SearchTargets, service interfaces.KnowledgeService, ids []string) (types.SearchTargets, error) {
	var result types.SearchTargets
	for _, id := range dedupNonEmptyStrings(ids) {
		k, err := authorizeKnowledgeInSearchTargets(ctx, targets, id, service)
		if err != nil {
			return nil, err
		}
		if documentIsRetired(k) {
			return nil, fmt.Errorf("指定文档已删除、正在删除或已归档，不能作为查询来源")
		}
		result = append(result, &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: k.KnowledgeBaseID, TenantID: targets.GetTenantIDForKB(k.KnowledgeBaseID), KnowledgeIDs: []string{k.ID}, DisableRecallThresholds: true})
	}
	return result, nil
}
