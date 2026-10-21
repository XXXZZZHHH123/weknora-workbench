package runtime

import (
	"github.com/Tencent/WeKnora/internal/models"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImageGenerationResolutionIsIndependentOfChat(t *testing.T) {
	for _, provider := range []string{"generic", "openai"} {
		r, err := Resolve(Ref{Provider: provider, Model: "operator-chosen-image-model", BaseURL: "https://images.example.com/v1", ModelType: types.ModelTypeImageGeneration})
		require.NoError(t, err)
		require.Equal(t, "operator-chosen-image-model", r.RemoteModel)
		require.Equal(t, "https://images.example.com/v1", r.BaseURL)
	}
	_, err := Resolve(Ref{Provider: "deepseek", Model: "image", ModelType: types.ModelTypeImageGeneration})
	require.Error(t, err)
	_, err = Resolve(Ref{Provider: "generic", Model: "image", BaseURL: "https://example.com", ModelType: types.ModelTypeImageGeneration, Override: &types.ModelSpecOverride{API: "openai-responses"}})
	require.Error(t, err)
	for _, value := range []string{"ImageGeneration", "image-generation"} {
		kind, ok := models.ParseModelType(value)
		require.True(t, ok)
		require.Equal(t, types.ModelTypeImageGeneration, kind)
	}
}
func TestImageGenerationParametersValidatedWhenSaved(t *testing.T) {
	for _, extra := range []map[string]string{{"image_size": "huge"}, {"image_size": "9000x64"}, {"image_quality": "bad\nvalue"}, {"image_timeout_seconds": "601"}, {"image_response_format": "xml"}, {models.ExtraAPI: "openai-completions"}} {
		err := ValidateRow("image-model", types.ModelTypeImageGeneration, &types.ModelParameters{Provider: "generic", BaseURL: "https://images.example.com", ExtraConfig: extra})
		require.Error(t, err)
	}
	require.NoError(t, ValidateRow("image-model", types.ModelTypeImageGeneration, &types.ModelParameters{Provider: "generic", BaseURL: "https://images.example.com", Spec: &types.ModelSpecOverride{}, ExtraConfig: map[string]string{"image_size": "auto", "image_response_format": "b64_json"}}))
}
