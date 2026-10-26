package docparser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemotePDFPageImagesAreBoundToOriginalAndPage(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	pdf := []byte("%PDF-1.7 isolated page fixture")
	hash := sha256.Sum256(pdf)
	source := hex.EncodeToString(hash[:])
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "source", "page", "absent"} {
		t.Run(bad, func(t *testing.T) {
			submissions := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-service" || r.Header.Get("X-Parse-Scope") != "tenant:7" {
					t.Fatal("missing scoped image permission")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/image"):
					if bad == "absent" {
						w.WriteHeader(404)
						return
					}
					digest, page := source, "1"
					if bad == "source" {
						digest = strings.Repeat("b", 64)
					}
					if bad == "page" {
						page = "2"
					}
					w.Header().Set("Content-Type", "image/png")
					w.Header().Set("X-Source-SHA256", digest)
					w.Header().Set("X-PDF-Page", page)
					w.Write(imageBytes.Bytes())
				case strings.HasSuffix(r.URL.Path, "/result"):
					json.NewEncoder(w).Encode(map[string]any{"schema_version": "remote_pdf_v1", "source": map[string]any{"sha256": source, "page_count": 1}, "pages": []any{map[string]any{"page": 1, "text": "source narrative", "markdown": "source narrative"}}})
				default:
					if r.Method == http.MethodPost {
						submissions++
					}
					json.NewEncoder(w).Encode(map[string]any{"id": "job1", "status": "completed", "lease_id": "lease1"})
				}
			}))
			defer srv.Close()
			ctx := types.WithExecutionTenant(context.Background(), 7)
			reader := RemotePDFReader{endpoint: srv.URL, token: "test-service", poll: time.Millisecond, preservePageImages: true}
			result, err := reader.Read(ctx, &types.ReadRequest{FileContent: pdf, FileType: "pdf"})
			if bad != "" {
				if err == nil {
					t.Fatal("mismatched original image accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.ImageRefs) != 1 || !result.ImageRefs[0].SkipMultimodal || !strings.Contains(result.MarkdownContent, "pdf-page-0001.png") || len(result.SourceBlocks) != 1 || result.SourceBlocks[0].Locator.Page != 1 {
				t.Fatal("original image or source page lost")
			}
			if submissions != 1 {
				t.Fatal("source image caused another parse")
			}
		})
	}
}
