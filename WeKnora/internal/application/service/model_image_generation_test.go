package service

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
	"testing"
)

type imageGenerationModelRepo struct {
	*stubCopyModelRepo
	tenant uint64
}

func (r *imageGenerationModelRepo) GetByID(ctx context.Context, tenant uint64, id string) (*types.Model, error) {
	r.tenant = tenant
	return r.stubCopyModelRepo.GetByID(ctx, tenant, id)
}
func TestGetImageGenerationModelChecksTypeStatusAndTenant(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "images.example.com")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	row := &types.Model{ID: "image-model", Type: types.ModelTypeImageGeneration, Source: types.ModelSourceRemote, Status: types.ModelStatusActive, Name: "operator-image-model", Parameters: types.ModelParameters{Provider: "generic", BaseURL: "https://images.example.com/v1"}}
	repo := &imageGenerationModelRepo{stubCopyModelRepo: &stubCopyModelRepo{source: row}}
	service := NewModelService(repo, nil, nil, nil, nil, nil)
	instance, err := service.GetImageGenerationModel(ctx, "image-model")
	require.NoError(t, err)
	require.Equal(t, "operator-image-model", instance.GetModelName())
	require.Equal(t, uint64(7), repo.tenant)
	_, err = service.GetChatModel(ctx, "image-model")
	require.Error(t, err)
	for _, change := range []func(){func() { row.Type = types.ModelTypeKnowledgeQA }, func() { row.Type = types.ModelTypeImageGeneration; row.Status = types.ModelStatusDownloadFailed }, func() { row.Status = types.ModelStatusActive; row.Source = types.ModelSourceLocal }} {
		change()
		_, err = service.GetImageGenerationModel(ctx, "image-model")
		require.Error(t, err)
	}
	_, err = service.GetImageGenerationModel(ctx, "")
	require.Error(t, err)
	_, err = service.GetImageGenerationModel(ctx, "missing")
	require.ErrorIs(t, err, ErrModelNotFound)
}
