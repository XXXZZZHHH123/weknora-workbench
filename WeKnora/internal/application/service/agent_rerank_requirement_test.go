package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentRequiresRerankModel(t *testing.T) {
	tests := []struct {
		name  string
		agent *types.CustomAgent
		want  bool
	}{
		{
			name: "knowledge search with all knowledge bases",
			agent: &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "all",
				AllowedTools:    []string{tools.ToolSearchKnowledge},
			}},
			want: true,
		},
		{
			name: "knowledge search with selected knowledge bases",
			agent: &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "selected",
				AllowedTools:    []string{tools.ToolSearchKnowledge},
			}},
			want: true,
		},
		{
			name: "knowledge search with knowledge bases disabled",
			agent: &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "none",
				AllowedTools:    []string{tools.ToolSearchKnowledge},
			}},
			want: false,
		},
		{
			name: "default tools with knowledge bases disabled",
			agent: &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "none",
			}},
			want: false,
		},
		{
			name: "wiki tools do not use reranker",
			agent: &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "all",
				AllowedTools:    []string{"wiki_search", "wiki_read_page"},
			}},
			want: false,
		},
		{
			name:  "nil agent",
			agent: nil,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, agentRequiresRerankModel(tt.agent))
		})
	}
}

// The assistant searches knowledge bases but cannot be given a rerank model,
// so it must take the workspace's instead of failing every run.
func TestWeKnoraAssistantTakesWorkspaceRerankModel(t *testing.T) {
	require.NoError(t, types.LoadBuiltinAgentsConfig(filepath.Join("..", "..", "..", "config")))
	agent := types.GetBuiltinAgent(types.BuiltinWeKnoraAssistantID, 1)
	require.NotNil(t, agent)
	assert.True(t, agentRequiresRerankModel(agent))
	assert.Empty(t, agent.Config.RerankModelID)
	assert.True(t, types.UsesWorkspaceModelFallback(agent))
}

func TestPickWorkspaceRerankModelID(t *testing.T) {
	model := func(id string, typ types.ModelType, status types.ModelStatus, isDefault bool) *types.Model {
		return &types.Model{ID: id, Type: typ, Status: status, IsDefault: isDefault}
	}
	active := types.ModelStatusActive
	tests := []struct {
		name   string
		models []*types.Model
		want   string
	}{
		{name: "no models", want: ""},
		{
			name: "default rerank model wins over an earlier one",
			models: []*types.Model{
				model("first", types.ModelTypeRerank, active, false),
				model("default", types.ModelTypeRerank, active, true),
			},
			want: "default",
		},
		{
			name: "first active rerank model without a default",
			models: []*types.Model{
				nil,
				model("chat", types.ModelTypeKnowledgeQA, active, true),
				model("first", types.ModelTypeRerank, active, false),
				model("second", types.ModelTypeRerank, active, false),
			},
			want: "first",
		},
		{
			name: "inactive rerank models are skipped, even the default",
			models: []*types.Model{
				model("broken-default", types.ModelTypeRerank, types.ModelStatusDownloadFailed, true),
				model("usable", types.ModelTypeRerank, active, false),
			},
			want: "usable",
		},
		{
			name:   "no rerank model",
			models: []*types.Model{model("chat", types.ModelTypeKnowledgeQA, active, true)},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pickWorkspaceRerankModelID(tt.models))
		})
	}
}

type workbenchRerankModelService struct {
	interfaces.ModelService
	models    []*types.Model
	listCalls int
	loaded    []string
	tenants   []uint64
}

func (s *workbenchRerankModelService) ListModels(ctx context.Context) ([]*types.Model, error) {
	s.listCalls++
	s.tenants = append(s.tenants, types.MustTenantIDFromContext(ctx))
	return s.models, nil
}

func (s *workbenchRerankModelService) GetRerankModel(ctx context.Context, id string) (rerank.Reranker, error) {
	s.loaded = append(s.loaded, id)
	s.tenants = append(s.tenants, types.MustTenantIDFromContext(ctx))
	return &scoredReranker{}, nil
}

func TestWorkbenchQuickAnswerResolveRerankModel(t *testing.T) {
	active := &types.Model{ID: "workspace-active", Type: types.ModelTypeRerank, Status: types.ModelStatusActive}
	preferred := &types.Model{ID: "workspace-default", Type: types.ModelTypeRerank, Status: types.ModelStatusActive, IsDefault: true}
	inactive := &types.Model{ID: "inactive", Type: types.ModelTypeRerank, Status: types.ModelStatusDownloadFailed}
	tests := []struct {
		name, explicit, scope, want string
		models                      []*types.Model
		wantLists                   int
	}{
		{name: "explicit model wins", explicit: "bound-model", scope: "all", want: "bound-model", models: []*types.Model{preferred}},
		{name: "workspace default", scope: "all", want: preferred.ID, models: []*types.Model{active, preferred}, wantLists: 1},
		{name: "workspace first active", scope: "all", want: active.ID, models: []*types.Model{inactive, active}, wantLists: 1},
		{name: "no active model", scope: "all", models: []*types.Model{inactive}, wantLists: 1},
		{name: "disabled knowledge scope", explicit: "bound-model", scope: "none", models: []*types.Model{preferred}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			models := &workbenchRerankModelService{models: tc.models}
			s := &sessionService{modelService: models}
			a := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, WorkbenchDefault: true,
				Config: types.CustomAgentConfig{KBSelectionMode: tc.scope, RerankModelID: tc.explicit, AllowedTools: []string{tools.ToolSearchKnowledge}}}
			ctx := types.WithExecutionTenant(t.Context(), 7)
			r, err := s.resolveAgentRerankModel(ctx, a)
			require.NoError(t, err)
			assert.Equal(t, tc.wantLists, models.listCalls)
			for _, tenant := range models.tenants {
				assert.Equal(t, uint64(7), tenant)
			}
			if tc.want == "" {
				assert.Nil(t, r)
				assert.Empty(t, models.loaded)
			} else {
				assert.NotNil(t, r)
				assert.Equal(t, []string{tc.want}, models.loaded)
			}
		})
	}
}
