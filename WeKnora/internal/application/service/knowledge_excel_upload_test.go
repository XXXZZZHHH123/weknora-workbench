package service

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	filesvc "github.com/Tencent/WeKnora/internal/application/service/file"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

type excelUploadQueue struct {
	types []string
}

func (q *excelUploadQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.types = append(q.types, task.Type())
	return &asynq.TaskInfo{ID: "isolated-upload-task", Queue: types.QueueDefault}, nil
}

func excelUploadFixture(t *testing.T) (*documentWriteFixture, *excelUploadQueue) {
	t.Helper()
	f := newDocumentWriteFixture(t)
	q := &excelUploadQueue{}
	f.svc.fileSvc = filesvc.NewLocalFileService(t.TempDir(), "")
	f.svc.task = q
	return f, q
}

func excelUploadContent(t *testing.T) string {
	t.Helper()
	workbook := excelize.NewFile()
	t.Cleanup(func() { require.NoError(t, workbook.Close()) })
	require.NoError(t, workbook.SetCellValue("Sheet1", "A1", "isolated fixture"))
	buffer, err := workbook.WriteToBuffer()
	require.NoError(t, err)
	return buffer.String()
}

func TestExcelUploadStoresOriginalWithoutSchedulingDocumentOrSummary(t *testing.T) {
	for _, filename := range []string{"table.xlsx", "table.xls", "table.XLSX", "table.XLS"} {
		t.Run(filename, func(t *testing.T) {
			f, q := excelUploadFixture(t)
			content := excelUploadContent(t)
			knowledge, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb",
				newMultipartFileHeader(t, filename, content), map[string]string{"source": "isolated"}, nil,
				"documents/tables/"+filename, []string{"tag"}, "", nil)
			require.NoError(t, err)
			require.NotNil(t, knowledge)
			require.Empty(t, q.types, "Excel source uploads must not schedule model processing")
			require.Equal(t, "stored", knowledge.ParseStatus)
			require.Equal(t, "disabled", knowledge.EnableStatus)
			require.Equal(t, types.SummaryStatusNone, knowledge.SummaryStatus)
			require.Equal(t, "documents/tables", knowledge.FolderPath)
			require.Equal(t, filename, knowledge.FileName)
			require.Equal(t, int64(len(content)), knowledge.FileSize)
			require.Equal(t, fmt.Sprintf("%x", md5.Sum([]byte(content))), knowledge.FileHash)
			require.Len(t, knowledge.Tags, 1)
			require.Equal(t, "tag", knowledge.Tags[0].ID)
			require.Equal(t, "isolated", knowledge.GetMetadata()["source"])
			saved, err := f.repo.GetKnowledgeByID(f.ctx, 7, knowledge.ID)
			require.NoError(t, err)
			require.Equal(t, knowledge.FilePath, saved.FilePath)
			require.Equal(t, "stored", saved.ParseStatus)
			require.Equal(t, types.SummaryStatusNone, saved.SummaryStatus)
			original, err := f.svc.fileSvc.GetFile(f.ctx, saved.FilePath)
			require.NoError(t, err)
			actual, err := io.ReadAll(original)
			require.NoError(t, err)
			require.NoError(t, original.Close())
			require.Equal(t, []byte(content), actual)
			var chunks int64
			require.NoError(t, f.db.Model(&types.Chunk{}).Where("knowledge_id = ?", knowledge.ID).Count(&chunks).Error)
			require.Zero(t, chunks)
		})
	}
}

func TestExcelUploadDuplicatePreservesRealIdentityAndCancelledState(t *testing.T) {
	for _, status := range []string{"stored", types.ParseStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			f, q := excelUploadFixture(t)
			content := excelUploadContent(t)
			file := newMultipartFileHeader(t, "table.xlsx", content)
			row := &types.Knowledge{ID: "existing-excel", TenantID: 7, KnowledgeBaseID: "kb", Type: "file",
				FileType: "xlsx", FileName: "existing.xlsx", FileHash: fmt.Sprintf("%x", md5.Sum([]byte(content))),
				FilePath: "local://original.xlsx", ParseStatus: status, EnableStatus: "disabled"}
			require.NoError(t, f.db.Create(row).Error)
			knowledge, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", file, nil, nil, "", nil, "", nil)
			var duplicate *types.DuplicateKnowledgeError
			require.ErrorAs(t, err, &duplicate)
			require.Equal(t, row.ID, knowledge.ID)
			require.Equal(t, status, knowledge.ParseStatus)
			require.Equal(t, row.FilePath, knowledge.FilePath)
			require.Empty(t, q.types)
			var count int64
			require.NoError(t, f.db.Model(&types.Knowledge{}).Where("file_hash = ?", row.FileHash).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestExcelUploadDoesNotRestoreDeletedKnowledge(t *testing.T) {
	f, q := excelUploadFixture(t)
	content := excelUploadContent(t)
	row := &types.Knowledge{ID: "deleted-excel", TenantID: 7, KnowledgeBaseID: "kb", Type: "file", FileType: "xlsx",
		FileHash: fmt.Sprintf("%x", md5.Sum([]byte(content))), ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, f.db.Create(row).Error)
	require.NoError(t, f.db.Delete(row).Error)
	knowledge, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, "table.xlsx", content),
		nil, nil, "", nil, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, row.ID, knowledge.ID)
	require.Equal(t, "stored", knowledge.ParseStatus)
	require.Empty(t, q.types)
	var deleted types.Knowledge
	require.NoError(t, f.db.Unscoped().First(&deleted, "id = ?", row.ID).Error)
	require.True(t, deleted.DeletedAt.Valid)
}

func TestExcelUploadRetainsFormatStorageAndQuotaGuards(t *testing.T) {
	for _, name := range []string{"storage unavailable", "quota exceeded", "FAQ", "unsupported xlsm", "unsupported xlsb"} {
		t.Run(name, func(t *testing.T) {
			f, q := excelUploadFixture(t)
			filename, ctx := "table.xlsx", f.ctx
			switch name {
			case "storage unavailable":
				f.svc.fileSvc = nil
			case "quota exceeded":
				ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7, StorageUsed: 10, StorageQuota: 10})
			case "FAQ":
				f.kbs.values["kb"].Type = types.KnowledgeBaseTypeFAQ
			case "unsupported xlsm":
				filename = "table.xlsm"
			case "unsupported xlsb":
				filename = "table.xlsb"
			}
			knowledge, err := f.svc.CreateKnowledgeFromFile(ctx, "kb", newMultipartFileHeader(t, filename, "isolated fixture"),
				nil, nil, "", nil, "", nil)
			require.Error(t, err)
			require.Nil(t, knowledge)
			require.Empty(t, q.types)
			var count int64
			require.NoError(t, f.db.Model(&types.Knowledge{}).Count(&count).Error)
			require.EqualValues(t, 2, count)
		})
	}
}

