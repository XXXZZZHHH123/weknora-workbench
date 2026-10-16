package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/panjf2000/ants/v2"
	"github.com/stretchr/testify/require"
)

func shortEmbeddingPolicy() embeddingRequestPolicy {
	return embeddingRequestPolicy{
		attempts: 3, baseDelay: 10 * time.Millisecond, maxDelay: 20 * time.Millisecond,
		minInterval: 5 * time.Millisecond,
	}
}

func TestEmbeddingRequestDefaultDoesNotThrottleHealthySupplier(t *testing.T) {
	t.Setenv("EMBEDDING_REQUEST_INTERVAL", "")
	p, err := newEmbeddingRequestPolicy()
	require.NoError(t, err)
	require.Zero(t, p.interval, "normal throughput must not have a fixed delay")
	flow := &embeddingRequestFlow{}
	ctx, cancel := context.WithTimeout(types.WithBackgroundTask(context.Background()), 2*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := flow.run(ctx, "healthy-model", p, func() ([][]float32, error) {
				entered <- struct{}{}
				select {
				case <-release:
					return [][]float32{{1}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("healthy requests were serialized by pacing")
		}
	}
	close(release)
	for range 2 {
		require.NoError(t, <-errs)
	}
}

func TestEmbeddingRequestRecoversThroughputAfterRateLimit(t *testing.T) {
	flow := &embeddingRequestFlow{}
	flow.pause(0, time.Second)
	for range 200 {
		flow.succeeded(0)
	}
	flow.mu.Lock()
	interval := flow.interval
	flow.mu.Unlock()
	require.Zero(t, interval, "sustained success must restore unpaced throughput")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, flow.wait(ctx, 0))
	require.NoError(t, flow.wait(ctx, 0))
}

func TestEmbeddingRequestRecoveryPreservesExplicitOperatorLimit(t *testing.T) {
	flow := &embeddingRequestFlow{}
	flow.pause(0, time.Second)
	for range 200 {
		flow.succeeded(250 * time.Millisecond)
	}
	flow.mu.Lock()
	interval := flow.interval
	flow.mu.Unlock()
	require.Equal(t, 250*time.Millisecond, interval)
}

func TestEmbeddingRequestNewSupplierDoesNotInheritOldCooldown(t *testing.T) {
	t.Setenv("EMBEDDING_REQUEST_INTERVAL", "0s")
	old := newPooledEmbedder(t, func(http.ResponseWriter, *http.Request) {})
	oldRaw := old.(*concurrencyEmbedder).inner.(*protocolEmbedder)
	oldRaw.requestFlow.pause(time.Minute, time.Second)
	client := newPooledEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(answer(r.URL.Path, body)))
	})
	raw := client.(*concurrencyEmbedder).inner.(*protocolEmbedder)
	require.NotSame(t, oldRaw.requestFlow, raw.requestFlow)
	ctx, cancel := context.WithTimeout(types.WithBackgroundTask(context.Background()), 2*time.Second)
	defer cancel()
	got, err := client.BatchEmbed(ctx, []string{"text"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{4}}, got)
}

func TestEmbeddingRequestRetriesOnlyFailedProtocolBatch(t *testing.T) {
	t.Setenv("BATCH_EMBED_SIZE", "2")
	var mu sync.Mutex
	calls := map[string]int{}
	client := newPooledEmbedder(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Input, 1)
		text := body.Input[0]
		mu.Lock()
		calls[text]++
		n := calls[text]
		mu.Unlock()
		if text == "bb" && n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"AccountRateLimitExceeded"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{"index": 0, "embedding": []float32{float32(len(text))}}},
		})
	})
	raw := client.(*concurrencyEmbedder).inner.(*protocolEmbedder)
	raw.requestPolicy = shortEmbeddingPolicy()
	raw.settings.MaxBatchSize = 1 // exercise a vendor that splits a pool sub-batch
	got, err := client.BatchEmbedWithPool(types.WithBackgroundTask(context.Background()), client, []string{"a", "bb", "ccc"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{1}, {2}, {3}}, got)
	require.Equal(t, map[string]int{"a": 1, "bb": 2, "ccc": 1}, calls)
}

