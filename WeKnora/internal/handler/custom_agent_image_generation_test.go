package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type imageBindingModelLookup struct {
	interfaces.ModelService
	model  *types.Model
	err    error
	calls  int
	tenant uint64
}

func (m *imageBindingModelLookup) GetModelByID(ctx context.Context, _ string) (*types.Model, error) {
	m.calls++
	m.tenant, _ = types.TenantIDFromContext(ctx)
	return m.model, m.err
}
func TestImageGenerationBindingRequiresAccessibleActiveOutputModel(t *testing.T) {
	cases := []struct {
		name   string
		model  *types.Model
		err    error
		status int
	}{
		{name: "accessible", model: &types.Model{Type: types.ModelTypeImageGeneration, Status: types.ModelStatusActive}},
		{name: "missing", err: errors.New("not found or other tenant"), status: http.StatusBadRequest},
		{name: "chat cannot substitute", model: &types.Model{Type: types.ModelTypeKnowledgeQA, Status: types.ModelStatusActive}, status: http.StatusBadRequest},
		{name: "inactive", model: &types.Model{Type: types.ModelTypeImageGeneration, Status: types.ModelStatusDownloadFailed}, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &imageBindingModelLookup{model: tc.model, err: tc.err}
			h := &CustomAgentHandler{modelService: lookup}
			err := h.validateAgentImageGenerationModel(agentTenantContext(), types.CustomAgentConfig{ImageGenerationModelID: "image-id"})
			if tc.status == 0 {
				require.NoError(t, err)
			} else {
				var app *apperrors.AppError
				require.ErrorAs(t, err, &app)
				require.Equal(t, tc.status, app.HTTPCode)
			}
			require.Equal(t, uint64(7), lookup.tenant)
			require.Equal(t, 1, lookup.calls)
		})
	}
}
func TestImageGenerationBindingEmptyKeepsExistingAgentsCompatible(t *testing.T) {
	h := &CustomAgentHandler{}
	require.NoError(t, h.validateAgentImageGenerationModel(context.Background(), types.CustomAgentConfig{}))
	raw, err := json.Marshal(types.CustomAgentConfig{ImageGenerationModelID: "image-a"})
	require.NoError(t, err)
	var back types.CustomAgentConfig
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, "image-a", back.ImageGenerationModelID)
}