func TestNonExcelUploadStillSchedulesExistingProcessing(t *testing.T) {
	for _, filename := range []string{"document.txt", "document.docx", "document.pptx", "table.csv"} {
		t.Run(filename, func(t *testing.T) {
			f, q := excelUploadFixture(t)
			knowledge, err := f.svc.CreateKnowledgeFromFile(f.ctx, "kb", newMultipartFileHeader(t, filename, "isolated fixture"),
				nil, nil, "", nil, "", nil)
			require.NoError(t, err)
			require.Equal(t, types.ParseStatusPending, knowledge.ParseStatus)
			want := []string{types.TypeDocumentProcess}
			if filename == "table.csv" {
				want = append(want, types.TypeDataTableSummary)
			}
			require.Equal(t, want, q.types)
		})
	}
}

func TestExcelNativeReparseRejectsBeforeDeletingExistingChunks(t *testing.T) {
	for _, fileType := range []string{"xlsx", "XLS", ""} {
		t.Run(fileType, func(t *testing.T) {
			f, q := excelUploadFixture(t)
			require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Updates(map[string]interface{}{
				"type": "file", "file_name": "table.xlsx", "file_type": fileType, "file_path": "local://original.xlsx",
				"parse_status": types.ParseStatusCancelled, "summary_status": types.SummaryStatusNone,
			}).Error)
			knowledge, err := f.svc.ReparseKnowledge(f.ctx, "doc", nil)
			var appErr *apperrors.AppError
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, 400, appErr.HTTPCode)
			require.Contains(t, appErr.Message, "结构化")
			require.Nil(t, knowledge)
			require.Empty(t, q.types)
			f.requireNoWrites(t)
			row, err := f.repo.GetKnowledgeByID(f.ctx, 7, "doc")
			require.NoError(t, err)
			require.Equal(t, types.ParseStatusCancelled, row.ParseStatus)
			require.Equal(t, "local://original.xlsx", row.FilePath)
		})
	}
}

func TestExcelNativeReparseRequiresWritePermissionBeforeTypeFeedback(t *testing.T) {
	f, q := excelUploadFixture(t)
	require.NoError(t, f.db.Model(&types.Knowledge{}).Where("id = ?", "doc").Updates(map[string]interface{}{
		"type": "file", "file_name": "table.xlsx", "file_type": "xlsx",
	}).Error)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "viewer", Role: types.TenantRoleViewer})
	ctx = (&access.KBAccess{KnowledgeBase: f.kbs.values["kb"], Caller: types.CallerFromContext(ctx),
		EffectiveTenantID: 7, Permission: types.OrgRoleViewer}).Context(ctx)
	_, err := f.svc.ReparseKnowledge(ctx, "doc", nil)
	requireForbiddenWrite(t, err)
	f.requireNoWrites(t)
	require.Empty(t, q.types)
}

func TestExcelNativeReplacementRejectsBeforeSavingOrChangingOriginal(t *testing.T) {
	for _, filename := range []string{"table.xlsx", "table.XLS"} {
		t.Run(filename, func(t *testing.T) {
			h := newReplaceFileHarness(t)
			knowledge, err := h.replaceNamed(t, "isolated replacement", filename, "", map[string]string{"source": "new"})
			var appErr *apperrors.AppError
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, 400, appErr.HTTPCode)
			require.Contains(t, appErr.Message, "结构化")
			require.Nil(t, knowledge)
			require.Equal(t, h.original, h.repo.row)
			require.Empty(t, h.events)
			require.Empty(t, h.tasks.payloads)
		})
	}
}
