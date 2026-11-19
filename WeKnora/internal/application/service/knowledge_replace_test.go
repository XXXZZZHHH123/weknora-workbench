package service

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replaceFileRepo is a single-row knowledge store. UpdateKnowledgeColumns can
// fail on a chosen call, either before applying the write or after it (a write
// that committed but reported an error).
type replaceFileRepo struct {
	interfaces.KnowledgeRepository
	row             types.Knowledge
	columnsCalls    int
	failColumnsCall int
	commitThenFail  bool
}

func (r *replaceFileRepo) GetKnowledgeByID(context.Context, uint64, string) (*types.Knowledge, error) {
	row := r.row
	return &row, nil
}

func (r *replaceFileRepo) UpdateKnowledge(_ context.Context, knowledge *types.Knowledge) error {
	r.row = *knowledge
	return nil
}

func (r *replaceFileRepo) UpdateKnowledgeColumn(_ context.Context, _ string, column string, value interface{}) error {
	r.apply(map[string]interface{}{column: value})
	return nil
}

func (r *replaceFileRepo) UpdateKnowledgeColumns(_ context.Context, _ string, values map[string]interface{}) error {
	r.columnsCalls++
	if r.columnsCalls != r.failColumnsCall {
		r.apply(values)
		return nil
	}
	if r.commitThenFail {
		r.apply(values)
	}
	return errors.New("database unavailable")
}

func (r *replaceFileRepo) UpdateKnowledgeFileSource(ctx context.Context, before *types.Knowledge, values map[string]interface{}) error {
	if before.FilePath != r.row.FilePath {
		return errors.New("source snapshot changed")
	}
	return r.UpdateKnowledgeColumns(ctx, before.ID, values)
}

func (r *replaceFileRepo) apply(values map[string]interface{}) {
	for column, value := range values {
		switch column {
		case "title":
			r.row.Title = value.(string)
		case "file_name":
			r.row.FileName = value.(string)
		case "folder_path":
			r.row.FolderPath = value.(string)
		case "file_type":
			r.row.FileType = value.(string)
		case "file_size":
			r.row.FileSize = value.(int64)
		case "file_hash":
			r.row.FileHash = value.(string)
		case "file_path":
			r.row.FilePath = value.(string)
		case "metadata":
			r.row.Metadata = value.(types.JSON)
		case "parse_status":
			r.row.ParseStatus = value.(string)
		case "enable_status":
			r.row.EnableStatus = value.(string)
		case "error_message":
			r.row.ErrorMessage = value.(string)
		case "updated_at":
			r.row.UpdatedAt = value.(time.Time)
		}
	}
}

type replaceFileStore struct {
	interfaces.FileService
	saveErr error
	saved   int
	deleted []string
	events  *[]string
}

func (f *replaceFileStore) SaveFile(context.Context, *multipart.FileHeader, uint64, string) (string, error) {
	if f.saveErr != nil {
		return "", f.saveErr
	}
	f.saved++
	*f.events = append(*f.events, "save")
	if f.saved > 1 {
		return fmt.Sprintf("new/file-%d.md", f.saved), nil
	}
	return "new/file.md", nil
}

func (f *replaceFileStore) DeleteFile(_ context.Context, filePath string) error {
	f.deleted = append(f.deleted, filePath)
	*f.events = append(*f.events, "delete:"+filePath)
	return nil
}

type replaceFileEnqueuer struct {
	err      error
	payloads []types.DocumentProcessPayload
	events   *[]string
}

func (e *replaceFileEnqueuer) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	var payload types.DocumentProcessPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return nil, err
	}
	e.payloads = append(e.payloads, payload)
	*e.events = append(*e.events, "enqueue")
	return &asynq.TaskInfo{ID: "task-1", Queue: types.QueueDefault}, nil
}

type replaceFileChunks struct{ interfaces.ChunkRepository }

func (replaceFileChunks) ListImageInfoByKnowledgeIDs(
	context.Context, uint64, []string,
) ([]interfaces.ChunkImageInfo, error) {
	return nil, nil
}

func (replaceFileChunks) DeleteChunksByKnowledgeID(context.Context, uint64, string) error { return nil }

type replaceFileChunkService struct{ interfaces.ChunkService }

func (replaceFileChunkService) GetRepository() interfaces.ChunkRepository { return replaceFileChunks{} }

type replaceFileGraph struct {
	interfaces.RetrieveGraphRepository
}

