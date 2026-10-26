package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Tencent/WeKnora/internal/ingestion"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type ingestionValidator struct {
	interfaces.KnowledgeService
	valid bool
	calls int
}

func (v *ingestionValidator) ValidateIngestionSource(context.Context, ingestion.Claim) error {
	v.calls++
	if !v.valid {
		return os.ErrPermission
	}
	return nil
}
func TestIngestionModelRequiresSignedBodyAndRechecksSource(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	path := filepath.Join(t.TempDir(), "service-key")
	require.NoError(t, os.WriteFile(path, key, 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", path)
	claim := ingestion.Claim{Attempt: "attempt-a", TenantID: 7, KnowledgeBaseID: "kb", KnowledgeID: "doc", FileName: "source.xlsx", FileType: "xlsx", FileHash: strings.Repeat("a", 32), SourceSHA256: strings.Repeat("b", 64), BundleSHA256: strings.Repeat("c", 64), FileSize: 32}
	for _, kind := range []string{"valid", "unsigned", "tampered", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := newStructuredModelFixture(t)
			validator := &ingestionValidator{valid: true}
			f.router.POST(ingestion.ModelPath, IngestionModelChat(&KnowledgeHandler{kgService: validator, kbService: f.knowledge}, NewModelHandler(f.models)))
			req := httptest.NewRequest(http.MethodPost, ingestion.ModelPath, strings.NewReader(structuredModelBody))
			req.Header.Set("Content-Type", "application/json")
			bodyHash := sha256.Sum256([]byte(structuredModelBody))
			req.Header.Set("X-Ingestion-Body-SHA256", hex.EncodeToString(bodyHash[:]))
			if kind != "unsigned" {
				require.NoError(t, ingestion.Sign(req, claim, key, time.Now()))
			}
			if kind == "tampered" {
				req.Body = http.NoBody
			}
			if kind == "revoked" {
				f.models.client.after = func() { validator.valid = false }
			}
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			if kind == "valid" {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Equal(t, 2, validator.calls)
				require.Equal(t, 1, f.models.client.calls)
			} else {
				require.NotEqual(t, 200, w.Code, w.Body.String())
				if kind != "revoked" {
					require.Zero(t, f.models.client.calls)
				}
			}
			require.NotContains(t, w.Body.String(), "fixture-provider-secret")
		})
	}
}
