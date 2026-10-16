package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func sourceOnlyOverrides(t *testing.T, enabled bool) *types.KnowledgeProcessOverrides {
	t.Helper()
	var overrides types.KnowledgeProcessOverrides
	raw, err := json.Marshal(map[string]bool{"source_only": enabled})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &overrides))
	return &overrides
}

func sourceOnlyFixtureBytes(t *testing.T, extension string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	switch extension {
	case "png":
		require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	case "zip":
		archive := zip.NewWriter(&buffer)
		entry, err := archive.Create("notes/reference.txt")
		require.NoError(t, err)
		_, err = entry.Write([]byte("Isolated reference document"))
		require.NoError(t, err)
		require.NoError(t, archive.Close())
	default:
		buffer.WriteString("Isolated source bytes")
	}
	return buffer.Bytes()
}

func TestSourceOnlyUploadsStoreRawBytesWithoutAnyModelOrQueue(t *testing.T) {
	for _, extension := range []string{"png", "zip", "txt", "csv"} {
		t.Run(extension, func(t *testing.T) {
			f, queue := excelUploadFixture(t)
			original := sourceOnlyFixtureBytes(t, extension)
			row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "reference."+extension, string(original)),
				nil, nil, "references/nested/reference."+extension, nil, "", sourceOnlyOverrides(t, true))
			require.NoError(t, err)
			require.Equal(t, types.ParseStatusStored, row.ParseStatus)
			require.Equal(t, "disabled", row.EnableStatus)
			require.Equal(t, types.SummaryStatusNone, row.SummaryStatus)
			require.Equal(t, "references/nested", row.FolderPath)
			require.Empty(t, queue.types, "original-only uploads must never enqueue parsing, vectorization or summary")
			saved, err := f.repo.GetKnowledgeByID(f.ctx, 7, row.ID)
			require.NoError(t, err)
			require.Equal(t, row.FileHash, saved.FileHash)
			require.Equal(t, types.ParseStatusStored, saved.ParseStatus)
			overrides, err := saved.ProcessOverrides()
			require.NoError(t, err)
			encoded, err := json.Marshal(overrides)
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"source_only":true`)
			file, err := f.svc.fileSvc.GetFile(f.ctx, row.FilePath)
			require.NoError(t, err)
			actual, err := io.ReadAll(file)
			require.NoError(t, err)
			require.NoError(t, file.Close())
			require.Equal(t, original, actual)
			var chunks int64
			require.NoError(t, f.db.Model(&types.Chunk{}).Where("knowledge_id = ?", row.ID).Count(&chunks).Error)
			require.Zero(t, chunks)
		})
	}
}

func TestSourceOnlyFalseKeepsImageAndArchiveAdmissionGuards(t *testing.T) {
	for _, extension := range []string{"png", "zip"} {
		for _, overrides := range []*types.KnowledgeProcessOverrides{nil, sourceOnlyOverrides(t, false)} {
			f, queue := excelUploadFixture(t)
			row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "reference."+extension, string(sourceOnlyFixtureBytes(t, extension))),
				nil, nil, "", nil, "", overrides)
			require.Error(t, err)
			require.Nil(t, row)
			require.Empty(t, queue.types)
			if extension == "png" {
				require.Contains(t, err.Error(), "VLM")
			}
			f.requireNoWrites(t)
		}
	}
}

func TestSourceOnlyUploadKeepsQuotaFAQAndUnsupportedTypeGuards(t *testing.T) {
	for _, scenario := range []string{"quota", "FAQ", "unavailable storage", "unsupported executable"} {
		t.Run(scenario, func(t *testing.T) {
			f, queue := excelUploadFixture(t)
			ctx, filename := f.ctx, "reference.zip"
			switch scenario {
			case "quota":
				ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7, StorageUsed: 10, StorageQuota: 10})
			case "FAQ":
				f.kbs.values["kb"].Type = types.KnowledgeBaseTypeFAQ
			case "unavailable storage":
				f.svc.fileSvc = nil
			case "unsupported executable":
				filename = "reference.exe"
			}
			row, err := f.svc.CreateKnowledgeFromFile(ctx, "kb", newMultipartFileHeader(t, filename, "Original reference"), nil, nil, "", nil, "", sourceOnlyOverrides(t, true))
			require.Error(t, err)
			require.Nil(t, row)
			require.Empty(t, queue.types)
			f.requireNoWrites(t)
		})
	}
}

func TestSourceOnlyNativeReparseAndURLRejectBeforeProcessing(t *testing.T) {
	f, queue := excelUploadFixture(t)
	row, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "reference.png", string(sourceOnlyFixtureBytes(t, "png"))), nil, nil, "", nil, "", sourceOnlyOverrides(t, true))
	require.NoError(t, err)
	originalPath := row.FilePath
	_, err = f.svc.ReparseKnowledge(f.ctx, row.ID, nil)
	require.ErrorContains(t, err, "仅保存原件")
	require.Empty(t, queue.types)
	saved, err := f.repo.GetKnowledgeByID(f.ctx, 7, row.ID)
	require.NoError(t, err)
	require.Equal(t, types.ParseStatusStored, saved.ParseStatus)
	require.Equal(t, originalPath, saved.FilePath)
	_, err = f.svc.ReplaceKnowledgeFile(f.ctx, row.ID, newMultipartFileHeader(t, "reference.png", string(sourceOnlyFixtureBytes(t, "png"))), "", nil)
	require.ErrorContains(t, err, "仅保存原件")
	_, err = f.svc.CreateKnowledgeFromURL(f.ctx, "kb", "https://invalid.example/reference.png", "", "", nil, "", nil, "", sourceOnlyOverrides(t, true))
	require.ErrorContains(t, err, "只支持文件上传")
	_, err = f.svc.createKnowledgeFromFileURL(f.ctx, "kb", "https://invalid.example/reference.png", "", "", nil, "", nil, "", sourceOnlyOverrides(t, true))
	require.ErrorContains(t, err, "只支持文件上传")
	require.Empty(t, queue.types)
}

func TestSourceOnlyArchiveDuplicatePreservesOriginalAndQueuesNothing(t *testing.T) {
	f, queue := excelUploadFixture(t)
	original := sourceOnlyFixtureBytes(t, "zip")
	first, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "reference.zip", string(original)), nil, nil, "", nil, "", sourceOnlyOverrides(t, true))
	require.NoError(t, err)
	repeated, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "reference.zip", string(original)), nil, nil, "", nil, "", sourceOnlyOverrides(t, true))
	var duplicate *types.DuplicateKnowledgeError
	require.ErrorAs(t, err, &duplicate)
	require.Equal(t, first.ID, repeated.ID)
	require.Equal(t, first.FilePath, repeated.FilePath)
	require.Equal(t, types.ParseStatusStored, repeated.ParseStatus)
	require.Empty(t, queue.types)
}
