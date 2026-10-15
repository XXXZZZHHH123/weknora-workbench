package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type workspaceDiagramModels struct {
	interfaces.ModelService
	model                    *types.Model
	client                   *workspaceDiagramChat
	getError, initError      error
	lookups, initializations int
}

func (m *workspaceDiagramModels) GetModelByID(_ context.Context, _ string) (*types.Model, error) {
	m.lookups++
	return m.model, m.getError
}
func (m *workspaceDiagramModels) GetChatModel(_ context.Context, _ string) (chat.Chat, error) {
	m.initializations++
	return m.client, m.initError
}

type workspaceDiagramChat struct {
	calls    int
	messages []chat.Message
	options  *chat.ChatOptions
	deadline time.Time
	response *types.ChatResponse
	err      error
	after    func()
}

func (f *workspaceDiagramChat) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	f.calls++
	f.messages, f.options = messages, options
	f.deadline, _ = ctx.Deadline()
	if f.after != nil {
		f.after()
	}
	return f.response, f.err
}

func (f *workspaceDiagramChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	panic("diagram generation must use non-streaming production Chat")
}
func (f *workspaceDiagramChat) GetModelName() string { return "synthetic" }
func (f *workspaceDiagramChat) GetModelID() string   { return "configured-chat" }

type workspaceDiagramKBs struct {
	interfaces.KnowledgeBaseService
	allowed bool
}

func (f *workspaceDiagramKBs) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if !f.allowed || id != "kb" {
		return nil, gorm.ErrRecordNotFound
	}
	return &types.KnowledgeBase{ID: id, TenantID: 1, Name: "Library"}, nil
}

type workspaceDiagramFixture struct {
	router      *gin.Engine
	models      *workspaceDiagramModels
	kbs         *workspaceDiagramKBs
	service     *service.WorkspaceService
	db          *gorm.DB
	workbenchID string
	user        string
	tenant      uint64
	role        types.TenantRole
	apiKey      bool
	principal   string
}

func newWorkspaceDiagramFixture(t *testing.T) *workspaceDiagramFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "diagram.db")), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.Exec("CREATE TABLE sessions(id TEXT PRIMARY KEY, tenant_id INTEGER, user_id TEXT)").Error)
	for _, path := range []string{"../../migrations/sqlite/000038_workspace_navigation.up.sql", "../../migrations/sqlite/000041_research_workbench_templates.up.sql"} {
		migration, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, db.Exec(string(migration)).Error)
	}
	kbs := &workspaceDiagramKBs{allowed: true}
	workspaces := service.NewWorkspaceService(db, kbs, nil)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "alice", Role: types.TenantRoleContributor})
	name, kb := "Synthetic research", "kb"
	workbench, err := workspaces.CreateWorkbench(ctx, service.WorkspaceInput{Name: &name, KnowledgeBaseID: &kb, IdempotencyKey: "test-create"})
	require.NoError(t, err)
	models := &workspaceDiagramModels{model: &types.Model{ID: "configured-chat", TenantID: 1, Type: types.ModelTypeKnowledgeQA, Status: types.ModelStatusActive, Source: types.ModelSourceLocal}, client: &workspaceDiagramChat{response: &types.ChatResponse{Content: "# Generated configuration\n"}}}
	f := &workspaceDiagramFixture{models: models, service: workspaces, db: db, kbs: kbs, workbenchID: workbench.ID, user: "alice", tenant: 1, role: types.TenantRoleContributor}
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: f.tenant, UserID: f.user, Role: f.role})
		if f.apiKey {
			ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 5, FullAccess: true})
		}
		if f.principal != "" {
			ctx = types.WithPrincipal(ctx, types.Principal{Type: f.principal, ID: "terminal"})
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/research/:id/diagram/generate", WorkspaceDiagramGenerator(NewWorkspaceHandler(workspaces), NewModelHandler(models)))
	f.router = r
	return f
}
func (f *workspaceDiagramFixture) request(body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/research/"+f.workbenchID+"/diagram/generate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}
func diagramBody(t *testing.T, image string) string {
	t.Helper()
	body, err := json.Marshal(workspaceDiagramRequest{ModelID: "configured-chat", KnowledgeBaseID: "kb", Prompt: "完整资料与真实目录。\n保持指定配置协议和未绑定项。", Image: image})
	require.NoError(t, err)
	return string(body)
}
func diagramPNG(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestWorkspaceDiagramGeneratesDraftUsingConfiguredProductionModel(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	body := diagramBody(t, "")
	before := time.Now()
	response := f.request(body)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.JSONEq(t, `{"success":true,"data":{"content":"# Generated configuration\n","model_id":"configured-chat"}}`, response.Body.String())
	require.Equal(t, 1, f.models.client.calls)
	require.Len(t, f.models.client.messages, 2)
	require.Equal(t, "system", f.models.client.messages[0].Role)
	require.Equal(t, "完整资料与真实目录。\n保持指定配置协议和未绑定项。", f.models.client.messages[1].Content)
	require.Equal(t, 8192, f.models.client.options.MaxTokens)
	require.WithinDuration(t, before.Add(120*time.Second), f.models.client.deadline, time.Second)
	var count int64
	require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "generation must not create or replace workbenches")
}

