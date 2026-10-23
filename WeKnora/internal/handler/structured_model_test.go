package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type structuredModelClientStub struct {
	calls    int
	messages []chat.Message
	options  *chat.ChatOptions
	caller   types.Caller
	tenant   uint64
	deadline time.Time
	response *types.ChatResponse
	err      error
	after    func()
}

func (s *structuredModelClientStub) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	s.calls++
	s.messages, s.options = messages, options
	s.caller, s.tenant = types.CallerFromContext(ctx), types.MustTenantIDFromContext(ctx)
	s.deadline, _ = ctx.Deadline()
	if s.after != nil {
		s.after()
	}
	return s.response, s.err
}

func (*structuredModelClientStub) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	panic("structured model proxy must not stream or create conversations")
}
func (*structuredModelClientStub) GetModelID() string   { return "configured-chat" }
func (*structuredModelClientStub) GetModelName() string { return "fixture" }

type structuredModelsStub struct {
	interfaces.ModelService
	model        *types.Model
	client       *structuredModelClientStub
	lookups      []uint64
	initializers []uint64
	lookupIDs    []string
	clientIDs    []string
	lookupErr    error
	initErr      error
}

func (s *structuredModelsStub) GetModelByID(ctx context.Context, id string) (*types.Model, error) {
	s.lookups = append(s.lookups, types.MustTenantIDFromContext(ctx))
	s.lookupIDs = append(s.lookupIDs, id)
	return s.model, s.lookupErr
}
func (s *structuredModelsStub) GetChatModel(ctx context.Context, id string) (chat.Chat, error) {
	s.initializers = append(s.initializers, types.MustTenantIDFromContext(ctx))
	s.clientIDs = append(s.clientIDs, id)
	return s.client, s.initErr
}

type structuredModelKBStub struct {
	interfaces.KnowledgeBaseService
	kb    *types.KnowledgeBase
	calls int
	err   error
}

func (s *structuredModelKBStub) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	s.calls++
	return s.kb, s.err
}

type structuredModelFixture struct {
	router       *gin.Engine
	knowledge    *structuredModelKBStub
	models       *structuredModelsStub
	caller       types.Caller
	principal    *types.Principal
	apiKey       *types.TenantAPIKeyScope
	requestGrant bool
}

func newStructuredModelFixture(t *testing.T) *structuredModelFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &structuredModelFixture{
		caller: types.Caller{TenantID: 7, UserID: "editor", Role: types.TenantRoleContributor},
		knowledge: &structuredModelKBStub{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7, CreatorID: "owner", AccessPolicy: &types.ResourceAccessPolicy{
			SpacePermission: types.ResourcePermissionNone, Members: []types.ResourceMemberPermission{{UserID: "editor", Permission: types.ResourcePermissionEditor}},
		}}},
		models: &structuredModelsStub{
			model:  &types.Model{ID: "configured-chat", TenantID: 7, Type: types.ModelTypeKnowledgeQA, Status: types.ModelStatusActive, Parameters: types.ModelParameters{APIKey: "fixture-provider-secret"}},
			client: &structuredModelClientStub{response: &types.ChatResponse{Content: `{"fields":[]}`, FinishReason: "stop", ReasoningContent: "private reasoning"}},
		},
	}
	f.router = gin.New()
	f.router.Use(middleware.ErrorHandler(), func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), f.caller)
		ctx = types.WithExecutionTenant(ctx, f.caller.TenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, f.caller.UserID)
		if f.principal != nil {
			ctx = types.WithPrincipal(ctx, *f.principal)
		}
		if f.apiKey != nil {
			ctx = types.WithTenantAPIKeyScope(ctx, *f.apiKey)
		}
		if f.requestGrant {
			minimumKBRole := types.OrgRoleEditor
			if f.caller.Role == types.TenantRoleViewer {
				minimumKBRole = types.OrgRoleViewer
			}
			grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: f.caller}, f.knowledge.kb, minimumKBRole, nil, nil)
			require.NoError(t, err)
			ctx = grant.WithGrant(ctx)
			c.Set(middleware.KBAccessContextKey, grant)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	f.router.POST("/knowledge-bases/:id/structured-model/chat", StructuredModelChat(&KnowledgeHandler{kbService: f.knowledge}, NewModelHandler(f.models)))
	f.router.POST("/knowledge-bases/:id/structured-model/plan", StructuredModelPlan(&KnowledgeHandler{kbService: f.knowledge}, NewModelHandler(f.models)))
	return f
}

