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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func imageDraftContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
	return context.WithValue(ctx, types.UserIDContextKey, "test-admin")
}
func runImageDraftCheck(t *testing.T, h *InitializationHandler, ctx context.Context, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	enabled := true
	router.POST("/api/v1/initialization/remote/check", middleware.RequireRole(types.TenantRoleAdmin, &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}), h.CheckRemoteModel)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/initialization/remote/check", bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	return result
}
func imageDraftFixture(t *testing.T, status int, response string) (string, *atomic.Int32, *map[string]any) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	calls := &atomic.Int32{}
	wire := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/draft/v1/images/generations", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
		wire["authorization"] = r.Header.Get("Authorization")
		wire["custom_route"] = r.Header.Get("X-Model-Route")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/draft/v1", calls, &wire
}
func imageDraftPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	return data.Bytes()
}
func imageDraftRequest(base string) map[string]any {
	return map[string]any{"modelType": "ImageGeneration", "imageGenerationTest": true, "modelName": "draft-model-name", "baseUrl": base, "provider": "generic", "apiKey": "typed-test-key", "extraConfig": map[string]string{"image_size": "1024x1024", "image_quality": "high"}}
}
func TestImageDraftCheckGeneratesOneTransientPreviewUsingUnsavedValues(t *testing.T) {
	imageBytes := imageDraftPNG(t)
	response, _ := json.Marshal(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	base, calls, wire := imageDraftFixture(t, http.StatusOK, string(response))
	req := imageDraftRequest(base)
	req["customHeaders"] = map[string]string{"X-Model-Route": "draft-route"}
	result := runImageDraftCheck(t, &InitializationHandler{}, imageDraftContext(), req)
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, "no-store", result.Header().Get("Cache-Control"))
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "draft-model-name", (*wire)["model"])
	require.Equal(t, "1024x1024", (*wire)["size"])
	require.Equal(t, "high", (*wire)["quality"])
	require.Equal(t, float64(1), (*wire)["n"])
	require.Equal(t, "Bearer typed-test-key", (*wire)["authorization"])
	require.Equal(t, "draft-route", (*wire)["custom_route"])
	var body struct {
		Data struct {
			Available bool `json:"available"`
			Image     struct {
				DataURL       string `json:"data_url"`
				Width, Height int
				MIME          string `json:"mime_type"`
			}
		}
	}
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
	require.True(t, body.Data.Available)
	require.Equal(t, 3, body.Data.Image.Width)
	require.Equal(t, 2, body.Data.Image.Height)
	require.Equal(t, "image/png", body.Data.Image.MIME)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body.Data.Image.DataURL, "data:image/png;base64,"))
	require.NoError(t, err)
	require.Equal(t, imageBytes, decoded)
	require.NotContains(t, result.Body.String(), "typed-test-key")
}
func TestImageDraftCheckReusesSavedKeyOnlyAfterTypedAuthorizedLookup(t *testing.T) {
	img := base64.StdEncoding.EncodeToString(imageDraftPNG(t))
	base, calls, wire := imageDraftFixture(t, 200, `{"data":[{"b64_json":"`+img+`"}]}`)
	lookups := 0
	h := &InitializationHandler{modelService: &stubFillSecretsModelService{getModelByID: func(ctx context.Context, id string) (*types.Model, error) {
		lookups++
		tenant, ok := types.TenantIDFromContext(ctx)
		require.True(t, ok)
		require.Equal(t, uint64(7), tenant)
		require.Equal(t, "saved-image", id)
		return &types.Model{ID: id, TenantID: 7, Type: types.ModelTypeImageGeneration, Source: types.ModelSourceRemote, Status: types.ModelStatusActive, Name: "stored-name", Parameters: types.ModelParameters{Provider: "generic", BaseURL: "https://stored.example/v1", APIKey: "saved-only-secret"}}, nil
	}}}
	req := imageDraftRequest(base)
	req["modelId"] = "saved-image"
	delete(req, "apiKey")
	req["modelType"] = "image-generation"
	result := runImageDraftCheck(t, h, imageDraftContext(), req)
	require.Equal(t, 200, result.Code)
	require.Contains(t, result.Body.String(), `"available":true`)
	require.Equal(t, 1, lookups)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "Bearer saved-only-secret", (*wire)["authorization"])
	require.Equal(t, "draft-model-name", (*wire)["model"])
	require.NotContains(t, result.Body.String(), "saved-only-secret")
}
func TestImageDraftCheckRejectsMissingConfirmationIdentityOrWrongTypeBeforeGeneration(t *testing.T) {
	base, calls, _ := imageDraftFixture(t, 200, `{}`)
	for _, name := range []string{"missing confirmation", "wrong request type", "missing identity", "viewer", "wrong stored type", "missing stored model", "other tenant", "builtin credentials", "local model", "invalid source"} {
		t.Run(name, func(t *testing.T) {
			request := imageDraftRequest(base)
			ctx := imageDraftContext()
			row := &types.Model{ID: "saved-image", TenantID: 7, Type: types.ModelTypeImageGeneration, Source: types.ModelSourceRemote, Status: types.ModelStatusActive}
			h := &InitializationHandler{modelService: &stubFillSecretsModelService{getModelByID: func(context.Context, string) (*types.Model, error) { return row, nil }}}
			want := 400
			switch name {
			case "missing confirmation":
				request["imageGenerationTest"] = false
			case "wrong request type":
				request["modelType"] = "KnowledgeQA"
			case "missing identity":
				ctx = context.Background()
				want = 403 // ordinary route Admin gate rejects before handler
			case "viewer":
				ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
				want = 403
			case "wrong stored type":
				request["modelId"] = "saved-image"
				row.Type = types.ModelTypeKnowledgeQA
			case "missing stored model":
				request["modelId"] = "saved-image"
				h.modelService = &stubFillSecretsModelService{getModelByID: func(context.Context, string) (*types.Model, error) { return nil, errors.New("upstream key secret") }}
				want = 404
			case "other tenant":
				request["modelId"] = "saved-image"
				row.TenantID = 8
				want = 404
			case "builtin credentials":
				request["modelId"] = "saved-image"
				row.IsBuiltin = true
				want = 403
			case "local model":
				request["modelId"] = "saved-image"
				row.Source = types.ModelSourceLocal
			case "invalid source":
				request["source"] = "local"
			}
			result := runImageDraftCheck(t, h, ctx, request)
			require.Equal(t, want, result.Code)
			require.NotContains(t, result.Body.String(), "upstream key secret")
		})
	}
	require.Zero(t, calls.Load())
}
func TestImageDraftCheckFailureDiagnosticIsSafeAndNotRetried(t *testing.T) {
	base, calls, _ := imageDraftFixture(t, 400, `{"error":{"code":"invalid_request_error","param":"size","message":"PRIVATE_PROVIDER_MESSAGE https://secret.invalid?key=hidden"}}`)
	result := runImageDraftCheck(t, &InitializationHandler{}, imageDraftContext(), imageDraftRequest(base))
	require.Equal(t, 200, result.Code)
	require.Equal(t, int32(1), calls.Load())
	require.Contains(t, result.Body.String(), `"available":false`)
	require.Contains(t, result.Body.String(), `"http_status":400`)
	require.Contains(t, result.Body.String(), `"code":"invalid_request_error"`)
	require.Contains(t, result.Body.String(), `"param":"size"`)
	require.Contains(t, result.Body.String(), "HTTP 400")
	require.NotContains(t, result.Body.String(), "PRIVATE_PROVIDER_MESSAGE")
	require.NotContains(t, result.Body.String(), "secret.invalid")
	require.NotContains(t, result.Body.String(), "typed-test-key")
}
func TestImageDraftCheckRejectsInvalidImageAndInvalidParameters(t *testing.T) {
	base, calls, _ := imageDraftFixture(t, 200, `{"data":[{"b64_json":"SGVsbG8="}]}`)
	result := runImageDraftCheck(t, &InitializationHandler{}, imageDraftContext(), imageDraftRequest(base))
	require.Equal(t, 200, result.Code)
	require.Contains(t, result.Body.String(), `"available":false`)
	require.NotContains(t, result.Body.String(), "data_url")
	require.Equal(t, int32(1), calls.Load())
	req := imageDraftRequest(base)
	req["extraConfig"] = map[string]string{"image_size": "1024X1024"}
	result = runImageDraftCheck(t, &InitializationHandler{}, imageDraftContext(), req)
	require.Contains(t, result.Body.String(), `"available":false`)
	require.Contains(t, result.Body.String(), `"kind":"configuration"`)
	require.Equal(t, int32(1), calls.Load())
}

func TestImageDraftCheckWrongExplicitTypeOmitsCredentialsFromLogs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	localLogger := logrus.New()
	localLogger.SetOutput(&logs)
	localLogger.SetFormatter(&logrus.JSONFormatter{})
	router := gin.New()
	router.ContextWithFallback = true
	router.Use(middleware.Logger(), middleware.ErrorHandler())
	router.POST("/api/v1/initialization/remote/check", (&InitializationHandler{}).CheckRemoteModel)
	requestBody := `{"modelType":"ImageGenerationTypo","imageGenerationTest":true,"modelName":"draft-model","baseUrl":"https://images.example.test/v1","apiKey":"private-draft-key","customHeaders":{"X-Provider-Credential":"private-draft-header"}}`
	ctx := context.WithValue(imageDraftContext(), types.LoggerContextKey, logrus.NewEntry(localLogger))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/initialization/remote/check", strings.NewReader(requestBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, logs.String(), `"status_code":400`)
	require.NotContains(t, logs.String(), "private-draft-key")
	require.NotContains(t, logs.String(), "private-draft-header")
	require.NotContains(t, logs.String(), "request_body")
	require.NotContains(t, logs.String(), "response_body")
}
