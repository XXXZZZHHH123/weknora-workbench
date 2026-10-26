package service

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWorkbenchScopeMetadataNeverReadsOriginalInventoryAndInternalScopeUsesTableBudget(t *testing.T) {
	s, _, _ := workspaceFixture(t)
	owner := workspaceContext("alice", 1, types.TenantRoleContributor)
	w, err := s.CreateWorkbench(owner, WorkspaceInput{Name: workspaceString("Inventory boundary"), KnowledgeBaseID: workspaceString("own"), IdempotencyKey: "inventory-boundary"})
	require.NoError(t, err)
	rows := []types.Knowledge{}
	for i := 0; i < 200; i++ {
		rows = append(rows, types.Knowledge{ID: fmt.Sprintf("private-note-%d", i), KnowledgeBaseID: "own", TenantID: 1, FileType: "docx"})
	}
	for i := 0; i < 128; i++ {
		rows = append(rows, types.Knowledge{ID: fmt.Sprintf("table-%d", i), KnowledgeBaseID: "own", TenantID: 1, FileType: "XLSX", FileHash: "private-hash"})
	}
	require.NoError(t, s.db.CreateInBatches(rows, 30).Error)
	reads := 0
	require.NoError(t, s.db.Callback().Row().Before("gorm:row").Register("inventory-budget", func(db *gorm.DB) {
		if db.Statement.Table == "knowledges" {
			reads++
		}
	}))
	t.Cleanup(func() { _ = s.db.Callback().Row().Remove("inventory-budget") })
	public, err := s.GetWorkbenchSourceScopeMetadata(owner, w.ID)
	require.NoError(t, err)
	require.False(t, public.InventoryIncluded)
	require.Zero(t, reads)
	require.Empty(t, public.Sources[0].Knowledge)
	internal, err := s.GetWorkbenchSourceScope(owner, w.ID)
	require.NoError(t, err)
	require.True(t, internal.InventoryIncluded)
	require.Len(t, internal.Sources[0].Knowledge, 128)
	require.Positive(t, reads)
	require.NoError(t, s.db.Create(&types.Knowledge{ID: "table-overflow", KnowledgeBaseID: "own", TenantID: 1, FileType: "csv"}).Error)
	_, err = s.GetWorkbenchSourceScope(owner, w.ID)
	workspaceStatus(t, err, 413)
	public, err = s.GetWorkbenchSourceScopeMetadata(owner, w.ID)
	require.NoError(t, err)
	require.Empty(t, public.Sources[0].Knowledge)
}
