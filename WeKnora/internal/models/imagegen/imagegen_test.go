package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
)

func loopback(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}
func picture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	return b.Bytes()
}
func TestGenerateSingleImagePreservesConfigAndBytes(t *testing.T) {
	loopback(t)
	img := picture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/gateway/v1/images/generations", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		require.Equal(t, "custom", r.Header.Get("X-Model-Route"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "configurable-image-model", body["model"])
		require.Equal(t, float64(1), body["n"])
		require.Equal(t, "A mountain at dawn", body["prompt"])
		require.Equal(t, "1536x1024", body["size"])
		require.Equal(t, "high", body["quality"])
		require.NotContains(t, body, "response_format")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(img), "revised_prompt": "Mountain illustration"}}})
	}))
	defer server.Close()
	g, err := New(&Config{ModelID: "row-id", ModelName: "configurable-image-model", Provider: "generic", BaseURL: server.URL + "/gateway/v1", APIKey: "secret", ExtraConfig: map[string]string{"image_size": "1536x1024", "image_quality": "high"}, CustomHeaders: map[string]string{"X-Model-Route": "custom", "Authorization": "bad"}})
	require.NoError(t, err)
	result, err := g.Generate(context.Background(), Request{Prompt: "A mountain at dawn"})
	require.NoError(t, err)
	require.Equal(t, img, result.Data)
	require.Equal(t, "image/png", result.MIMEType)
	require.Equal(t, 3, result.Width)
	require.Equal(t, 2, result.Height)
	require.Equal(t, "Mountain illustration", result.RevisedPrompt)
	require.Equal(t, int32(1), calls.Load())
}
func TestGenerateDownloadsURLWithoutCredentials(t *testing.T) {
	loopback(t)
	img := picture(t)
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("X-Secret"))
		_, _ = w.Write(img)
	}))
	defer blob.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"url": blob.URL + "/generated.png?signature=opaque"}}})
	}))
	defer server.Close()
	g, err := New(&Config{ModelName: "model", Provider: "generic", BaseURL: server.URL + "/v1/images/generations", APIKey: "secret", CustomHeaders: map[string]string{"X-Secret": "private"}})
	require.NoError(t, err)
	result, err := g.Generate(context.Background(), Request{Prompt: "image"})
	require.NoError(t, err)
	require.Equal(t, img, result.Data)
}
func TestGenerateRejectsMalformedAndOversizedResultsWithoutRetry(t *testing.T) {
	loopback(t)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"upstream error", `{"error":{"message":"secret provider content"}}`, 429},
		{"empty", `{"data":[]}`, 200},
		{"multiple", `{"data":[{},{}]}`, 200},
		{"bad base64", `{"data":[{"b64_json":"not base64"}]}`, 200},
		{"not image", `{"data":[{"b64_json":"SGVsbG8="}]}`, 200},
		{"unsafe URL", `{"data":[{"url":"file:///etc/passwd"}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			g, err := New(&Config{ModelName: "model", Provider: "generic", BaseURL: server.URL})
			require.NoError(t, err)
			result, err := g.Generate(context.Background(), Request{Prompt: "image"})
			require.Error(t, err)
			require.Nil(t, result)
			require.NotContains(t, err.Error(), "secret provider content")
			require.Equal(t, int32(1), calls.Load())
		})
	}
	_, err := ValidateImage(make([]byte, MaxImageBytes+1))
	require.Error(t, err)
	_, err = readBounded(strings.NewReader("12345"), 4)
	require.Error(t, err)
	img := picture(t)
	_, err = ValidateImage(img[:len(img)-8])
	require.Error(t, err)
}
func TestGenerateRejectsInvalidInputBeforeRequest(t *testing.T) {
	loopback(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	g, err := New(&Config{ModelName: "model", Provider: "generic", BaseURL: server.URL})
	require.NoError(t, err)
	for _, in := range []Request{{Prompt: " "}, {Prompt: "image", Size: "99999x99999"}, {Prompt: "image", Quality: strings.Repeat("x", 41)}} {
		_, err = g.Generate(context.Background(), in)
		require.Error(t, err)
	}
	require.Zero(t, calls.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = g.Generate(ctx, Request{Prompt: "image"})
	require.Error(t, err)
	require.Zero(t, calls.Load())
}
func TestNewRejectsUnsupportedProvidersAndInvalidConfig(t *testing.T) {
	for _, cfg := range []*Config{nil, {ModelName: ""}, {ModelName: "image", Provider: "deepseek", BaseURL: "https://example.com"}, {ModelName: "image", Provider: "generic"}, {ModelName: "image", Provider: "generic", BaseURL: "https://user:secret@example.com/v1"}, {ModelName: "image", Provider: "generic", BaseURL: "https://example.com/v1?secret=x"}, {ModelName: "image", Provider: "generic", BaseURL: "https://example.com/v1", ExtraConfig: map[string]string{"image_timeout_seconds": "-1"}}} {
		_, err := New(cfg)
		require.Error(t, err)
	}
}
