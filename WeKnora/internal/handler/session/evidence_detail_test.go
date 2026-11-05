package session

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type evidenceMessages struct {
	interfaces.MessageService
	message *types.Message
}

func (s *evidenceMessages) GetMessage(context.Context, string, string) (*types.Message, error) {
	return s.message, nil
}

type evidenceKnowledge struct {
	interfaces.KnowledgeService
	row   *types.Knowledge
	raw   []byte
	reads int
	tags  []string
}

func (s *evidenceKnowledge) GetKnowledgeByID(ctx context.Context, id string) (*types.Knowledge, error) {
	if id != s.row.ID || ctx.Value(types.TenantIDContextKey) != s.row.TenantID {
		return nil, nil
	}
	copy := *s.row
	return &copy, nil
}
func (s *evidenceKnowledge) GetKnowledgeFile(context.Context, string) (io.ReadCloser, string, error) {
	s.reads++
	return io.NopCloser(bytes.NewReader(s.raw)), s.row.FileName, nil
}
func (s *evidenceKnowledge) ListKnowledgeIDsByTagIDs(context.Context, uint64, string, []string) ([]string, error) {
	return s.tags, nil
}

func evidenceHandlerFixture(t *testing.T) (*Handler, *evidenceKnowledge, *types.Message, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw := []byte("%PDF-1.7\nsource fixture")
	sha, md5sum := sha256.Sum256(raw), md5.Sum(raw)
	k := &evidenceKnowledge{row: &types.Knowledge{ID: "doc", KnowledgeBaseID: "kb", TenantID: 7, FileHash: hex.EncodeToString(md5sum[:]), FileName: "source.pdf", FilePath: "local://7/source.pdf", FileSize: int64(len(raw)), EnableStatus: "enabled"}, raw: raw, tags: []string{"doc"}}
	m := &types.Message{ID: "message", SessionID: "session", Role: "assistant", Content: "Bound source observation.", IsCompleted: true}
	m.EvidenceReferences = types.EvidenceReferences{{ID: "ev_source", Kind: types.EvidenceKindRawCell, ResultID: "result", ResultHash: strings.Repeat("a", 64), CandidateHash: types.EvidenceCandidateHash(m.Content), Sources: []types.EvidenceSourceSnapshot{{KnowledgeBaseID: "kb", KnowledgeID: "doc", SourceSHA256: hex.EncodeToString(sha[:]), Authorization: types.EvidenceSourceAuthorization{KnowledgeIDs: []string{"doc"}, TagIDs: []string{"allowed-tag"}}}}, Manifest: json.RawMessage(`{"schema_version":1,"result":{"columns":["quantity"],"rows":[["27"]]},"source_context":{"secret":"PRIVATE_SCOPE"},"receipt":"PRIVATE_RECEIPT"}`)}}
	h := &Handler{messageService: &evidenceMessages{message: m}, knowledgeService: k, knowledgebaseService: &researchScopeKBs{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(types.WithCaller(c.Request.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer}))
		c.Next()
		if len(c.Errors) > 0 && !c.Writer.Written() {
			c.JSON(http.StatusConflict, gin.H{"error": "unavailable"})
		}
	})
	r.GET("/sessions/:id/messages/:message_id/evidence/:evidence_id", h.GetMessageEvidence)
	r.GET("/sessions/:id/messages/:message_id/evidence/:evidence_id/sources/:source_index/preview", h.PreviewMessageEvidenceSource)
	return h, k, m, r
}

func TestEvidenceDetailRequiresCurrentSourcePermissionAndVersion(t *testing.T) {
	h, k, m, r := evidenceHandlerFixture(t)
	path := "/sessions/session/messages/message/evidence/ev_source"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"snapshot"`)
	require.Contains(t, w.Body.String(), `"quantity"`)
	require.NotContains(t, w.Body.String(), "PRIVATE_")
	require.NotContains(t, w.Body.String(), "claim_bindings")
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, 1, k.reads)
	k.tags = nil
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Contains(t, w.Body.String(), `"status":"unavailable"`)
	require.NotContains(t, w.Body.String(), `"manifest"`)
	require.Equal(t, 1, k.reads, "revoked/narrowed source scope must be checked before file access")
	k.tags = []string{"doc"}
	k.raw = []byte("different current file")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Contains(t, w.Body.String(), `"status":"stale"`)
	require.NotContains(t, w.Body.String(), `"manifest"`)
	h.knowledgebaseService = &researchScopeKBs{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7, CreatorID: "owner", AccessPolicy: types.NewResourceAccessPolicy()}}
	k.reads = 0
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Contains(t, w.Body.String(), `"status":"unavailable"`)
	require.Zero(t, k.reads)
	m.EvidenceReferences[0].CandidateHash = strings.Repeat("f", 64)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.NotContains(t, w.Body.String(), `"manifest"`)
}

func TestEvidencePreviewServesOnlyExactOriginalAndDoesNotReuseDetailAuthorization(t *testing.T) {
	_, k, _, r := evidenceHandlerFixture(t)
	path := "/sessions/session/messages/message/evidence/ev_source/sources/0/preview"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, k.raw, w.Body.Bytes())
	k.raw = []byte("%PDF-1.7\nchanged source")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.NotEqual(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "changed source")
	k.tags = nil
	k.reads = 0
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.NotEqual(t, http.StatusOK, w.Code)
	require.Zero(t, k.reads)
}

func TestEvidenceDetailsPreserveLargeObservedIntegersAndOriginalPreviewCapability(t *testing.T) {
	view := publicEvidenceManifest(json.RawMessage(`{"result":{"columns":["quantity"],"rows":[[9007199254740993,1.000000000000000001,null]]}}`))
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"9007199254740993"`)
	require.Contains(t, string(encoded), `"1.000000000000000001"`)
	h, k, message, _ := evidenceHandlerFixture(t)
	ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb"}, Capabilities: types.StringArray{string(types.APIKeyCapabilityChat)}})
	_, err = h.authorizeEvidenceSources(ctx, message, &message.EvidenceReferences[0])
	require.Error(t, err)
	require.Zero(t, k.reads)
}

