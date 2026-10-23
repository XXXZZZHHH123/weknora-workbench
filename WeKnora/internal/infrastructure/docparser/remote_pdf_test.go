package docparser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

func TestRemotePDFResultScopeAndCoverage(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	pdf := []byte("%PDF-1.7 fixture")
	hash := sha256.Sum256(pdf)
	for _, tc := range []struct {
		name, sha string
		page      int
		wantErr   bool
	}{
		{"valid", hex.EncodeToString(hash[:]), 1, false},
		{"wrong_source", "wrong", 1, true},
		{"missing_page", hex.EncodeToString(hash[:]), 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Parse-Scope") != "tenant:123" || r.Header.Get("Authorization") != "Bearer service-test" {
					t.Error("missing trusted scope")
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/result") {
					json.NewEncoder(w).Encode(map[string]any{"schema_version": "remote_pdf_v1", "source": map[string]any{"sha256": tc.sha, "page_count": 1}, "pages": []any{map[string]any{"page": tc.page, "text": "内容", "markdown": "# 标题\n表格值 123"}}})
					return
				}
				if r.Method == "POST" {
					f, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					f.Close()
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "job1", "status": "completed", "lease_id": "lease1"})
			}))
			defer srv.Close()
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
			c := RemotePDFReader{endpoint: srv.URL, token: "service-test", poll: time.Millisecond}
			result, err := c.Read(ctx, &types.ReadRequest{FileContent: pdf, FileType: "pdf"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil {
				if len(result.SourceBlocks) != 1 || result.SourceBlocks[0].End != len([]rune(result.MarkdownContent)) {
					t.Fatal("source range not exact")
				}
			}
		})
	}
}

func TestRemotePDFFailurePreservesSafeCauseWithoutAutomaticRetry(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	for _, code := range []string{"MODEL_CONTENT_FILTER", "MODEL_AUTH_FAILED", "REMOTE_PDF_OUTPUT_INVALID", "secret-token https://private"} {
		t.Run(code, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				json.NewEncoder(w).Encode(map[string]any{"id": "failed-job", "status": "failed", "lease_id": "lease1", "error": map[string]any{"code": code, "message": "secret-token"}})
			}))
			defer srv.Close()
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
			c := RemotePDFReader{endpoint: srv.URL, token: "service-test"}
			result, err := c.Read(ctx, &types.ReadRequest{FileContent: []byte("%PDF-1.7 fixture"), FileType: "pdf"})
			if err != nil || result == nil || result.Error == "" || calls != 1 {
				t.Fatalf("terminal failure must not trigger whole-document retry: result=%v err=%v calls=%d", result, err, calls)
			}
			if result.Metadata["parser_error_code"] != safeRemotePDFCode(code) || strings.Contains(result.Error, "secret-token") {
				t.Fatalf("unsafe or missing cause: %v", result)
			}
		})
	}
}

func TestRemotePDFTableAgreementDoesNotApproveValuesForRetrieval(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	pdf := []byte("%PDF-1.7 review fixture")
	hash := sha256.Sum256(pdf)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/result") {
			json.NewEncoder(w).Encode(map[string]any{"id": "job1", "status": "completed", "lease_id": "lease1"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"schema_version": "remote_pdf_v1", "source": map[string]any{"sha256": hex.EncodeToString(hash[:]), "page_count": 2},
			"pages": []any{
				map[string]any{"page": 1, "text": "Duplicated unverified value 98765", "markdown": "| Amount |\n|98765|", "quality": map[string]any{"status": "passed"},
					"tables":   []any{map[string]any{"title": "Inventory", "columns": []string{"Region", "Amount"}, "rows": []any{[]string{"North", "98765"}}}},
					"formulas": []any{map[string]any{"latex": "x^2"}}},
				map[string]any{"page": 2, "text": "Ordinary prose", "markdown": "Ordinary prose", "tables": []any{}},
			},
		})
	}))
	defer srv.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	c := RemotePDFReader{endpoint: srv.URL, token: "service-test"}
	result, err := c.Read(ctx, &types.ReadRequest{FileContent: pdf, FileType: "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.MarkdownContent, "98765") || !strings.Contains(result.MarkdownContent, "Inventory") || !strings.Contains(result.MarkdownContent, "Amount") || !strings.Contains(result.MarkdownContent, "x^2") || !strings.Contains(result.MarkdownContent, "Ordinary prose") || len(result.SourceBlocks) != 2 {
		t.Fatalf("unreviewed table must remain discoverable without exposing exact values: %+v", result)
	}
	if !strings.Contains(result.MarkdownContent, "PDF 物理页 1/2") || !strings.Contains(result.MarkdownContent, "PDF 物理页 2/2") {
		t.Fatal("every page must carry its verified physical position and document page count")
	}
}
func TestRemotePDFCancellationDetachesLease(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	var detached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			detached.Store(r.Header.Get("X-Parse-Lease") == "consumer-a")
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"id":"job1","status":"queued","lease_id":"consumer-a"}`))
	}))
	defer srv.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	c := RemotePDFReader{endpoint: srv.URL, token: "token", poll: time.Millisecond}
	_, err := c.Read(ctx, &types.ReadRequest{FileContent: []byte("%PDF-1.7")})
	if err == nil || !detached.Load() {
		t.Fatalf("err=%v detached=%v", err, detached.Load())
	}
}
func TestRemotePDFAdmissionCancellation(t *testing.T) {
	release1, _ := AcquireRemotePDFBuffer(context.Background())
	defer release1()
	release2, _ := AcquireRemotePDFBuffer(context.Background())
	defer release2()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcquireRemotePDFBuffer(ctx); err == nil {
		t.Fatal("cancelled admission unexpectedly succeeded")
	}
}

func TestRemotePDFOnlyExplicitRetry(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	var retryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/retry") {
			retryCalls.Add(1)
			if r.Header.Get("X-Parse-Retry-Key") != "manual-attempt-a" {
				t.Error("manual retry identity missing")
			}
		}
		w.Write([]byte(`{"id":"job1","status":"failed","lease_id":"lease1","error":{"code":"MODEL_AUTH_FAILED"}}`))
	}))
	defer srv.Close()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(123))
	c := RemotePDFReader{endpoint: srv.URL, token: "token", poll: time.Millisecond}
	req := &types.ReadRequest{FileContent: []byte("%PDF-1.7")}
	result, err := c.Read(ctx, req)
	if err != nil || result.Error == "" || retryCalls.Load() != 0 {
		t.Fatal("ordinary call must not retry failed parse")
	}
	req.ParserRetryKey = "manual-attempt-a"
	result, err = c.Read(ctx, req)
	if err != nil || result.Error == "" || retryCalls.Load() != 1 {
		t.Fatal("explicit retry not sent once")
	}
}
