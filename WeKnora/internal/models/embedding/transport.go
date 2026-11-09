package embedding

import (
	"fmt"
	"net/http"
	"time"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// sharedEmbeddingHTTPTransport keeps a single SSRF-safe connection pool for
// all embedding clients. Embedders are recreated as model configuration changes,
// but their outbound connections can be safely reused across client instances,
// so the transport (and its keep-alive pool) is built once at package load.
var sharedEmbeddingHTTPTransport = proxiedSSRFSafeTransport()

// proxiedSSRFSafeTransport uses the same egress as chat models
// (internal/models/api/transport.go): a configured HTTP(S)_PROXY is honoured,
// while every request URL and redirect is still validated against SSRF.
// Without a proxy environment the connection is direct, as before.
func proxiedSSRFSafeTransport() *http.Transport {
	transport := secutils.NewSSRFSafeTransport(secutils.DefaultSSRFSafeHTTPClientConfig())
	transport.Proxy = http.ProxyFromEnvironment
	return transport
}

// validateEmbeddingBaseURL checks that a resolved embedding API base URL is safe
// for outbound requests. Empty URLs are allowed (callers apply provider defaults).
func validateEmbeddingBaseURL(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	if err := secutils.ValidateURLForSSRF(baseURL); err != nil {
		return fmt.Errorf("base URL SSRF check failed: %w", err)
	}
	return nil
}

// newEmbeddingHTTPClient returns an HTTP client with connection-level SSRF
// protection and redirect validation, aligned with internal/models/chat/transport.go.
// All clients share sharedEmbeddingHTTPTransport so keep-alive connections are
// pooled globally, while each keeps its own timeout.
func newEmbeddingHTTPClient(timeout time.Duration) *http.Client {
	cfg := secutils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = timeout
	return secutils.NewSSRFSafeHTTPClientWithTransport(cfg, sharedEmbeddingHTTPTransport)
}