func TestEmbeddingRequestDoesNotRetryPermanentErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"auth", 401, ""},
		{"parameters", 400, ""},
		{"quota", 429, `{"error":{"code":"QuotaExceeded.FreeTrialQuotaExceeded"}}`},
		{"usage_limit", 429, `{"error":{"code":"SetLimitExceeded"}}`},
		{"insufficient_quota", 429, `{"error":{"type":"insufficient_quota"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow := &embeddingRequestFlow{}
			calls := 0
			want := &api.HTTPError{StatusCode: tc.status, Body: tc.body}
			_, err := flow.run(types.WithBackgroundTask(context.Background()), "model", shortEmbeddingPolicy(), func() ([][]float32, error) {
				calls++
				return nil, want
			})
			require.ErrorIs(t, err, want)
			require.Equal(t, 1, calls)
		})
	}
}

func TestEmbeddingRequestHonoursRetryAfter(t *testing.T) {
	flow := &embeddingRequestFlow{}
	calls := 0
	start := time.Now()
	_, err := flow.run(types.WithBackgroundTask(context.Background()), "model", shortEmbeddingPolicy(), func() ([][]float32, error) {
		calls++
		if calls == 1 {
			return nil, &api.HTTPError{StatusCode: 429, Header: http.Header{"Retry-After": {"1"}}}
		}
		return [][]float32{{1}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestEmbeddingRequestSharedCooldownExtendsQueuedWait(t *testing.T) {
	flow := &embeddingRequestFlow{}
	flow.pause(30*time.Millisecond, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- flow.wait(ctx, 0) }()
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	flow.pause(80*time.Millisecond, 0)
	require.NoError(t, <-done)
	require.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond)
}

func TestEmbeddingRequestPacesConcurrentDocuments(t *testing.T) {
	flow := &embeddingRequestFlow{}
	p := shortEmbeddingPolicy()
	p.interval = 15 * time.Millisecond
	ctx := types.WithBackgroundTask(context.Background())
	var mu sync.Mutex
	var starts []time.Time
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := flow.run(ctx, "same-model", p, func() ([][]float32, error) {
				mu.Lock()
				starts = append(starts, time.Now())
				mu.Unlock()
				return [][]float32{{1}}, nil
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Len(t, starts, 4)
	for i := 1; i < len(starts); i++ {
		require.GreaterOrEqual(t, starts[i].Sub(starts[i-1]), 14*time.Millisecond)
	}
}

func TestEmbeddingRequestCancellationStopsCooldown(t *testing.T) {
	flow := &embeddingRequestFlow{}
	ctx, cancel := context.WithCancel(types.WithBackgroundTask(context.Background()))
	calls := 0
	_, err := flow.run(ctx, "model", shortEmbeddingPolicy(), func() ([][]float32, error) {
		calls++
		cancel()
		return nil, &api.HTTPError{StatusCode: 429, Header: http.Header{"Retry-After": {"60"}}}
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestEmbeddingRequestRetryBudgetAndInteractiveQuery(t *testing.T) {
	for _, background := range []bool{true, false} {
		ctx := context.Background()
		wantCalls := 1
		if background {
			ctx = types.WithBackgroundTask(ctx)
			wantCalls = 3
		}
		flow := &embeddingRequestFlow{}
		calls := 0
		want := &api.HTTPError{StatusCode: 429}
		_, err := flow.run(ctx, "model", shortEmbeddingPolicy(), func() ([][]float32, error) {
			calls++
			return nil, want
		})
		require.ErrorIs(t, err, want)
		require.Equal(t, wantCalls, calls)
	}
}

type batchFailureEmbedder struct {
	Embedder
	calls atomic.Int32
	err   error
}

func (e *batchFailureEmbedder) BatchEmbed(ctx context.Context, _ []string) ([][]float32, error) {
	e.calls.Add(1)
	if e.err != nil {
		return nil, e.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEmbeddingPoolStopsOnFailureAndRejectsInvalidBatchSize(t *testing.T) {
	pool, err := ants.NewPool(1)
	require.NoError(t, err)
	defer pool.Release()
	pooler := NewBatchEmbedder(pool)
	want := errors.New("permanent failure")
	model := &batchFailureEmbedder{err: want}
	t.Setenv("BATCH_EMBED_SIZE", "1")
	_, err = pooler.BatchEmbedWithPool(context.Background(), model, []string{"a", "b", "c"})
	require.ErrorIs(t, err, want)
	require.EqualValues(t, 1, model.calls.Load())
	for _, value := range []string{"0", "-1", "bad"} {
		t.Setenv("BATCH_EMBED_SIZE", value)
		_, err := pooler.BatchEmbedWithPool(context.Background(), model, []string{"a"})
		require.ErrorContains(t, err, "positive integer")
	}
}

func TestEmbeddingPoolCancellationDoesNotReturnEmptySuccess(t *testing.T) {
	t.Setenv("BATCH_EMBED_SIZE", "1")
	pool, err := ants.NewPool(2)
	require.NoError(t, err)
	defer pool.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = NewBatchEmbedder(pool).BatchEmbedWithPool(ctx, &batchFailureEmbedder{}, []string{"a", "b", "c"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
