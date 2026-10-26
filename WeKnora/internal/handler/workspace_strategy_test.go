package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceStrategyUsesFixedPurposeAndConfiguredModel(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	f.models.client.response.Content = `{"candidates":[],"gaps":["Synthetic gap"]}`
	f.router.POST("/research/:id/strategy/generate", WorkspaceStrategyGenerator(NewWorkspaceHandler(f.service), NewModelHandler(f.models)))
	request := httptest.NewRequest(http.MethodPost, "/research/"+f.workbenchID+"/strategy/generate", strings.NewReader(diagramBody(t, "")))
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, workspaceStrategySystem, f.models.client.messages[0].Content)
	require.Equal(t, 1, f.models.client.calls)
	require.JSONEq(t, `{"type":"object"}`, string(f.models.client.options.Format))
	require.NotContains(t, response.Body.String(), "status=published")
}

func TestWorkspaceDraftGenerationRequiresAnEditableWorkbenchAndAllDirectSources(t *testing.T) {
	for _, module := range []string{"judgments", "strategies", "diagram"} {
		row := &types.ResearchWorkbench{ScopeVersion: 2, CanEdit: true, CanReadSource: true, SourceDataAvailable: true, SourcesSummary: []types.WorkbenchSourceSummary{
			{ID: "direct", Status: "active", CanRead: true},
		}}
		require.True(t, canGenerateWorkbenchDraft(row, module))
		row.SourcesSummary = append(row.SourcesSummary, types.WorkbenchSourceSummary{ID: "shared", Status: "active", Delegated: true, ViewModules: []string{module}})
		row.CanReadSource = false
		require.False(t, canGenerateWorkbenchDraft(row, module))
		row.CanEdit = false
		require.False(t, canGenerateWorkbenchDraft(row, module))
		row.CanEdit = true
		row.SourcesSummary = row.SourcesSummary[1:]
		require.False(t, canGenerateWorkbenchDraft(row, module))
		row.SourcesSummary = nil
		require.False(t, canGenerateWorkbenchDraft(row, module))
	}
}