func (replaceFileGraph) DelGraph(context.Context, []types.NameSpace) error { return nil }

type replaceFileInspector struct {
	fakeTaskInspector
	events *[]string
}

func (i *replaceFileInspector) CancelTasksForKnowledge(_ context.Context, knowledgeID string) (int, int, error) {
	*i.events = append(*i.events, "dequeue:"+knowledgeID)
	return 1, 0, nil
}

type replaceFileHarness struct {
	svc      *knowledgeService
	repo     *replaceFileRepo
	store    *replaceFileStore
	tasks    *replaceFileEnqueuer
	events   []string
	original types.Knowledge
	ctx      context.Context
}

const replaceFileOldContent = "old"

func newReplaceFileHarness(t *testing.T) *replaceFileHarness {
	t.Helper()
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7}
	h := &replaceFileHarness{}
	h.original = types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        7,
		KnowledgeBaseID: "kb-1",
		Type:            "file",
		Title:           "a.md",
		FileName:        "a.md",
		FolderPath:      "notes",
		FileType:        "md",
		FileSize:        int64(len(replaceFileOldContent)),
		FileHash:        md5Hex(replaceFileOldContent),
		FilePath:        "old/file.md",
		ParseStatus:     types.ParseStatusCompleted,
		EnableStatus:    "enabled",
		Metadata:        types.JSON(`{"external_id":"notes/a.md","extra":{"nested":true}}`),
		CreatedAt:       time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC),
	}
	h.repo = &replaceFileRepo{row: h.original}
	h.store = &replaceFileStore{events: &h.events}
	h.tasks = &replaceFileEnqueuer{events: &h.events}
	h.svc = &knowledgeService{
		repo:          h.repo,
		kbService:     &reparseFailureKBService{kb: kb},
		fileSvc:       h.store,
		task:          h.tasks,
		taskInspector: &replaceFileInspector{events: &h.events},
		chunkService:  replaceFileChunkService{},
		chunkRepo:     replaceFileChunks{},
		graphEngine:   replaceFileGraph{},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7})
	ctx, err := access.WithKBTaskWrite(ctx, kb, 7)
	require.NoError(t, err)
	h.ctx = ctx
	return h
}

func (h *replaceFileHarness) replace(
	t *testing.T, content, customFileName string, metadata map[string]string,
) (*types.Knowledge, error) {
	t.Helper()
	return h.replaceNamed(t, content, "upload.md", customFileName, metadata)
}

func (h *replaceFileHarness) replaceNamed(
	t *testing.T, content, filename, customFileName string, metadata map[string]string,
) (*types.Knowledge, error) {
	t.Helper()
	fh, err := bytesToFileHeader([]byte(content), filename)
	require.NoError(t, err)
	return h.svc.ReplaceKnowledgeFile(h.ctx, h.original.ID, fh, customFileName, metadata)
}

func md5Hex(content string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(content)))
}

