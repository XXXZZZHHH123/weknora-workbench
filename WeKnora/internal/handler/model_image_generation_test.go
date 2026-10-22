package handler

import (
	"context"
	"github.com/Tencent/WeKnora/internal/models/providers"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageGenerationProviderMetadata(t *testing.T) {
	for _, id := range []string{providers.GenericID, providers.OpenaiID} {
		vendor, ok := modelruntime.Get(id)
		require.True(t, ok)
		dto := providerDTO(vendor, types.ModelTypeImageGeneration, true)
		require.Contains(t, dto.ModelTypes, "image-generation")
		if id == providers.OpenaiID {
			require.Equal(t, providers.OpenaiBaseURL, dto.DefaultURLs["image-generation"])
		}
	}
}
func TestSavedImageModelCannotUseChatConnectionCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	calls := 0
	handler := &InitializationHandler{modelService: &stubFillSecretsModelService{getModelByID: func(context.Context, string) (*types.Model, error) {
		calls++
		return &types.Model{Type: types.ModelTypeImageGeneration}, nil
	}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/initialization/remote/check", strings.NewReader(`{"modelId":"image-model","modelName":"anything","baseUrl":"https://images.example.com/v1"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.CheckRemoteModel(c)
	require.Len(t, c.Errors, 1)
	require.Contains(t, c.Errors[0].Error(), "only tests chat models")
	require.Equal(t, 1, calls)
}
