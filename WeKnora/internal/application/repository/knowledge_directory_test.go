package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeDirectoriesPersistAndIsolate(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	require.NoError(t, db.Migrator().DropTable(&types.KnowledgeFolder{}))
	sql, err := os.ReadFile("../../../migrations/sqlite/000033_knowledge_directories.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(sql)).Error)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()
	created, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "A/child")
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	_, err = repo.CreateKnowledgeFolder(ctx, 2, "kb", "other-tenant")
	require.NoError(t, err)
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "other-kb", "other-library")
	require.NoError(t, err)
	counts, err := repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.NoError(t, err)
	tree := types.BuildKnowledgeFolderTree(counts)
	require.Len(t, tree.Folders, 1)
	require.Equal(t, "A", tree.Folders[0].Path)
	require.Len(t, tree.Folders[0].Children, 1)
	require.Zero(t, tree.TotalDocumentCount)
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "A")
	require.Error(t, err)
	require.Error(t, repo.DeleteKnowledgeFolder(ctx, 1, "kb", "A"))
	require.Error(t, repo.DeleteKnowledgeFolder(ctx, 2, "kb", "A/child"))
	require.NoError(t, repo.DeleteKnowledgeFolder(ctx, 1, "kb", "A/child"))
	counts, err = repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.NoError(t, err)
	tree = types.BuildKnowledgeFolderTree(counts)
	require.Len(t, tree.Folders, 1)
	require.Empty(t, tree.Folders[0].Children)
	require.NoError(t, repo.DeleteKnowledgeFolder(ctx, 1, "kb", "A"))
	counts, err = repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.NoError(t, err)
	require.Empty(t, counts)
	for _, path := range []string{"", "../escape", "a//b", strings.Repeat("a/", 17) + "b"} {
		_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", path)
		require.Error(t, err)
	}
}

func TestKnowledgeDirectoriesRenamePreservesIDsAndRollsBack(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()
	child, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "A_%/child")
	require.NoError(t, err)
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "Axx/child")
	require.NoError(t, err)
	id := insertKnowledgeInFolder(t, db, 1, "kb", "A_%/child", "file.pdf")
	require.Error(t, repo.DeleteKnowledgeFolder(ctx, 1, "kb", "A_%/child"))
	require.NoError(t, db.Exec("CREATE TRIGGER reject_folder_move BEFORE UPDATE OF folder_path ON knowledges BEGIN SELECT RAISE(ABORT,'test failure'); END").Error)
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "A_%", "Renamed")
	require.Error(t, err)
	var stored types.KnowledgeFolder
	require.NoError(t, db.First(&stored, "id = ?", child.ID).Error)
	require.Equal(t, "A_%/child", stored.Path)
	require.Equal(t, "A_%/child", folderPathOf(t, db, id))
	require.NoError(t, db.Exec("DROP TRIGGER reject_folder_move").Error)
	count, err := repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "A_%", "Renamed")
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.NoError(t, db.First(&stored, "id = ?", child.ID).Error)
	require.Equal(t, "Renamed/child", stored.Path)
	require.Equal(t, "Renamed/child", folderPathOf(t, db, id))
	var document types.Knowledge
	require.NoError(t, db.First(&document, "id = ?", id).Error)
	require.Equal(t, "file.pdf", document.FileName)
	require.Equal(t, "completed", document.ParseStatus)
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "Renamed", "Axx")
	require.Error(t, err)
	legacyID := insertKnowledgeInFolder(t, db, 1, "kb", "Legacy", "legacy.pdf")
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "Renamed", "Legacy")
	require.Error(t, err)
	require.Equal(t, "Legacy", folderPathOf(t, db, legacyID))
	require.Equal(t, "Renamed/child", folderPathOf(t, db, id))
	_, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "Renamed", "Renamed/sub")
	require.Error(t, err)
	_, err = repo.RenameKnowledgeFolderPath(ctx, 2, "kb", "Renamed", "Foreign")
	require.Error(t, err)
	require.Equal(t, "Renamed/child", folderPathOf(t, db, id))
	counts, err := repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.NoError(t, err)
	tree := types.BuildKnowledgeFolderTree(counts)
	require.Len(t, tree.Folders, 3)
	require.Equal(t, int64(2), tree.TotalDocumentCount)
}

func TestKnowledgeDirectoriesRenameDistinguishesEmptyFromMissing(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()
	_, err := repo.CreateKnowledgeFolder(ctx, 1, "kb", "empty")
	require.NoError(t, err)
	count, err := repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "empty", "renamed-empty")
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = repo.RenameKnowledgeFolderPath(ctx, 1, "kb", "empty", "missing-target")
	var missing *apperrors.AppError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, 404, missing.HTTPCode)
	require.Zero(t, count)
	counts, err := repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.NoError(t, err)
	require.Equal(t, "renamed-empty", types.BuildKnowledgeFolderTree(counts).Folders[0].Path)
}

func TestKnowledgeDirectoriesMissingSchemaFailsExplicitly(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()
	require.NoError(t, db.Migrator().DropTable(&types.KnowledgeFolder{}))
	_, err := repo.ListKnowledgeFolderCounts(ctx, 1, "kb")
	require.ErrorContains(t, err, "knowledge_folders")
	_, err = repo.CreateKnowledgeFolder(ctx, 1, "kb", "A")
	require.ErrorContains(t, err, "knowledge_folders")
}