func TestEvidenceDetailProjectsColumnUnitsAndCalculationScopeWithoutPrivateEvidence(t *testing.T) {
	_, _, message, router := evidenceHandlerFixture(t)
	inner, err := json.Marshal(map[string]interface{}{"kind": "source_cell_projection", "source_id": "PRIVATE_PARENT_ID", "projection_applied_by_runtime": true,
		"source_rows": 8, "source_columns": 4, "projection_output_rows": 2,
		"mapping":           map[string]interface{}{"orientation": "columns", "x_index": 0, "y_indexes": []int{2}, "value_indexes": []int{1, 3}, "receipt": "PRIVATE_MAPPING_RECEIPT"},
		"source_provenance": `{"source_id":"PRIVATE_ORIGINAL_ID","receipt":"PRIVATE_RECEIPT"}`})
	require.NoError(t, err)
	manifest, err := json.Marshal(map[string]interface{}{
		"result": map[string]interface{}{"columns": []string{"period", "count"}, "rows": []interface{}{[]interface{}{"2024", 17}}},
		"column_metadata": []interface{}{
			map[string]interface{}{"index": 0, "name": "period", "display_name": "Period", "unit_metadata": map[string]interface{}{"status": "unknown", "symbol": "INVALID_UPGRADE", "evidence_level": "none", "reason": "No unit declaration", "evidence": []string{"PRIVATE_UNIT_ID"}}},
			map[string]interface{}{"index": 2, "name": "count", "display_name": "Count", "unit_metadata": map[string]interface{}{"status": "derived", "symbol": "row", "evidence_level": "deterministic_expression", "expression": "COUNT(*)", "reason": "Source cardinality", "evidence": []string{"PRIVATE_DEPENDENCY_ID"}}},
		},
		"computation": map[string]interface{}{"sql": "SELECT period, COUNT(*) FROM t GROUP BY period", "execution": map[string]interface{}{"kind": "sql", "input_scope": "loaded_returned_rows", "receipt": "PRIVATE_EXECUTION_RECEIPT"}, "input_count": 8, "coverage": map[string]interface{}{"loaded_rows": 8}, "source_truncated": true, "mapping_provenance": string(inner)},
	})
	require.NoError(t, err)
	message.EvidenceReferences[0].Manifest = manifest
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sessions/session/messages/message/evidence/ev_source", nil))
	require.Equal(t, http.StatusOK, response.Code)
	for _, text := range []string{"deterministic_expression", "COUNT(*)", "Source cardinality", "loaded_returned_rows", "loaded_rows", `"source_truncated":true`, `"value_indexes":[1,3]`} {
		require.Contains(t, response.Body.String(), text)
	}
	require.NotContains(t, response.Body.String(), "PRIVATE_")
	require.NotContains(t, response.Body.String(), "INVALID_UPGRADE")
	require.NotContains(t, response.Body.String(), "mapping_provenance")
	whole := publicEvidenceManifest(json.RawMessage(`{"unit":{"status":"per_column","column_metadata":[{"index":0,"name":"amount","unit":{"status":"declared","symbol":"kg","evidence_level":"reviewed_plan_declaration","evidence":[{"receipt":"PRIVATE_UNIT"}]}}]}}`))
	columns, ok := whole["column_metadata"].([]interface{})
	require.True(t, ok)
	require.Len(t, columns, 1)
	encoded, err := json.Marshal(whole)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "reviewed_plan_declaration")
	require.NotContains(t, string(encoded), "PRIVATE_UNIT")
}
