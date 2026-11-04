package docparser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRemotePDFDeferredPollingKeepsOneLeaseAndDoesNotWaitInWorker(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	pdf := []byte("%PDF-1.7 deferred fixture")
	sha := sha256.Sum256(pdf)
	posts, deletes, polls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "tenant:123", r.Header.Get("X-Parse-Scope"))
		switch {
		case r.Method == "DELETE":
			deletes++
			json.NewEncoder(w).Encode(map[string]any{"status": "cancelled"})
		case r.Method == "POST":
			posts++
			require.Equal(t, "86400", r.Header.Get("X-Parse-Queue-Timeout"))
			require.Equal(t, "7200", r.Header.Get("X-Parse-Execution-Timeout"))
			json.NewEncoder(w).Encode(map[string]any{"id": "job1", "status": "queued", "lease_id": "original-lease"})
		case r.URL.Path == "/v1/jobs/job1/result":
			json.NewEncoder(w).Encode(map[string]any{"schema_version": "remote_pdf_v1", "source": map[string]any{"sha256": hex.EncodeToString(sha[:]), "page_count": 1}, "pages": []any{map[string]any{"page": 1, "text": "prose"}}, "bundle": map[string]any{}})
		default:
			polls++
			status := "running"
			if polls > 1 {
				status = "completed"
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "job1", "status": status})
		}
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	reader := RemotePDFReader{endpoint: server.URL, token: "fixture"}
	req := &types.ReadRequest{FileContent: pdf, DeferRemotePDF: true}
	for i := 0; i < 2; i++ {
		child, cancel := context.WithTimeout(ctx, time.Second)
		result, err := reader.Read(child, req)
		cancel()
		require.Nil(t, result)
		var pending *RemotePDFPendingError
		require.ErrorAs(t, err, &pending)
		require.Equal(t, "original-lease", pending.Job.LeaseID)
		req.RemotePDFJob = &pending.Job
		require.Zero(t, deletes)
	}
	result, err := reader.Read(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, posts)
	require.Zero(t, deletes)
	require.Equal(t, 2, polls)
}
func TestRemotePDFDeferredQueueFullDoesNotFailUntilQueueBudget(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer server.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	reader := RemotePDFReader{endpoint: server.URL, token: "fixture"}
	req := &types.ReadRequest{FileContent: []byte("%PDF-fixture"), DeferRemotePDF: true}
	_, err := reader.Read(ctx, req)
	var pending *RemotePDFPendingError
	require.True(t, errors.As(err, &pending))
	require.NotZero(t, pending.Job.QueuedAt)
	pending.Job.QueuedAt = time.Now().Add(-25 * time.Hour).Unix()
	req.RemotePDFJob = &pending.Job
	_, err = reader.Read(ctx, req)
	require.ErrorContains(t, err, "QUEUE_TIMEOUT")
}

func TestRemotePDFDeferredTerminalPollNeverRestartsManualAction(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		require.Equal(t, "GET", r.Method)
		json.NewEncoder(w).Encode(map[string]any{"id": "existing", "status": "failed", "error": map[string]any{"code": "EXECUTION_TIMEOUT"}})
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	reader := RemotePDFReader{endpoint: server.URL, token: "fixture"}
	result, err := reader.Read(ctx, &types.ReadRequest{FileContent: []byte("%PDF-fixture"), DeferRemotePDF: true,
		ParserRetryKey: "original-manual-action", RemotePDFJob: &types.RemotePDFJobReference{ID: "existing", LeaseID: "saved-lease"}})
	require.NoError(t, err)
	require.Contains(t, result.Error, "EXECUTION_TIMEOUT")
	require.Zero(t, posts)
}

func TestRemotePDFDeferredManualRetryAlsoWaitsForCapacity(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	submissions, retries := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs":
			submissions++
			json.NewEncoder(w).Encode(map[string]any{"id": "existing", "status": "failed", "lease_id": "old"})
		case "/v1/jobs/existing/retry":
			retries++
			w.WriteHeader(429)
		case "/v1/capacity":
			w.WriteHeader(429)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	reader := RemotePDFReader{endpoint: server.URL, token: "fixture"}
	req := &types.ReadRequest{FileContent: []byte("%PDF-fixture"), DeferRemotePDF: true, ParserRetryKey: "manual-action"}
	for i := 0; i < 2; i++ {
		_, err := reader.Read(ctx, req)
		var pending *RemotePDFPendingError
		require.ErrorAs(t, err, &pending)
		require.Empty(t, pending.Job.ID)
		require.NotZero(t, pending.Job.QueuedAt)
		req.RemotePDFJob = &pending.Job
	}
	require.Equal(t, 1, submissions)
	require.Equal(t, 1, retries)
}
