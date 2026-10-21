package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestModelConnectionDiagnosticUsesStatusNotVendorText(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
	}{
		{400, "request_rejected"}, {422, "request_rejected"}, {401, "access_denied"}, {403, "access_denied"},
		{404, "not_found"}, {429, "rate_limited"}, {503, "upstream_unavailable"}, {409, "unknown"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &api.HTTPError{StatusCode: tc.status, Body: `{"message":"any vendor message"}`})
			d := modelConnectionDiagnostic(err, false)
			require.Equal(t, tc.kind, d["kind"])
			require.Equal(t, tc.status, d["http_status"])
		})
	}
	require.Equal(t, "unknown", modelConnectionDiagnostic(fmt.Errorf("Field required: input.contents"), false)["kind"])
	require.Equal(t, "configuration", modelConnectionDiagnostic(fmt.Errorf("invalid override"), true)["kind"])
	require.Equal(t, "connection", modelConnectionDiagnostic(&api.TransportError{Op: "send", Err: fmt.Errorf("timeout")}, false)["kind"])
}

func TestEmbeddingConnectionDraftUsesSelectedWireProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			calls := 0
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/embed", r.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "arbitrary-model-alias", body["model"])
				if native {
					input, ok := body["input"].(map[string]any)
					require.True(t, ok)
					contents := input["contents"].([]any)
					require.Equal(t, "hello", contents[0].(map[string]any)["text"])
					fmt.Fprint(w, `{"output":{"embeddings":[{"index":0,"embedding":[0.1,0.2]}]}}`)
				} else {
					require.Equal(t, []any{"hello"}, body["input"])
					fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
				}
			}))
			defer gateway.Close()
			path := ""
			if !native {
				path = "/embed"
			}
			compat := map[string]any{"path": path}
			if native {
				compat["api"] = "dashscope-embeddings"
			}
			base := gateway.URL
			if native {
				base += "/embed"
			}
			payload := map[string]any{"source": "remote", "provider": "generic", "modelName": "arbitrary-model-alias", "baseUrl": base, "spec": map[string]any{"compat": compat}}
			result := runEmbeddingDraftTest(t, payload)
			require.Equal(t, true, result["available"])
			require.Equal(t, float64(2), result["dimension"])
			require.Equal(t, 1, calls)
		})
	}
}

func runEmbeddingDraftTest(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/initialization/embedding/test", strings.NewReader(string(raw)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.TenantInfoContextKey, &types.Tenant{ID: 10042}))
	(&InitializationHandler{}).TestEmbeddingModel(c)
	require.Empty(t, c.Errors)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body.Data
}

func TestEmbeddingConnectionRejectionReturnsDiagnosticAndOriginalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	calls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
		fmt.Fprint(w, `{"detail":"opaque parameter refusal","request_id":"fixture-reference"}`)
	}))
	defer gateway.Close()
	result := runEmbeddingDraftTest(t, map[string]any{"source": "remote", "provider": "generic", "modelName": "arbitrary-alias", "baseUrl": gateway.URL})
	require.Equal(t, false, result["available"])
	require.Contains(t, result["message"], "fixture-reference")
	require.Equal(t, "request_rejected", result["diagnostic"].(map[string]any)["kind"])
	require.Equal(t, float64(400), result["diagnostic"].(map[string]any)["http_status"])
	require.Equal(t, 1, calls, "a rejected request must not be retried with another protocol")
}
