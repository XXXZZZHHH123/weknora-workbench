package vlm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/api"
)

func TestVLMRateLimitDelayHonoursVendorHints(t *testing.T) {
	limited := &api.HTTPError{StatusCode: http.StatusTooManyRequests, Header: http.Header{},
		Body: `{"code":"GW_RATE_LIMITED","retry_after_ms":7500}`}
	if wait, ok := vlmRateLimitDelay(limited, 1); !ok || wait != 7500*time.Millisecond {
		t.Fatalf("retry_after_ms: got %s %v", wait, ok)
	}
	limited.Header.Set("Retry-After", "120")
	if wait, _ := vlmRateLimitDelay(limited, 1); wait != vlmRateLimitMaxWait {
		t.Fatalf("cap: got %s", wait)
	}
	if wait, ok := vlmRateLimitDelay(&api.HTTPError{StatusCode: http.StatusServiceUnavailable}, 2); !ok || wait != 4*time.Second {
		t.Fatalf("backoff: got %s %v", wait, ok)
	}
	for _, err := range []error{nil, errors.New("boom"), &api.HTTPError{StatusCode: http.StatusBadRequest}} {
		if _, ok := vlmRateLimitDelay(err, 1); ok {
			t.Fatalf("%v must not retry", err)
		}
	}
}

func TestRemoteAPIVLMRetriesRateLimitedRequests(t *testing.T) {
	withVLMSSRFWhitelist(t, "127.0.0.1")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"GW_RATE_LIMITED","retry_after_ms":10}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"chart text"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	v, err := NewRemoteAPIVLM(&Config{BaseURL: server.URL, ModelName: "vision", APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Predict(context.Background(), [][]byte{testPNG}, "read")
	if err != nil || got != "chart text" || calls.Load() != 2 {
		t.Fatalf("got %q err=%v calls=%d", got, err, calls.Load())
	}
}
