package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/api/anthropicmessages"
	"github.com/Tencent/WeKnora/internal/models/api/googlegenai"
	"github.com/Tencent/WeKnora/internal/models/api/openaicompletions"
	"github.com/Tencent/WeKnora/internal/models/api/openairesponses"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
)

type imageFallbackClient interface {
	Chat(context.Context, []api.Message, *api.Options) (*types.ChatResponse, error)
	ChatStream(context.Context, []api.Message, *api.Options) (<-chan types.StreamResponse, error)
}

func TestImageFallbackPreservesRequiredEvidenceAcrossProtocols(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	t.Setenv("SSRF_WHITELIST_EXTRA", "")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	factories := map[string]func(api.Endpoint) imageFallbackClient{
		"completions": func(e api.Endpoint) imageFallbackClient {
			return openaicompletions.New(openaicompletions.Config{Endpoint: e, Settings: api.DefaultOpenAICompletions()})
		},
		"responses": func(e api.Endpoint) imageFallbackClient {
			return openairesponses.New(openairesponses.Config{Endpoint: e})
		},
		"anthropic": func(e api.Endpoint) imageFallbackClient {
			return anthropicmessages.New(anthropicmessages.Config{Endpoint: e})
		},
		"google": func(e api.Endpoint) imageFallbackClient { return googlegenai.New(googlegenai.Config{Endpoint: e}) },
	}
	const imageData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZ1kAAAAASUVORK5CYII="
	for protocol, factory := range factories {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"nil-options", "default", "required"} {
				t.Run(protocol+"/"+mode+map[bool]string{false: "/chat", true: "/stream"}[stream], func(t *testing.T) {
					var bodies []string
					var mu sync.Mutex
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						require.NoError(t, err)
						mu.Lock()
						bodies = append(bodies, string(body))
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"image not supported","code":"fixture_image_rejected"}}`))
					}))
					t.Cleanup(server.Close)
					client := factory(api.Endpoint{URL: server.URL, Model: "synthetic", Client: server.Client()})
					var options *api.Options
					if mode != "nil-options" {
						options = &api.Options{DisableImageFallback: mode == "required"}
					}
					messages := []api.Message{{Role: "user", Content: "Read the image", Images: []string{"data:image/png;base64," + imageData}}}
					var err error
					if stream {
						_, err = client.ChatStream(context.Background(), messages, options)
					} else {
						_, err = client.Chat(context.Background(), messages, options)
					}
					var upstream *api.HTTPError
					require.True(t, errors.As(err, &upstream), "the actual provider rejection must remain available")
					require.Equal(t, 400, upstream.StatusCode)
					require.Contains(t, upstream.Body, "fixture_image_rejected")
					mu.Lock()
					defer mu.Unlock()
					want := 2
					if mode == "required" {
						want = 1
					}
					require.Len(t, bodies, want)
					require.Contains(t, bodies[0], imageData)
					if len(bodies) == 2 {
						require.NotContains(t, bodies[1], imageData)
					}
					for _, body := range bodies {
						require.False(t, strings.Contains(body, "DisableImageFallback"))
					}
					require.Equal(t, []string{"data:image/png;base64," + imageData}, messages[0].Images, "fallback must not mutate the original evidence")
				})
			}
		}
	}
}
