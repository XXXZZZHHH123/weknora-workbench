package session

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/filetransport"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const evidenceOriginalByteLimit = 100 << 20

type authorizedEvidenceSource struct {
	snapshot  types.EvidenceSourceSnapshot
	knowledge *types.Knowledge
	ctx       context.Context
}

type evidenceSourceView struct {
	KnowledgeBaseID string               `json:"knowledge_base_id"`
	KnowledgeID     string               `json:"knowledge_id"`
	Title           string               `json:"title"`
	FileName        string               `json:"file_name"`
	SourceSHA256    string               `json:"source_sha256"`
	Locators        types.SourceLocators `json:"locators,omitempty"`
}

// GetMessageEvidence returns a view of immutable proof, never a reusable
// execution/reviewer receipt. Knowing a reference ID grants no source access.
func (h *Handler) GetMessageEvidence(c *gin.Context) {
	message, reference, err := h.messageEvidence(c)
	if err != nil {
		c.Error(errors.NewNotFoundError("证据不存在或无权访问"))
		return
	}
	c.Header("Cache-Control", "private, no-store")
	sources, err := h.authorizeEvidenceSources(c.Request.Context(), message, reference)
	if err != nil {
		evidenceUnavailable(c, reference, "unavailable", "来源已删除、范围已变化或无权访问")
		return
	}
	remaining := int64(evidenceOriginalByteLimit)
	for _, source := range sources {
		if source.snapshot.SourceSHA256 == "" {
			evidenceUnavailable(c, reference, "unavailable", "历史证据未保存原件版本，无法确认原定位")
			return
		}
		if source.knowledge.FileSize > remaining {
			evidenceUnavailable(c, reference, "unavailable", "原件版本核验超过单次预览范围")
			return
		}
		if _, err := h.readEvidenceOriginal(source, remaining); err != nil {
			evidenceUnavailable(c, reference, "stale", "来源已更新或原件版本无法读取，原定位不可用")
			return
		}
		remaining -= source.knowledge.FileSize
	}
	views := make([]evidenceSourceView, len(sources))
	for index, source := range sources {
		views[index] = evidenceSourceView{KnowledgeBaseID: source.knowledge.KnowledgeBaseID, KnowledgeID: source.knowledge.ID, Title: source.knowledge.Title, FileName: source.knowledge.FileName, SourceSHA256: source.snapshot.SourceSHA256, Locators: source.snapshot.Locators}
	}
	// Snapshot refers to the historical execution/plan. Equal original bytes do
	// not claim that the currently published table record/plan is unchanged.
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"reference": reference.PublicSummary(), "status": "snapshot", "sources": views, "locators": reference.Locators, "manifest": publicEvidenceManifest(reference.Manifest)}})
}

func evidenceUnavailable(c *gin.Context, ref *types.EvidenceReference, status, message string) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"reference": ref.PublicSummary(), "status": status, "message": message}})
}

func (h *Handler) messageEvidence(c *gin.Context) (*types.Message, *types.EvidenceReference, error) {
	if h.messageService == nil {
		return nil, nil, fmt.Errorf("message service unavailable")
	}
	message, err := h.messageService.GetMessage(c.Request.Context(), paramSessionID(c), c.Param("message_id"))
	if err != nil || message == nil || message.ID != c.Param("message_id") || message.SessionID != paramSessionID(c) || message.Role != "assistant" || !message.IsCompleted {
		return nil, nil, fmt.Errorf("message unavailable")
	}
	for _, reference := range message.EvidenceReferences {
		if reference.ID != c.Param("evidence_id") {
			continue
		}
		if err := reference.ValidateStored(); err != nil || reference.CandidateHash != types.EvidenceCandidateHash(message.Content) {
			return nil, nil, fmt.Errorf("evidence version mismatch")
		}
		return message, &reference, nil
	}
	return nil, nil, fmt.Errorf("evidence unavailable")
}

