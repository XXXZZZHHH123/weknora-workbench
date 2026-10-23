package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func judgmentRequest(f *workspaceDiagramFixture, body string) *httptest.ResponseRecorder {
	f.router.POST("/research/:id/judgment/generate", WorkspaceJudgmentGenerator(NewWorkspaceHandler(f.service), NewModelHandler(f.models)))
	request := httptest.NewRequest(http.MethodPost, "/research/"+f.workbenchID+"/judgment/generate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func TestWorkspaceJudgmentUsesFixedResearchSystemAndNormalConfiguredModel(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	response := judgmentRequest(f, diagramBody(t, ""))
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 1, f.models.client.calls)
	require.Equal(t, workspaceJudgmentSystem, f.models.client.messages[0].Content)
	require.NotContains(t, f.models.client.messages[0].Content, "产业图")
	require.Equal(t, workspaceDiagramTokens, f.models.client.options.MaxTokens)
	require.NotNil(t, f.models.client.options.Thinking)
	require.False(t, *f.models.client.options.Thinking)
	require.JSONEq(t, `{"type":"object"}`, string(f.models.client.options.Format))
	require.Contains(t, response.Body.String(), `"finish_reason":"unknown"`)
}

func TestWorkspaceJudgmentReportsOnlyFiniteCompletionDiagnostics(t *testing.T) {
	for _, scenario := range []struct {
		name, reason, content string
		status                int
	}{
		{"complete", "stop", `{"judgment":{}}`, 200},
		{"truncated", "length", `{"judgment":`, 200},
		{"empty-budget", "length", "", 503},
		{"untrusted-reason", "provider secret diagnostic", `{"judgment":{}}`, 200},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			f.models.client.response.Content = scenario.content
			f.models.client.response.FinishReason = scenario.reason
			response := judgmentRequest(f, diagramBody(t, ""))
			require.Equal(t, scenario.status, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "provider secret diagnostic")
			if scenario.status == 503 {
				require.Contains(t, response.Body.String(), `"error_class":"output_limit"`)
			} else if scenario.reason == "provider secret diagnostic" {
				require.Contains(t, response.Body.String(), `"finish_reason":"unknown"`)
			} else {
				require.Contains(t, response.Body.String(), `"finish_reason":"`+scenario.reason+`"`)
			}
		})
	}
}

func TestWorkspaceJudgmentRequiresItsOwnApprovedSourceModule(t *testing.T) {
	for _, module := range []string{"market", "diagram", "judgments"} {
		t.Run(module, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			f.kbs.private = true
			require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Where("id = ?", f.workbenchID).Updates(map[string]any{
				"access_policy":           `{"space_permission":"viewer","members":[]}`,
				"source_sharing_approved": true, "source_sharing_approved_by": "source-owner",
				"source_sharing_modules": `["` + module + `"]`,
			}).Error)
			response := judgmentRequest(f, diagramBody(t, ""))
			if module == "judgments" {
				require.Equal(t, 200, response.Code, response.Body.String())
				require.Equal(t, 1, f.models.client.calls)
			} else {
				require.Equal(t, 403, response.Code, response.Body.String())
				require.Zero(t, f.models.client.calls)
			}
		})
	}
}

func TestWorkspaceJudgmentRejectsPurposeOverridesImagesAndViewersBeforeInference(t *testing.T) {
	for _, scenario := range []string{"purpose", "image", "viewer", "api-key"} {
		t.Run(scenario, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			body := diagramBody(t, "")
			switch scenario {
			case "purpose":
				body = strings.TrimSuffix(body, "}") + `,"purpose":"override"}`
			case "image":
				body = diagramBody(t, diagramPNG(t))
			case "viewer":
				f.role = types.TenantRoleViewer
			case "api-key":
				f.apiKey = true
			}
			response := judgmentRequest(f, body)
			require.Contains(t, []int{400, 403}, response.Code)
			require.Zero(t, f.models.client.calls)
		})
	}
}

func TestWorkspaceJudgmentRechecksBindingAndBoundsModelOutput(t *testing.T) {
	t.Run("changed-binding", func(t *testing.T) {
		f := newWorkspaceDiagramFixture(t)
		f.models.client.after = func() {
			require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Where("id = ?", f.workbenchID).Update("knowledge_base_id", "other").Error)
		}
		response := judgmentRequest(f, diagramBody(t, ""))
		require.Equal(t, 404, response.Code)
		require.NotContains(t, response.Body.String(), f.models.client.response.Content)
	})
	t.Run("oversize", func(t *testing.T) {
		f := newWorkspaceDiagramFixture(t)
		f.models.client.response.Content = strings.Repeat("x", 128*1024+1)
		require.Equal(t, 503, judgmentRequest(f, diagramBody(t, "")).Code)
	})
}

type workspaceJudgmentTwoKBs struct {
	*workspaceDiagramKBs
	replacement *types.KnowledgeBase
}

func (k *workspaceJudgmentTwoKBs) GetKnowledgeBaseByIDOnly(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	if id == k.replacement.ID {
		return k.replacement, nil
	}
	return k.workspaceDiagramKBs.GetKnowledgeBaseByIDOnly(ctx, id)
}

func TestWorkspaceJudgmentRejectsChangedAuthorizedBinding(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	replacement := &types.KnowledgeBase{ID: "replacement-kb", TenantID: 1, Name: "Other authorized library"}
	require.NoError(t, f.db.Create(replacement).Error)
	f.service = service.NewWorkspaceService(f.db, &workspaceJudgmentTwoKBs{f.kbs, replacement}, nil)
	f.models.client.after = func() {
		require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Where("id = ?", f.workbenchID).Update("knowledge_base_id", replacement.ID).Error)
	}
	response := judgmentRequest(f, diagramBody(t, ""))
	require.Equal(t, 409, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), f.models.client.response.Content)
	require.Equal(t, 1, f.models.client.calls)
}
