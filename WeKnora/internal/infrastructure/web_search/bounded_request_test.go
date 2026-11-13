package web_search

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedSearchRequestClassifiesAndLimitsAttempts(t *testing.T) {
	for _, status := range []int{403, 401, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
			}))
			defer server.Close()
			_, err := boundedSearchRequest(context.Background(), server.Client(), server.URL, nil, []byte(`{}`), time.Second)
			var failure *RequestFailure
			require.ErrorAs(t, err, &failure)
			expected := int32(1)
			if status == 429 || status == 503 {
				expected = 2
			}
			require.Equal(t, expected, calls.Load())
			require.Equal(t, int(expected), failure.Attempts)
		})
	}
}
func TestBoundedSearchRequestRecoversWithinSharedDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	started := time.Now()
	client := server.Client()
	client.Timeout = 100 * time.Millisecond
	body, err := boundedSearchRequest(context.Background(), client, server.URL, nil, nil, 800*time.Millisecond)
	require.NoError(t, err)
	require.Contains(t, string(body), "results")
	require.Equal(t, int32(2), calls.Load())
	require.Less(t, time.Since(started), time.Second)
}
func TestBoundedSearchRequestHonorsRetryAfterAndCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}))
	defer server.Close()
	_, err := boundedSearchRequest(context.Background(), server.Client(), server.URL, nil, nil, time.Second)
	var failure *RequestFailure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, time.Minute, failure.RetryAfter)
	require.Equal(t, int32(1), calls.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = boundedSearchRequest(ctx, server.Client(), server.URL, nil, nil, time.Second)
	require.True(t, errors.Is(err, context.Canceled))
	require.Equal(t, int32(1), calls.Load())
}

func TestBoundedSearchPreservesFirstAttemptWindow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(180 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	_, err := boundedSearchRequest(context.Background(), server.Client(), server.URL, nil, nil, 300*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Greater(t, retryDelay("90000", time.Now()), 24*time.Hour)
	require.Greater(t, retryDelay("99999999999999999999999", time.Now()), 24*time.Hour)
}
