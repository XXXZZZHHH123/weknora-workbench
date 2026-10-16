package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
)

// Request pacing is shared by all clients for the same model in this process,
// including separate document jobs. The existing distributed concurrency gate
// remains in force. This is not an account-wide or cross-replica TPM limiter.
var embeddingRequestFlows sync.Map

type embeddingRequestFlow struct {
	mu        sync.Mutex
	next      time.Time
	cooldown  time.Time
	interval  time.Duration
	successes int
}

type embeddingRequestPolicy struct {
	interval    time.Duration
	attempts    int
	baseDelay   time.Duration
	maxDelay    time.Duration
	minInterval time.Duration
	jitter      func(time.Duration) time.Duration
}

func newEmbeddingRequestPolicy() (embeddingRequestPolicy, error) {
	p := embeddingRequestPolicy{
		attempts: 6, baseDelay: 5 * time.Second, maxDelay: time.Minute,
		minInterval: time.Second,
		jitter: func(d time.Duration) time.Duration {
			return time.Duration(rand.Int64N(int64(d/4) + 1))
		},
	}
	if value := strings.TrimSpace(os.Getenv("EMBEDDING_REQUEST_INTERVAL")); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval < 0 {
			return p, fmt.Errorf("EMBEDDING_REQUEST_INTERVAL must be a non-negative duration")
		}
		p.interval = interval
	}
	return p, nil
}

func embeddingFlow(key string) *embeddingRequestFlow {
	flow, _ := embeddingRequestFlows.LoadOrStore(key, &embeddingRequestFlow{})
	return flow.(*embeddingRequestFlow)
}

func waitEmbeddingRequest(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Waiters recheck the shared cooldown instead of reserving future slots: a
// sibling request receiving 429 must also stop requests that are already queued.
func (f *embeddingRequestFlow) wait(ctx context.Context, interval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.mu.Lock()
		now := time.Now()
		ready := f.next
		if f.cooldown.After(ready) {
			ready = f.cooldown
		}
		if !ready.After(now) {
			f.next = now.Add(max(interval, f.interval))
			f.mu.Unlock()
			return nil
		}
		f.mu.Unlock()
		if err := waitEmbeddingRequest(ctx, time.Until(ready)); err != nil {
			return err
		}
	}
}

func (f *embeddingRequestFlow) pause(delay, interval time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cooldown = maxTime(f.cooldown, time.Now().Add(delay))
	f.interval = max(f.interval, interval)
	f.successes = 0
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (f *embeddingRequestFlow) succeeded(floor time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes++
	// Recover throughput gradually after sustained success, preserving the
	// operator's configured minimum interval.
	if f.successes >= 20 {
		f.successes = 0
		f.interval /= 2
		if f.interval < 10*time.Millisecond {
			f.interval = 0
		}
		f.interval = max(f.interval, floor)
	}
}

func embeddingRetryStatus(err error) (*api.HTTPError, bool) {
	var reply *api.HTTPError
	if !errors.As(err, &reply) {
		return nil, false // protocol clients already retry transport failures
	}
	if reply.StatusCode == http.StatusTooManyRequests {
		var body struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(reply.Body), &body)
		for _, code := range []string{body.Error.Code, body.Error.Type} {
			code = strings.ToLower(code)
			if strings.HasPrefix(code, "quotaexceeded") || strings.HasPrefix(code, "setlimitexceeded") || code == "insufficient_quota" {
				return reply, false // exhausted entitlement needs operator action
			}
		}
		return reply, true
	}
	switch reply.StatusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return reply, true
	}
	return reply, false
}

// run retries exactly one protocol request, not an entire document or a pool
// batch. Successful sibling requests remain in the caller's result slice.
// Interactive query embedding retains its existing latency/error behaviour.
func (f *embeddingRequestFlow) run(ctx context.Context, modelID string, p embeddingRequestPolicy,
	send func() ([][]float32, error),
) ([][]float32, error) {
	if !types.IsBackgroundTask(ctx) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return send()
	}
	delay := p.baseDelay
	for attempt := 0; ; attempt++ {
		if err := f.wait(ctx, p.interval); err != nil {
			return nil, err
		}
		vectors, err := send()
		if err == nil {
			f.succeeded(p.interval)
			return vectors, nil
		}
		reply, retry := embeddingRetryStatus(err)
		if !retry {
			return nil, err
		}
		wait := delay
		if p.jitter != nil {
			wait += p.jitter(delay)
		}
		wait = max(wait, reply.RetryAfter())
		if reply.StatusCode == http.StatusTooManyRequests {
			// Even an exhausted caller must tell other document jobs to stop.
			f.pause(wait, max(p.interval, p.minInterval))
		}
		if attempt+1 >= p.attempts {
			return nil, err
		}
		logger.Warnf(ctx, "Embedding request retry: model_id=%s status=%d attempt=%d/%d wait=%s",
			modelID, reply.StatusCode, attempt+1, p.attempts, wait)
		if err := waitEmbeddingRequest(ctx, wait); err != nil {
			return nil, err
		}
		delay *= 2
		if delay > p.maxDelay {
			delay = p.maxDelay
		}
	}
}
