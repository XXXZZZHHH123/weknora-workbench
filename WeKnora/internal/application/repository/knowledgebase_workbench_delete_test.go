package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func knowledgeBaseDeleteContext(tenantID uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
}

func knowledgeBaseDeleteFixture(t *testing.T) (*gorm.DB, *knowledgeBaseRepository, *types.KnowledgeBase) {
	t.Helper()
	db := setupKBTestDB(t)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sql.Close()) })
	require.NoError(t, db.AutoMigrate(&types.ResearchWorkbench{}))
	kb := makeKB(nil)
	require.NoError(t, db.Create(kb).Error)
	return db, NewKnowledgeBaseRepository(db).(*knowledgeBaseRepository), kb
}

func insertDeleteWorkbench(t *testing.T, db *gorm.DB, tenantID uint64, kbID string) *types.ResearchWorkbench {
	t.Helper()
	id := uuid.NewString()
	row := &types.ResearchWorkbench{
		ID: id, TenantID: tenantID, UserID: "owner", Name: "Retained research metadata",
		KnowledgeBaseID: kbID, IdempotencyKey: id, WorkspaceMode: types.ResearchWorkbenchModeIsolated,
	}
	require.NoError(t, db.Create(row).Error)
	return row
}

func requireDeleteState(t *testing.T, db *gorm.DB, kbID string, workbenches []*types.ResearchWorkbench, deleted bool) {
	t.Helper()
	var kb types.KnowledgeBase
	require.NoError(t, db.Unscoped().First(&kb, "id = ?", kbID).Error)
	require.Equal(t, deleted, kb.DeletedAt.Valid)
	for _, original := range workbenches {
		var row types.ResearchWorkbench
		require.NoError(t, db.Unscoped().First(&row, "id = ?", original.ID).Error)
		require.Equal(t, deleted, row.DeletedAt.Valid)
		require.Equal(t, original.Name, row.Name)
		require.Equal(t, original.KnowledgeBaseID, row.KnowledgeBaseID)
	}
}

func TestKnowledgeBaseDeleteSoftDeletesEveryDirectWorkbenchAndPreservesUnrelatedRows(t *testing.T) {
	db, repo, kb := knowledgeBaseDeleteFixture(t)
	bound := []*types.ResearchWorkbench{
		insertDeleteWorkbench(t, db, 1, kb.ID),
		// A different workspace can legitimately bind a shared knowledge base.
		insertDeleteWorkbench(t, db, 2, kb.ID),
	}
	otherKB := makeKB(nil)
	otherKB.TenantID = 2
	require.NoError(t, db.Create(otherKB).Error)
	unrelated := insertDeleteWorkbench(t, db, 2, otherKB.ID)
	alreadyDeleted := insertDeleteWorkbench(t, db, 1, kb.ID)
	deletedAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, db.Model(alreadyDeleted).Update("deleted_at", deletedAt).Error)

	require.NoError(t, repo.DeleteKnowledgeBase(knowledgeBaseDeleteContext(1), kb.ID))
	requireDeleteState(t, db, kb.ID, bound, true)
	requireDeleteState(t, db, otherKB.ID, []*types.ResearchWorkbench{unrelated}, false)
	_, err := repo.GetKnowledgeBaseByID(context.Background(), kb.ID)
	require.ErrorIs(t, err, ErrKnowledgeBaseNotFound)
	var visible int64
	require.NoError(t, db.Model(&types.ResearchWorkbench{}).Where("knowledge_base_id = ?", kb.ID).Count(&visible).Error)
	require.Zero(t, visible)
	var preserved types.ResearchWorkbench
	require.NoError(t, db.Unscoped().First(&preserved, "id = ?", alreadyDeleted.ID).Error)
	require.WithinDuration(t, deletedAt, preserved.DeletedAt.Time, time.Millisecond)
}

func TestKnowledgeBaseDeleteWithoutWorkbenchStillSoftDeletes(t *testing.T) {
	db, repo, kb := knowledgeBaseDeleteFixture(t)
	require.NoError(t, repo.DeleteKnowledgeBase(knowledgeBaseDeleteContext(1), kb.ID))
	requireDeleteState(t, db, kb.ID, nil, true)
}

func TestKnowledgeBaseDeleteRejectsMissingOrDifferentExecutionTenant(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{"missing tenant", context.Background()},
		{"zero tenant", knowledgeBaseDeleteContext(0)},
		{"different tenant", knowledgeBaseDeleteContext(2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repo, kb := knowledgeBaseDeleteFixture(t)
			row := insertDeleteWorkbench(t, db, 1, kb.ID)
			require.Error(t, repo.DeleteKnowledgeBase(test.ctx, kb.ID))
			requireDeleteState(t, db, kb.ID, []*types.ResearchWorkbench{row}, false)
		})
	}
}

func TestKnowledgeBaseDeleteRollsBackBothSidesWhenEitherSoftDeleteFails(t *testing.T) {
	for _, table := range []string{"research_workbenches", "knowledge_bases"} {
		t.Run(table, func(t *testing.T) {
			db, repo, kb := knowledgeBaseDeleteFixture(t)
			bound := []*types.ResearchWorkbench{insertDeleteWorkbench(t, db, 1, kb.ID), insertDeleteWorkbench(t, db, 2, kb.ID)}
			require.NoError(t, db.Exec("CREATE TRIGGER reject_related_delete BEFORE UPDATE OF deleted_at ON "+table+" BEGIN SELECT RAISE(FAIL, 'forced related soft-delete failure'); END").Error)
			require.ErrorContains(t, repo.DeleteKnowledgeBase(knowledgeBaseDeleteContext(1), kb.ID), "forced related soft-delete failure")
			requireDeleteState(t, db, kb.ID, bound, false)
			require.NoError(t, db.Exec("DROP TRIGGER reject_related_delete").Error)
			require.NoError(t, repo.DeleteKnowledgeBase(knowledgeBaseDeleteContext(1), kb.ID))
			requireDeleteState(t, db, kb.ID, bound, true)
		})
	}
}