func TestReplaceKnowledgeFilePreservesIDAndReparsesNewContent(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.ctx = context.WithValue(h.ctx, types.UserIDContextKey, "researcher")
	audit := &fileReplacementAudit{}
	h.svc.audit = audit
	zero := 0
	require.NoError(t, setContentProcessingMetadata(&h.repo.row, 8, &zero, false))
	require.NoError(t, setIngestionState(&h.repo.row, knowledgeIngestionState{SourceHash: h.original.FileHash, Result: []byte(`{"accepted_count":1}`)}))
	content := "# new body"

	got, err := h.replace(t, content, "notes/sub/b.md",
		map[string]string{"source_updated_at": "2026-09-14T00:00:00Z"})

	require.NoError(t, err)
	require.Equal(t, h.original.ID, got.ID)
	row := h.repo.row
	assert.Equal(t, "new/file.md", row.FilePath)
	assert.Equal(t, md5Hex(content), row.FileHash)
	assert.Equal(t, int64(len(content)), row.FileSize)
	assert.Equal(t, "b.md", row.FileName)
	assert.Equal(t, "b.md", row.Title, "a title that mirrored the file name follows the rename")
	assert.Equal(t, "notes/sub", row.FolderPath)
	assert.Equal(t, types.ParseStatusPending, row.ParseStatus)

	metadata, err := row.Metadata.Map()
	require.NoError(t, err)
	assert.Equal(t, "notes/a.md", metadata["external_id"])
	assert.Equal(t, "2026-09-14T00:00:00Z", metadata["source_updated_at"])
	assert.Equal(t, map[string]interface{}{"nested": true}, metadata["extra"], "unmanaged entries survive")
	assert.NotContains(t, metadata, contentProcessingKey)
	assert.NotContains(t, metadata, ingestionMetadataKey, "old original table counts cannot survive replacement")
	history, err := row.FileVersionHistory()
	require.NoError(t, err)
	require.Len(t, history.Previous, 1)
	assert.Equal(t, h.original.FileHash, history.Previous[0].FileHash)
	assert.Equal(t, h.original.FileSize, history.Previous[0].FileSize)
	assert.Equal(t, h.original.CreatedAt, history.Previous[0].UploadedAt)
	assert.Equal(t, "researcher", history.Previous[0].ReplacedBy)
	assert.Equal(t, history.UpdatedAt, history.Previous[0].ReplacedAt)
	assert.True(t, history.UpdatedAt.After(h.original.CreatedAt))
	require.NotNil(t, audit.updated)
	assert.Equal(t, "researcher", audit.updated.ActorUserID)
	details, err := audit.updated.Details.Map()
	require.NoError(t, err)
	assert.Equal(t, h.original.FileHash, details["previous_file_hash"])
	assert.Equal(t, row.FileHash, details["file_hash"])

	require.Len(t, h.tasks.payloads, 1)
	assert.Equal(t, h.original.ID, h.tasks.payloads[0].KnowledgeID)
	assert.Equal(t, "new/file.md", h.tasks.payloads[0].FilePath)
	assert.Equal(t, []string{"save", "dequeue:knowledge-1", "enqueue", "delete:old/file.md"}, h.events,
		"queued parse tasks are dropped before reparse; the old file is deleted only after enqueue")
}

type fileReplacementAudit struct {
	interfaces.AuditLogService
	updated *types.AuditLog
}

func (s *fileReplacementAudit) Log(_ context.Context, entry *types.AuditLog) error {
	if entry.Action == types.AuditActionKnowledgeUpdated {
		s.updated = entry
	}
	return nil
}

func TestReplaceKnowledgeFileHistoryKeepsRecentVersions(t *testing.T) {
	h := newReplaceFileHarness(t)
	for i := 1; i <= knowledgeFilePreviousVersionsRetained+1; i++ {
		_, err := h.replace(t, fmt.Sprintf("version %d", i), "notes/a.md", nil)
		require.NoError(t, err)
	}
	history, err := h.repo.row.FileVersionHistory()
	require.NoError(t, err)
	require.Len(t, history.Previous, knowledgeFilePreviousVersionsRetained)
	assert.Equal(t, md5Hex(fmt.Sprintf("version %d", knowledgeFilePreviousVersionsRetained)), history.Previous[0].FileHash)
	assert.Equal(t, md5Hex("version 1"), history.Previous[len(history.Previous)-1].FileHash)
}

func TestReplaceKnowledgeFileRetainsLastPublishedHashAcrossFailedWorkbookUpdates(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured.internal")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "unused-by-enqueue")
	h := newReplaceFileHarness(t)
	h.repo.row.FileName, h.repo.row.FileType = "workbook.xlsx", "xlsx"
	first, err := h.replaceNamed(t, "published workbook", "workbook.xlsx", "", nil)
	require.NoError(t, err)
	state, err := ingestionState(first)
	require.NoError(t, err)
	state.Status, state.Result = "completed", json.RawMessage(`{"dataset_id":"published"}`)
	require.NoError(t, h.svc.saveIngestionState(h.ctx, first, state))
	for i := 0; i <= knowledgeFilePreviousVersionsRetained; i++ {
		_, err := h.replaceNamed(t, fmt.Sprintf("unpublished workbook %d", i), "workbook.xlsx", "", nil)
		require.NoError(t, err)
	}
	history, err := h.repo.row.FileVersionHistory()
	require.NoError(t, err)
	require.Equal(t, first.FileHash, history.LastPublishedFileHash)
	for _, version := range history.Previous {
		require.NotEqual(t, first.FileHash, version.FileHash)
	}
}

func TestReplaceKnowledgeFileRejectsDifferentSupportedType(t *testing.T) {
	h := newReplaceFileHarness(t)
	_, err := h.replaceNamed(t, "text", "replacement.txt", "", nil)
	require.ErrorContains(t, err, "相同的文件类型")
	assert.Zero(t, h.store.saved)
	assert.Empty(t, h.tasks.payloads)
}