func (h *Handler) authorizeEvidenceSources(ctx context.Context, message *types.Message, ref *types.EvidenceReference) ([]authorizedEvidenceSource, error) {
	if h.knowledgeService == nil || h.knowledgebaseService == nil {
		return nil, fmt.Errorf("source services unavailable")
	}
	if scope, ok := types.TenantAPIKeyScopeFromContext(ctx); ok && !scope.HasCapability(types.APIKeyCapabilityRetrieve) {
		return nil, fmt.Errorf("source preview requires the existing retrieve capability")
	}
	var result []authorizedEvidenceSource
	caller := types.CallerFromContext(ctx)
	for _, source := range ref.Sources {
		kb, err := h.knowledgebaseService.GetKnowledgeBaseByIDOnly(ctx, source.KnowledgeBaseID)
		if err != nil || kb == nil || kb.DeletedAt.Valid || kb.ID != source.KnowledgeBaseID || source.TenantID != 0 && source.TenantID != kb.TenantID {
			return nil, fmt.Errorf("source scope unavailable")
		}
		request := access.KBRequest{Caller: caller}
		var agents access.AgentShareLookup
		if message.AgentID != "" && !strings.HasPrefix(message.AgentID, "builtin-") {
			request.AgentID = message.AgentID
			agents = h.agentShareService
			if message.AgentTenantID != 0 {
				request.AgentSourceTenantID = strconv.FormatUint(message.AgentTenantID, 10)
			}
		}
		grant, err := access.ResolveKB(ctx, request, kb, types.OrgRoleViewer, h.kbShareService, agents)
		if err != nil {
			return nil, fmt.Errorf("source permission unavailable")
		}
		scoped := grant.Context(ctx)
		knowledge, err := h.knowledgeService.GetKnowledgeByID(scoped, source.KnowledgeID)
		if err != nil || knowledge == nil || knowledge.ID != source.KnowledgeID || knowledge.KnowledgeBaseID != kb.ID || knowledge.TenantID != kb.TenantID || knowledge.DeletedAt.Valid || knowledge.ParseStatus == types.ParseStatusDeleting || knowledge.EnableStatus == "disabled" {
			return nil, fmt.Errorf("source document unavailable")
		}
		if ids := source.Authorization.KnowledgeIDs; len(ids) > 0 && !slices.Contains(ids, knowledge.ID) {
			return nil, fmt.Errorf("source document outside original scope")
		}
		if ids := source.Authorization.TagIDs; len(ids) > 0 {
			matches, err := h.knowledgeService.ListKnowledgeIDsByTagIDs(scoped, kb.TenantID, kb.ID, ids)
			if err != nil || !slices.Contains(matches, knowledge.ID) {
				return nil, fmt.Errorf("source tag scope changed")
			}
		}
		if source.KnowledgeFileHash != "" && source.KnowledgeFileHash != knowledge.FileHash {
			return nil, fmt.Errorf("source file revision changed")
		}
		result = append(result, authorizedEvidenceSource{snapshot: source, knowledge: knowledge, ctx: scoped})
	}
	return result, nil
}

// PreviewMessageEvidenceSource validates and serves the SAME original bytes.
// A separate unversioned /knowledge/:id/preview request could race a replace
// after the details check and highlight the historical cells on a new file.
func (h *Handler) PreviewMessageEvidenceSource(c *gin.Context) {
	message, reference, err := h.messageEvidence(c)
	if err != nil {
		c.Error(errors.NewNotFoundError("证据不存在或无权访问"))
		return
	}
	sources, err := h.authorizeEvidenceSources(c.Request.Context(), message, reference)
	index, indexErr := strconv.Atoi(c.Param("source_index"))
	if err != nil || indexErr != nil || index < 0 || index >= len(sources) {
		c.Error(errors.NewNotFoundError("来源不存在或无权访问"))
		return
	}
	raw, err := h.readEvidenceOriginal(sources[index], evidenceOriginalByteLimit)
	if err != nil {
		c.Error(errors.NewConflictError("来源已更新，原定位不可用"))
		return
	}
	if err := filetransport.Serve(c.Writer, c.Request, io.NopCloser(bytes.NewReader(raw)), filetransport.Options{Filename: sources[index].knowledge.FileName, CacheControl: "private, no-store"}); err != nil {
		c.Error(errors.NewInternalServerError("原件预览失败"))
	}
}

