package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/models/imagegen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type imageRegistrationModelService struct {
	interfaces.ModelService
	err   error
	calls int
}

func (m *imageRegistrationModelService) GetImageGenerationModel(context.Context, string) (imagegen.Generator, error) {
	m.calls++
	return nil, m.err
}

type imageRegistrationService struct {
	interfaces.ImageGenerationService
}

func TestImageGenerationToolRequiresExplicitAllowlistAndBinding(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allowed     []string
		binding     string
		shared      bool
		unavailable bool
		want        bool
		lookups     int
	}{
		{name: "enabled", allowed: []string{tools.ToolGenerateImage}, binding: "configured", want: true, lookups: 1},
		{name: "binding alone", allowed: []string{}, binding: "configured"},
		{name: "allowlist alone", allowed: []string{tools.ToolGenerateImage}},
		{name: "shared agent", allowed: []string{tools.ToolGenerateImage}, binding: "configured", shared: true},
		{name: "inaccessible model", allowed: []string{tools.ToolGenerateImage}, binding: "configured", unavailable: true, lookups: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := &imageRegistrationModelService{}
			if tc.unavailable {
				models.err = errors.New("model unavailable")
			}
			s := &agentService{modelService: models, imageGenerationService: &imageRegistrationService{}}
			registry := tools.NewToolRegistry()
			cfg := &types.AgentConfig{AllowedTools: tc.allowed, ImageGenerationModelID: tc.binding, SharedAgentReadOnly: tc.shared}
			require.NoError(t, s.registerTools(context.Background(), registry, cfg, nil, nil, "session"))
			_, err := registry.GetTool(tools.ToolGenerateImage)
			require.Equal(t, tc.want, err == nil)
			require.Equal(t, tc.lookups, models.calls)
			require.False(t, tools.CanRunConcurrently(tools.ToolGenerateImage))
		})
	}
}

func TestAgentConfigCarriesImageGenerationBinding(t *testing.T) {
	svc := &sessionService{cfg: &config.Config{}, webSearchProviderRepo: &sharedAgentWebSearchRepo{}}
	req := &types.QARequest{Session: &types.Session{ID: "session", TenantID: 1}, CustomAgent: &types.CustomAgent{TenantID: 1, Config: types.CustomAgentConfig{AgentMode: types.AgentModeSmartReasoning, ImageGenerationModelID: "image-config"}}}
	cfg, err := svc.buildAgentConfig(t.Context(), req, &types.Tenant{ID: 1}, 1)
	require.NoError(t, err)
	require.Equal(t, "image-config", cfg.ImageGenerationModelID)
}