func TestReplaceKnowledgeFileQueuesNativeWorkbookIngestion(t *testing.T) {
	t.Setenv("STRUCTURED_DATA_URL", "http://structured.internal")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "unused-by-enqueue")
	for _, mode := range []string{"legacy upload", "registered structured original"} {
		t.Run(mode, func(t *testing.T) {
			h := newReplaceFileHarness(t)
			h.repo.row.FileName = "workbook.xlsx"
			h.repo.row.FileType = "xlsx"
			h.repo.row.ParseStatus = types.ParseStatusStored
			sourceOnly := mode == "registered structured original"
			if sourceOnly {
				require.NoError(t, h.repo.row.SetProcessOverrides(&types.KnowledgeProcessOverrides{SourceOnly: true}))
			}
			updated, err := h.replaceNamed(t, "new workbook", "workbook.XLSX", "", nil)
			require.NoError(t, err)
			assert.Equal(t, h.original.ID, updated.ID)
			assert.Equal(t, "xlsx", updated.FileType)
			state, err := ingestionState(updated)
			require.NoError(t, err)
			assert.NotEmpty(t, state.Attempt)
			assert.Equal(t, updated.FileHash, state.SourceHash)
			overrides, err := updated.ProcessOverrides()
			require.NoError(t, err)
			assert.Equal(t, sourceOnly, overrides != nil && overrides.SourceOnly)
			require.Len(t, h.tasks.payloads, 1)
			assert.Equal(t, updated.ID, h.tasks.payloads[0].KnowledgeID)
			assert.Equal(t, updated.FilePath, h.tasks.payloads[0].FilePath)
		})
	}
}

func TestReplaceKnowledgeFileUnchangedContentSkipsReparse(t *testing.T) {
	h := newReplaceFileHarness(t)

	got, err := h.replace(t, replaceFileOldContent, "notes/a.md", map[string]string{"source_updated_at": "later"})

	var dupErr *types.DuplicateKnowledgeError
	require.ErrorAs(t, err, &dupErr)
	require.Equal(t, h.original.ID, got.ID)
	assert.Equal(t, h.original.ID, dupErr.Knowledge.ID)
	assert.Zero(t, h.store.saved)
	assert.Empty(t, h.tasks.payloads)
	assert.Empty(t, h.store.deleted)
	assert.Equal(t, h.original.FilePath, h.repo.row.FilePath)
	assert.Equal(t, "later", h.repo.row.GetMetadata()["source_updated_at"], "changed metadata is still persisted")
}

func TestReplaceKnowledgeFileSaveFailureLeavesKnowledgeUntouched(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.store.saveErr = errors.New("storage unavailable")

	_, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.ErrorIs(t, err, h.store.saveErr)
	assert.Equal(t, h.original, h.repo.row)
	assert.Empty(t, h.store.deleted)
	assert.Empty(t, h.tasks.payloads)
}

func TestReplaceKnowledgeFileSourceUpdateFailureDiscardsNewFile(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.repo.failColumnsCall = 1

	_, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.Error(t, err)
	assert.Equal(t, h.original, h.repo.row)
	assert.Equal(t, []string{"new/file.md"}, h.store.deleted, "the old file must never be deleted")
	assert.Empty(t, h.tasks.payloads)
}

func TestReplaceKnowledgeFileCommittedUpdateReportedAsFailedContinuesReparse(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.repo.failColumnsCall = 1
	h.repo.commitThenFail = true

	got, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.NoError(t, err)
	require.Equal(t, h.original.ID, got.ID)
	assert.Equal(t, "new/file.md", h.repo.row.FilePath)
	assert.Equal(t, types.ParseStatusPending, h.repo.row.ParseStatus)
	assert.Equal(t, []string{"old/file.md"}, h.store.deleted)
	require.Len(t, h.tasks.payloads, 1)
}

func TestReplaceKnowledgeFileReparseFailureRestoresPreviousSource(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.tasks.err = errors.New("queue unavailable")

	_, err := h.replace(t, "# new body", "notes/sub/b.md", map[string]string{"source_updated_at": "later"})

	require.Error(t, err)
	row := h.repo.row
	assert.Equal(t, h.original.ID, row.ID)
	assert.Equal(t, h.original.FilePath, row.FilePath)
	assert.Equal(t, h.original.FileHash, row.FileHash)
	assert.Equal(t, h.original.FileSize, row.FileSize)
	assert.Equal(t, h.original.FileName, row.FileName)
	assert.Equal(t, h.original.Title, row.Title)
	assert.Equal(t, h.original.FolderPath, row.FolderPath)
	assert.JSONEq(t, string(h.original.Metadata), string(row.Metadata))
	assert.Equal(t, types.ParseStatusFailed, row.ParseStatus, "the old index may already be cleaned up")
	assert.Equal(t, []string{"new/file.md"}, h.store.deleted)
}