func (h *Handler) readEvidenceOriginal(source authorizedEvidenceSource, limit int64) ([]byte, error) {
	if source.snapshot.SourceSHA256 == "" || source.knowledge.FileSize <= 0 || source.knowledge.FileSize > limit {
		return nil, fmt.Errorf("source version unavailable")
	}
	file, _, err := h.knowledgeService.GetKnowledgeFile(source.ctx, source.knowledge.ID)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit || int64(len(raw)) != source.knowledge.FileSize {
		return nil, fmt.Errorf("source size changed")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != source.snapshot.SourceSHA256 {
		return nil, fmt.Errorf("source SHA changed")
	}
	if source.knowledge.FileHash != "" {
		legacy := md5.Sum(raw)
		actual := hex.EncodeToString(legacy[:])
		if len(source.knowledge.FileHash) == 64 {
			actual = hex.EncodeToString(digest[:])
		}
		if actual != source.knowledge.FileHash {
			return nil, fmt.Errorf("source file record changed")
		}
	}
	current, err := h.knowledgeService.GetKnowledgeByID(source.ctx, source.knowledge.ID)
	if err != nil || current == nil || current.DeletedAt.Valid || current.ParseStatus == types.ParseStatusDeleting || current.EnableStatus == "disabled" || current.KnowledgeBaseID != source.knowledge.KnowledgeBaseID || current.FileHash != source.knowledge.FileHash || current.FilePath != source.knowledge.FilePath {
		return nil, fmt.Errorf("source changed during preview")
	}
	return raw, nil
}

// The stored execution can contain internal navigation and scope metadata.
// Return only display/verification fields, never the full original tool result.
func publicEvidenceManifest(raw json.RawMessage) map[string]interface{} {
	var source map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return nil
	}
	view := map[string]interface{}{}
	for _, key := range []string{"schema_version", "selector", "result", "source_cells", "quality"} {
		if value, ok := source[key]; ok {
			view[key] = value
		}
	}
	if unit, ok := source["unit"].(map[string]interface{}); ok {
		view["unit"] = publicEvidenceUnit(unit)
	}
	columnMetadata := source["column_metadata"]
	if columnMetadata == nil {
		if unit, ok := source["unit"].(map[string]interface{}); ok {
			columnMetadata = unit["column_metadata"]
		}
	}
	if columns := publicEvidenceColumnMetadata(columnMetadata); len(columns) > 0 {
		view["column_metadata"] = columns
	}
	if computation, ok := source["computation"].(map[string]interface{}); ok {
		projected := map[string]interface{}{}
		for _, key := range []string{"sql", "expression", "input_scope", "input_count", "input_complete", "coverage", "source_truncated", "sql_result_lineage_verified"} {
			if value, ok := computation[key]; ok {
				projected[key] = value
			}
		}
		if execution, ok := computation["execution"].(map[string]interface{}); ok {
			projected["execution"] = evidenceDisplayFields(execution, "kind", "engine", "engine_version", "numeric_semantics", "input_scope", "cell_lineage_verified", "column_metadata_scope", "operation")
			if _, exists := projected["input_scope"]; !exists {
				if scope, exists := execution["input_scope"]; exists {
					projected["input_scope"] = scope
				}
			}
		}
		if mappings := publicEvidenceMappings(computation["mapping_provenance"]); len(mappings) > 0 {
			projected["mappings"] = mappings
		}
		view["computation"] = projected
	}
	if result, ok := source["result"].(map[string]interface{}); ok {
		projected := map[string]interface{}{}
		if columns, ok := result["columns"]; ok {
			projected["columns"] = columns
		}
		if rows, ok := result["rows"]; ok {
			projected["rows"] = evidenceDisplayValues(rows)
		}
		view["result"] = projected
	}
	return view
}

func evidenceDisplayFields(source map[string]interface{}, keys ...string) map[string]interface{} {
	view := map[string]interface{}{}
	for _, key := range keys {
		if value, exists := source[key]; exists {
			view[key] = value
		}
	}
	return view
}

// Unit evidence may contain private input IDs and reviewer receipts. Display
// only the declaration/derivation, never its reusable internal evidence array.
func publicEvidenceUnit(source map[string]interface{}) map[string]interface{} {
	view := evidenceDisplayFields(source, "status", "symbol", "evidence_level", "binding_scope", "expression", "quantity", "reason")
	if source["status"] == "unknown" || source["status"] == "mixed" {
		delete(view, "symbol")
	}
	return view
}

func publicEvidenceColumnMetadata(raw interface{}) []interface{} {
	columns, _ := raw.([]interface{})
	view := []interface{}{}
	for _, rawColumn := range columns {
		column, ok := rawColumn.(map[string]interface{})
		if !ok {
			return nil // Keep ordering exact; never join the next column by label.
		}
		item := evidenceDisplayFields(column, "index", "name", "display_name")
		unit, ok := column["unit_metadata"].(map[string]interface{})
		if !ok {
			unit, _ = column["unit"].(map[string]interface{})
		}
		if unit != nil {
			item["unit_metadata"] = publicEvidenceUnit(unit)
		}
		view = append(view, item)
	}
	return view
}

// A mapping is a copy of explicit source coordinates, not contributor lineage.
// Do not export the nested provenance blobs, source identities or raw cells.
func publicEvidenceMappings(raw interface{}) []interface{} {
	text, _ := raw.(string)
	view := []interface{}{}
	for depth := 0; depth < 8 && text != "" && len(text) <= types.MaxEvidenceManifestBytes; depth++ {
		var mapping map[string]interface{}
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if decoder.Decode(&mapping) != nil || mapping["kind"] != "source_cell_projection" || mapping["projection_applied_by_runtime"] != true {
			break
		}
		coordinates, ok := mapping["mapping"].(map[string]interface{})
		if !ok {
			break
		}
		item := evidenceDisplayFields(mapping, "source_rows", "source_columns", "projection_output_rows")
		item["mapping"] = evidenceDisplayFields(coordinates, "orientation", "x_index", "y_indexes", "value_indexes", "label_index", "unit_index")
		view = append(view, item)
		text, _ = mapping["source_provenance"].(string)
	}
	return view
}

// Detail rows are display observations, not executable SQL values. Decimal
// strings preserve every digit across browser JSON.parse (including integers
// above JavaScript's safe-integer boundary) without changing the stored result.
func evidenceDisplayValues(value interface{}) interface{} {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i, item := range typed {
			result[i] = evidenceDisplayValues(item)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = evidenceDisplayValues(item)
		}
		return result
	default:
		return value
	}
}