func (f *structuredModelFixture) request(t *testing.T, body string) *httptest.ResponseRecorder {
	return f.requestPurpose(t, "chat", body)
}

func (f *structuredModelFixture) requestPurpose(t *testing.T, purpose, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb/structured-model/"+purpose, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

const structuredModelBody = `{"model_id":"configured-chat","messages":[{"role":"system","content":"Return a JSON draft based on all provided evidence."},{"role":"user","content":"Source rows and explicit field evidence."}],"max_tokens":2048,"temperature":0,"enable_thinking":false}`

func TestStructuredModelPlanViewerMayPlanButCannotRepair(t *testing.T) {
	f := newStructuredModelFixture(t)
	f.caller.Role = types.TenantRoleViewer
	f.knowledge.kb.AccessPolicy.Members[0].Permission = types.ResourcePermissionViewer
	w := f.requestPurpose(t, "plan", structuredModelBody)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, f.models.client.calls)
	require.Equal(t, 2, f.knowledge.calls)
	require.Equal(t, f.caller, f.models.client.caller)
	require.Equal(t, []uint64{7, 7}, f.models.lookups)
	require.Empty(t, f.models.client.options.Tools)
	require.Equal(t, http.StatusForbidden, f.request(t, structuredModelBody).Code)
	require.Equal(t, 1, f.models.client.calls, "read-only planning does not authorize the repair endpoint")
}

func TestStructuredModelPlanViewerNeedsCurrentKBReadPermission(t *testing.T) {
	for _, revokeDuringCall := range []bool{false, true} {
		f := newStructuredModelFixture(t)
		f.caller.Role = types.TenantRoleViewer
		f.knowledge.kb.AccessPolicy.Members[0].Permission = types.ResourcePermissionViewer
		if revokeDuringCall {
			f.requestGrant = true
			f.models.client.after = func() { f.knowledge.kb.AccessPolicy.Members = nil }
		} else {
			f.knowledge.kb.AccessPolicy.Members = nil
		}
		w := f.requestPurpose(t, "plan", structuredModelBody)
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "fields")
		if revokeDuringCall {
			require.Equal(t, 1, f.models.client.calls)
			require.Equal(t, 2, f.knowledge.calls)
		} else {
			require.Zero(t, f.models.client.calls)
		}
	}
}

func TestStructuredModelPlanStillRejectsMachineAndForeignModels(t *testing.T) {
	for _, machine := range []bool{false, true} {
		f := newStructuredModelFixture(t)
		f.caller.Role = types.TenantRoleViewer
		f.knowledge.kb.AccessPolicy.Members[0].Permission = types.ResourcePermissionViewer
		if machine {
			f.apiKey = &types.TenantAPIKeyScope{KeyID: 1, FullAccess: true}
		} else {
			f.models.model.TenantID = 9
		}
		w := f.requestPurpose(t, "plan", structuredModelBody)
		require.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
		require.Zero(t, f.models.client.calls)
	}
}

func TestStructuredModelChatUsesCallerModelAndOnlyReturnsCompletion(t *testing.T) {
	f := newStructuredModelFixture(t)
	w := f.request(t, structuredModelBody)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `{"success":true,"data":{"model_id":"configured-chat","content":"{\"fields\":[]}","finish_reason":"stop"}}`, w.Body.String())
	require.Equal(t, []uint64{7, 7}, f.models.lookups)
	require.Equal(t, []uint64{7}, f.models.initializers)
	require.Equal(t, []string{"configured-chat", "configured-chat"}, f.models.lookupIDs)
	require.Equal(t, []string{"configured-chat"}, f.models.clientIDs)
	require.Equal(t, 2, f.knowledge.calls)
	require.Equal(t, 1, f.models.client.calls)
	require.Equal(t, f.caller, f.models.client.caller)
	require.Equal(t, uint64(7), f.models.client.tenant)
	require.Len(t, f.models.client.messages, 2)
	require.Equal(t, "Source rows and explicit field evidence.", f.models.client.messages[1].Content)
	require.Equal(t, 2048, f.models.client.options.MaxTokens)
	require.Equal(t, float64(0), f.models.client.options.Temperature)
	require.NotNil(t, f.models.client.options.Thinking)
	require.False(t, *f.models.client.options.Thinking)
	require.Empty(t, f.models.client.options.Tools)
	require.Empty(t, f.models.client.options.Format)
	require.WithinDuration(t, time.Now().Add(structuredModelTimeout), f.models.client.deadline, 2*time.Second)
	require.NotContains(t, w.Body.String(), "fixture-provider-secret")
	require.NotContains(t, w.Body.String(), "private reasoning")
}