func TestReplaceKnowledgeFileRestoreFailureKeepsBothFiles(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.tasks.err = errors.New("queue unavailable")
	h.repo.failColumnsCall = 2 // the restore write

	_, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.Error(t, err)
	assert.Equal(t, "new/file.md", h.repo.row.FilePath)
	assert.Empty(t, h.store.deleted)
}

func TestReplaceKnowledgeFileRejectsNonFileKnowledge(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.repo.row.Type = types.KnowledgeTypeManual

	_, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.Error(t, err)
	assert.Zero(t, h.store.saved)
}

func TestReplaceKnowledgeFileRejectsUnsupportedFileType(t *testing.T) {
	h := newReplaceFileHarness(t)

	_, err := h.replace(t, "MZ", "notes/tool.exe", nil)

	require.Error(t, err)
	assert.Zero(t, h.store.saved)
	assert.Equal(t, h.original, h.repo.row)
}

func TestReplaceKnowledgeFileEmptyCustomNameKeepsFolder(t *testing.T) {
	h := newReplaceFileHarness(t)

	_, err := h.replaceNamed(t, "# new body", "a.md", "", nil)

	require.NoError(t, err)
	assert.Equal(t, "notes", h.repo.row.FolderPath)
	assert.Equal(t, "a.md", h.repo.row.FileName)
}

func TestReplaceKnowledgeFileBareCustomNameKeepsFolder(t *testing.T) {
	h := newReplaceFileHarness(t)

	_, err := h.replace(t, "# new body", "b.md", nil)

	require.NoError(t, err)
	assert.Equal(t, "notes", h.repo.row.FolderPath, "a basename must not move the document to the KB root")
	assert.Equal(t, "b.md", h.repo.row.FileName)
}

func TestReplaceKnowledgeFileIgnoresStorageQuota(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.ctx = context.WithValue(h.ctx, types.TenantInfoContextKey, &types.Tenant{
		ID: 7, StorageQuota: 1, StorageUsed: 1,
	})

	got, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.NoError(t, err)
	require.Equal(t, h.original.ID, got.ID)
	assert.Equal(t, types.ParseStatusPending, h.repo.row.ParseStatus)
}

func TestReplaceKnowledgeFileDequeuesInProgressParse(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.repo.row.ParseStatus = types.ParseStatusProcessing

	got, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.NoError(t, err)
	require.Equal(t, h.original.ID, got.ID)
	assert.Contains(t, h.events, "dequeue:knowledge-1")
	assert.Equal(t, types.ParseStatusPending, h.repo.row.ParseStatus)
	assert.Equal(t, "disabled", h.repo.row.EnableStatus)
}

func TestReplaceKnowledgeFileRejectsFAQKnowledgeBase(t *testing.T) {
	h := newReplaceFileHarness(t)
	h.svc.kbService = &reparseFailureKBService{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 7, Type: types.KnowledgeBaseTypeFAQ,
	}}

	_, err := h.replace(t, "# new body", "notes/a.md", nil)

	require.Error(t, err)
	assert.Zero(t, h.store.saved)
}

func TestIsKnowledgeSourceReplaced(t *testing.T) {
	h := newReplaceFileHarness(t)
	loaded := h.original
	assert.False(t, h.svc.isKnowledgeSourceReplaced(h.ctx, &loaded))

	h.repo.row.FilePath = "new/file.md"
	assert.True(t, h.svc.isKnowledgeSourceReplaced(h.ctx, &loaded))
}

func TestUpdateKnowledgeUnlessSourceReplacedSkipsStaleSave(t *testing.T) {
	h := newReplaceFileHarness(t)
	stale := h.original
	stale.ParseStatus = types.ParseStatusFailed
	h.repo.row.FilePath = "new/file.md"

	require.NoError(t, h.svc.updateKnowledgeUnlessSourceReplaced(h.ctx, &stale))
	assert.Equal(t, "new/file.md", h.repo.row.FilePath)
	assert.NotEqual(t, types.ParseStatusFailed, h.repo.row.ParseStatus)
}