func TestWorkspaceDiagramChecksIdentityModelAndBindingBeforeInference(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*workspaceDiagramFixture)
		body      func(*testing.T) string
		status    int
	}{
		{"anonymous", func(f *workspaceDiagramFixture) { f.user = "" }, nil, 401},
		{"viewer", func(f *workspaceDiagramFixture) { f.role = types.TenantRoleViewer }, nil, 403},
		{"api-key-even-full-access", func(f *workspaceDiagramFixture) { f.apiKey = true }, nil, 403},
		{"api-principal", func(f *workspaceDiagramFixture) { f.principal = types.PrincipalAPIExternalUser }, nil, 403},
		{"unreadable-kb", func(f *workspaceDiagramFixture) { f.kbs.allowed = false }, nil, 404},
		{"other-tenant", func(f *workspaceDiagramFixture) { f.tenant = 2 }, nil, 404},
		{"wrong-kb", func(f *workspaceDiagramFixture) {}, func(t *testing.T) string {
			return strings.Replace(diagramBody(t, ""), `"knowledge_base_id":"kb"`, `"knowledge_base_id":"other-kb"`, 1)
		}, 409},
		{"unavailable-model", func(f *workspaceDiagramFixture) { f.models.getError = errors.New("provider-secret-marker") }, nil, 404},
		{"wrong-model-id", func(f *workspaceDiagramFixture) { f.models.model.ID = "other" }, nil, 404},
		{"foreign-model", func(f *workspaceDiagramFixture) { f.models.model.TenantID = 2 }, nil, 404},
		{"inactive-model", func(f *workspaceDiagramFixture) { f.models.model.Status = types.ModelStatusDownloadFailed }, nil, 400},
		{"embedding-model", func(f *workspaceDiagramFixture) { f.models.model.Type = types.ModelTypeEmbedding }, nil, 400},
		{"no-vision", func(f *workspaceDiagramFixture) {}, func(t *testing.T) string { return diagramBody(t, diagramPNG(t)) }, 400},
		{"initialization-error", func(f *workspaceDiagramFixture) { f.models.initError = errors.New("provider-secret-marker") }, nil, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			tc.configure(f)
			body := diagramBody(t, "")
			if tc.body != nil {
				body = tc.body(t)
			}
			response := f.request(body)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.Zero(t, f.models.client.calls)
			require.NotContains(t, response.Body.String(), "provider-secret-marker")
		})
	}
}

func TestWorkspaceDiagramRejectsInvalidAndOversizeInputsWithoutModelCall(t *testing.T) {
	for name, body := range map[string]string{
		"unknown-field":  `{"model_id":"configured-chat","knowledge_base_id":"kb","prompt":"data","tenant_id":2}`,
		"extra-json":     diagramBody(t, "") + ` {}`,
		"empty-prompt":   `{"model_id":"configured-chat","knowledge_base_id":"kb","prompt":"  "}`,
		"long-prompt":    `{"model_id":"configured-chat","knowledge_base_id":"kb","prompt":"` + strings.Repeat("x", workspaceDiagramPromptLimit+1) + `"}`,
		"long-id":        `{"model_id":"` + strings.Repeat("x", 65) + `","knowledge_base_id":"kb","prompt":"data"}`,
		"external-image": diagramBody(t, "https://example.test/source.png"),
		"invalid-base64": diagramBody(t, "data:image/png;base64,invalid!"),
		"non-image":      diagramBody(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("not an image"))),
		"large-image":    diagramBody(t, "data:image/png;base64,"+strings.Repeat("A", base64.StdEncoding.EncodedLen(workspaceDiagramImageLimit+1))),
		"large-body":     `{"prompt":"` + strings.Repeat("x", workspaceDiagramBodyLimit) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			response := f.request(body)
			require.Equal(t, 400, response.Code, response.Body.String())
			require.Zero(t, f.models.lookups)
			require.Zero(t, f.models.client.calls)
		})
	}
}

func TestWorkspaceDiagramVisionAndBuiltinModelsUseNormalChat(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	f.user = "bob" // Readable workbenches are shared within a tenant; only the generated draft is private.
	f.models.model.Parameters.SupportsVision = true
	f.models.model.IsBuiltin = true
	f.models.model.TenantID = 0
	image := diagramPNG(t)
	response := f.request(diagramBody(t, image))
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, []string{image}, f.models.client.messages[1].Images)
}

func TestWorkspaceDiagramFailuresAndChangedScopeNeverReturnDraft(t *testing.T) {
	for _, scenario := range []string{"provider-error", "nil-response", "empty-response", "tool-call", "deleted-workbench", "changed-kb", "revoked-kb"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			status := 503
			switch scenario {
			case "provider-error":
				f.models.client.err = errors.New("provider-secret-marker")
			case "nil-response":
				f.models.client.response = nil
			case "empty-response":
				f.models.client.response = &types.ChatResponse{Content: " "}
			case "tool-call":
				f.models.client.response.ToolCalls = []types.LLMToolCall{{ID: "unrequested-tool"}}
			case "revoked-kb":
				status = 404
				f.models.client.after = func() { f.kbs.allowed = false }
			case "deleted-workbench":
				status = 404
				f.models.client.after = func() {
					require.NoError(t, f.db.Where("id = ?", f.workbenchID).Delete(&types.ResearchWorkbench{}).Error)
				}
			case "changed-kb":
				status = 404
				f.models.client.after = func() {
					require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Where("id = ?", f.workbenchID).Update("knowledge_base_id", "unknown-kb").Error)
				}
			}
			response := f.request(diagramBody(t, ""))
			require.Equal(t, status, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "provider-secret-marker")
			require.NotContains(t, response.Body.String(), "Generated configuration")
		})
	}
}
