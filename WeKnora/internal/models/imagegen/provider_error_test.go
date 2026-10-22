package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderErrorOnlyExposesSafeStatusAndProtocolIdentifiers(t *testing.T) {
	e := providerError(422, `{"error":{"code":"invalid_request_error","param":"size","message":"PRIVATE MESSAGE https://secret.invalid?api_key=private-key"}}`, []string{"private-key"})
	require.Equal(t, map[string]any{"kind": "provider_http", "http_status": 422, "code": "invalid_request_error", "param": "size"}, e.Diagnostic())
	require.Contains(t, e.Error(), "HTTP 422")
	require.Contains(t, e.Error(), "param=size")
	require.NotContains(t, e.Error(), "PRIVATE")
	require.NotContains(t, e.Error(), "secret.invalid")
	for _, bad := range []string{"https://secret.invalid/", "private-key", "not a protocol identifier", strings.Repeat("a", 65), "private-key-suffix"} {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": bad, "param": bad, "message": bad}})
		e = providerError(401, string(raw), []string{"private-key"})
		require.Equal(t, map[string]any{"kind": "provider_http", "http_status": 401}, e.Diagnostic())
		require.NotContains(t, e.Error(), bad)
	}
	raw := &ProviderError{StatusCode: -2, Code: "https://secret.invalid", Param: "arbitrary text"}
	require.Equal(t, map[string]any{"kind": "provider_http"}, raw.Diagnostic())
	require.Equal(t, "image generation provider rejected the request", raw.Error())
}
func TestGenerateReturnsTypedSafeProviderErrorWithoutRetry(t *testing.T) {
	loopback(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","param":"secret-route-value","message":"PRIVATE_PROVIDER_MESSAGE"}}`))
	}))
	defer server.Close()
	g, err := New(&Config{ModelName: "configured-model", Provider: "generic", BaseURL: server.URL, APIKey: "secret-key", CustomHeaders: map[string]string{"X-Route": "secret-route-value"}})
	require.NoError(t, err)
	_, err = g.Generate(context.Background(), Request{Prompt: "Connection test image"})
	require.Error(t, err)
	var safe *ProviderError
	require.True(t, errors.As(err, &safe))
	require.Equal(t, 429, safe.StatusCode)
	require.Equal(t, "rate_limit_exceeded", safe.Code)
	require.Empty(t, safe.Param)
	require.NotContains(t, err.Error(), "PRIVATE_PROVIDER_MESSAGE")
	require.NotContains(t, err.Error(), "secret-route-value")
	require.Equal(t, int32(1), calls.Load())
}
