package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Tencent/WeKnora/internal/ingestion"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUnifiedExcelUploadEnqueuesOneNativeTaskAndBindsOriginal(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured.internal")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "/private/isolated-test-key")
	f, queue := excelUploadFixture(t)
	row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "source.xlsx", "isolated-source"), nil, nil, "", nil, "", nil)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusPending, row.ParseStatus)
	require.Equal(t, []string{types.TypeDocumentProcess}, queue.types)
	state, err := ingestionState(row)
	require.NoError(t, err)
	require.NotEmpty(t, state.Attempt)
	require.Equal(t, row.FileHash, state.SourceHash)
	require.Equal(t, "pending", state.Status)
	saved, err := f.repo.GetKnowledgeByID(f.ctx, 7, row.ID)
	require.NoError(t, err)
	require.Equal(t, row.FilePath, saved.FilePath)
}
func TestUnifiedSourceOnlyStillNeverProcesses(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured.internal")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "/private/isolated-test-key")
	f, queue := excelUploadFixture(t)
	row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "source.xlsx", "isolated-source"), nil, nil, "", nil, "", sourceOnlyOverrides(t, true))
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusStored, row.ParseStatus)
	require.Empty(t, queue.types)
	state, err := ingestionState(row)
	require.NoError(t, err)
	require.Empty(t, state.Attempt)
}
func TestIngestionGrantCannotMoveAcrossSourceOrKnowledgeBase(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured.internal")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "/private/isolated-test-key")
	f, _ := excelUploadFixture(t)
	raw := "isolated-source"
	row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "source.xlsx", raw), nil, nil, "", nil, "", nil)
	require.NoError(t, err)
	state, err := ingestionState(row)
	require.NoError(t, err)
	sha := sha256.Sum256([]byte(raw))
	empty := sha256.Sum256([]byte(`{}`))
	claim := ingestion.Claim{Attempt: state.Attempt, TenantID: 7, KnowledgeBaseID: "kb", KnowledgeID: row.ID, UserID: "uploader", FileName: row.FileName, FileType: "xlsx", FileHash: row.FileHash, FileSize: row.FileSize, SourceSHA256: hex.EncodeToString(sha[:]), BundleSHA256: hex.EncodeToString(empty[:])}
	require.NoError(t, f.svc.ValidateIngestionSource(context.Background(), claim))
	for _, mutate := range []func(*ingestion.Claim){func(c *ingestion.Claim) { c.TenantID = 8 }, func(c *ingestion.Claim) { c.KnowledgeBaseID = "other-kb" }, func(c *ingestion.Claim) { c.KnowledgeID = "other-file" }, func(c *ingestion.Claim) { c.Attempt = "other-attempt" }, func(c *ingestion.Claim) { c.FileSize++ }, func(c *ingestion.Claim) { c.FileHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }} {
		other := claim
		mutate(&other)
		require.Error(t, f.svc.ValidateIngestionSource(context.Background(), other))
	}
	row.ParseStatus = types.ParseStatusCancelled
	require.NoError(t, f.repo.UpdateKnowledge(f.ctx, row))
	require.Error(t, f.svc.ValidateIngestionSource(context.Background(), claim))
}
