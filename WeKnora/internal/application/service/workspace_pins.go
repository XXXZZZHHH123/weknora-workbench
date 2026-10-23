package service

import (
	"context"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm/clause"
)

type WorkbenchPinState struct {
	ID       string     `json:"id"`
	IsPinned bool       `json:"is_pinned"`
	PinnedAt *time.Time `json:"pinned_at"`
}

func (s *WorkspaceService) SetWorkbenchPin(ctx context.Context, id string, pinned bool) (*WorkbenchPinState, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = s.workbench(ctx, s.db, id, false); err != nil {
		return nil, err
	}
	state := &WorkbenchPinState{ID: id, IsPinned: pinned}
	query := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND workbench_id = ?", caller.TenantID, caller.UserID, id)
	if !pinned {
		if err = query.Delete(&types.UserWorkbenchPin{}).Error; err != nil {
			return nil, err
		}
		return state, nil
	}
	row := types.UserWorkbenchPin{TenantID: caller.TenantID, UserID: caller.UserID, WorkbenchID: id, PinnedAt: time.Now().UTC()}
	if err = s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err = query.First(&row).Error; err != nil {
		return nil, err
	}
	state.PinnedAt = &row.PinnedAt
	return state, nil
}

func (s *WorkspaceService) applyWorkbenchPins(ctx context.Context, caller types.Caller, rows []*types.ResearchWorkbench) error {
	if len(rows) == 0 {
		return nil
	}
	var pins []types.UserWorkbenchPin
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", caller.TenantID, caller.UserID).Find(&pins).Error; err != nil {
		return err
	}
	byID := make(map[string]time.Time, len(pins))
	for _, pin := range pins {
		byID[pin.WorkbenchID] = pin.PinnedAt
	}
	for _, row := range rows {
		row.IsPinned = false
		row.PinnedAt = nil
		if at, ok := byID[row.ID]; ok {
			row.IsPinned = true
			row.PinnedAt = &at
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].IsPinned && !rows[j].IsPinned })
	return nil
}
