package repository

import (
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// Advance the grant revision with the file so a grant for the old hash cannot
// authorize a later request for the new hash.
func bumpWorkbenchSourcesForKnowledgeFile(tx *gorm.DB, before *types.Knowledge) error {
	central := tx.Model(&types.CentralDataset{}).Select("id").
		Where("backing_knowledge_base_id = ? AND tenant_id = ?", before.KnowledgeBaseID, before.TenantID)
	bindings := tx.Model(&types.ResearchWorkbenchSourceBinding{}).Select("workbench_id").
		Where("enabled = ? AND status = ? AND source_tenant_id = ?", true, "active", before.TenantID).
		Where("((source_type = ? AND source_id = ?) OR (source_type = ? AND source_id IN (?)))",
			types.WorkbenchSourceKnowledgeBase, before.KnowledgeBaseID, types.WorkbenchSourceCentralDataset, central)
	return tx.Model(&types.ResearchWorkbench{}).Where("id IN (?)", bindings).
		Update("sources_revision", gorm.Expr("sources_revision + 1")).Error
}