func TestStructuredModelChatDefaultsAndBuiltinModels(t *testing.T) {
	f := newStructuredModelFixture(t)
	f.models.model.IsBuiltin = true
	f.models.model.TenantID = types.DefaultBuiltinModelTenantID
	w := f.request(t, `{"model_id":"configured-chat","messages":[{"role":"user","content":"Use current source evidence."}]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, structuredModelMaxTokens, f.models.client.options.MaxTokens)
	require.Nil(t, f.models.client.options.Thinking)
	require.Equal(t, []uint64{7, 7}, f.models.lookups)
}

func TestStructuredModelChatRejectsIdentityOrKBPermissionBeforeInference(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*structuredModelFixture)
		code int
	}{
		{"no user", func(f *structuredModelFixture) { f.caller.UserID = "" }, 401},
		{"no tenant", func(f *structuredModelFixture) { f.caller.TenantID = 0 }, 401},
		{"viewer", func(f *structuredModelFixture) { f.caller.Role = types.TenantRoleViewer }, 403},
		{"synthetic", func(f *structuredModelFixture) { f.caller.UserID = "system-7" }, 403},
		{"machine", func(f *structuredModelFixture) {
			f.principal = &types.Principal{Type: types.PrincipalAPITenant, ID: "terminal"}
		}, 403},
		{"different principal", func(f *structuredModelFixture) {
			f.principal = &types.Principal{Type: types.PrincipalWebUser, ID: "someone-else"}
		}, 403},
		{"full API key", func(f *structuredModelFixture) { f.apiKey = &types.TenantAPIKeyScope{KeyID: 1, FullAccess: true} }, 403},
		{"chat and ingest API key", func(f *structuredModelFixture) {
			f.apiKey = &types.TenantAPIKeyScope{KeyID: 1, Capabilities: types.StringArray{"chat", "ingest"}, KnowledgeBaseIDs: types.StringArray{"kb"}}
		}, 403},
		{"read-only resource", func(f *structuredModelFixture) {
			f.knowledge.kb.AccessPolicy.Members[0].Permission = types.ResourcePermissionViewer
		}, 403},
		{"private resource", func(f *structuredModelFixture) { f.knowledge.kb.AccessPolicy.Members = nil }, 403},
		{"foreign KB", func(f *structuredModelFixture) { f.knowledge.kb.TenantID = 9 }, 404},
		{"wrong KB ID", func(f *structuredModelFixture) { f.knowledge.kb.ID = "other" }, 404},
		{"deleted KB", func(f *structuredModelFixture) { f.knowledge.kb.DeletedAt.Valid = true }, 404},
		{"missing KB", func(f *structuredModelFixture) { f.knowledge.kb = nil }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			tc.set(f)
			w := f.request(t, structuredModelBody)
			require.Equal(t, tc.code, w.Code, w.Body.String())
			require.Empty(t, f.models.lookups)
			require.Zero(t, f.models.client.calls)
		})
	}
}

func TestStructuredModelChatRejectsInvalidOrForeignModels(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*structuredModelFixture)
	}{
		{"different tenant", func(f *structuredModelFixture) { f.models.model.TenantID = 9 }},
		{"different ID", func(f *structuredModelFixture) { f.models.model.ID = "other" }},
		{"deleted", func(f *structuredModelFixture) { f.models.model.DeletedAt.Valid = true }},
		{"disabled", func(f *structuredModelFixture) { f.models.model.Status = types.ModelStatusDownloadFailed }},
		{"non-chat", func(f *structuredModelFixture) { f.models.model.Type = types.ModelTypeEmbedding }},
		{"not found", func(f *structuredModelFixture) { f.models.lookupErr = errors.New("fixture-provider-secret") }},
		{"no model", func(f *structuredModelFixture) { f.models.model = nil }},
		{"client initialization", func(f *structuredModelFixture) { f.models.initErr = errors.New("fixture-provider-secret") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			tc.set(f)
			w := f.request(t, structuredModelBody)
			require.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
			require.Zero(t, f.models.client.calls)
			require.NotContains(t, w.Body.String(), "fixture-provider-secret")
		})
	}
}

func TestStructuredModelChatValidatesBoundedTextOnlyProtocol(t *testing.T) {
	var base map[string]any
	require.NoError(t, json.Unmarshal([]byte(structuredModelBody), &base))
	for name, value := range map[string]any{
		"tools": []any{}, "images": []any{}, "base_url": "https://fixture.invalid/", "response_format": map[string]any{"type": "json_object"},
		"max_tokens": 8193, "temperature": 1.1,
	} {
		t.Run(name, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			input := map[string]any{}
			for key, entry := range base {
				input[key] = entry
			}
			input[name] = value
			body, err := json.Marshal(input)
			require.NoError(t, err)
			w := f.request(t, string(body))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Zero(t, f.knowledge.calls)
			require.Zero(t, f.models.client.calls)
		})
	}
	for _, body := range []string{
		structuredModelBody + `{}`, `null`, `{"model_id":"configured-chat","messages":[]}`,
		`{"model_id":"configured-chat","messages":[{"role":"tool","content":"value"}]}`,
		`{"model_id":"configured-chat","messages":[{"role":"system","content":"value"}]}`,
		`{"model_id":"configured-chat","messages":[{"role":"user","content":" "}]}`,
		`{"model_id":"configured-chat","messages":[{"role":"user","content":"value","images":["https://fixture.invalid/"]}]}`,
		`{"model_id":"configured-chat","messages":[{"role":"user","content":"` + strings.Repeat("x", structuredModelPromptBytes+1) + `"}]}`,
		strings.Repeat(" ", 6*structuredModelPromptBytes+8193) + structuredModelBody,
	} {
		f := newStructuredModelFixture(t)
		w := f.request(t, body)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.Zero(t, f.knowledge.calls)
		require.Zero(t, f.models.client.calls)
	}
}

func TestStructuredModelChatRechecksKBAndModelAfterCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*structuredModelFixture)
	}{
		{"KB revoked", func(f *structuredModelFixture) { f.knowledge.kb.AccessPolicy.Members = nil }},
		{"KB removed", func(f *structuredModelFixture) { f.knowledge.kb.DeletedAt.Valid = true }},
		{"KB moved", func(f *structuredModelFixture) { f.knowledge.kb.TenantID = 9 }},
		{"model disabled", func(f *structuredModelFixture) { f.models.model.Status = types.ModelStatusDownloadFailed }},
		{"model moved", func(f *structuredModelFixture) { f.models.model.TenantID = 9 }},
		{"model deleted", func(f *structuredModelFixture) { f.models.model.DeletedAt.Valid = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			f.requestGrant = true
			f.models.client.after = func() { tc.set(f) }
			w := f.request(t, structuredModelBody)
			require.NotEqual(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, 1, f.models.client.calls)
			require.Equal(t, 2, f.knowledge.calls, "a cached request-local grant must not replace a current lookup")
			require.NotContains(t, w.Body.String(), "fields")
		})
	}
}

func TestStructuredModelChatDoesNotLeakInvalidProviderResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*structuredModelFixture)
	}{
		{"provider error", func(f *structuredModelFixture) {
			f.models.client.err = errors.New("Authorization: fixture-provider-secret; private source excerpt")
		}},
		{"no response", func(f *structuredModelFixture) { f.models.client.response = nil }},
		{"empty response", func(f *structuredModelFixture) { f.models.client.response.Content = " " }},
		{"oversized", func(f *structuredModelFixture) {
			f.models.client.response.Content = strings.Repeat("x", structuredModelResponseBytes+1)
		}},
		{"tool request", func(f *structuredModelFixture) {
			f.models.client.response.ToolCalls = []types.LLMToolCall{{ID: "not-executed"}}
		}},
		{"oversized finish", func(f *structuredModelFixture) { f.models.client.response.FinishReason = strings.Repeat("x", 65) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			tc.set(f)
			w := f.request(t, structuredModelBody)
			require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), "fixture-provider-secret")
			require.NotContains(t, w.Body.String(), "private source excerpt")
			require.NotContains(t, w.Body.String(), "fields")
		})
	}
}
