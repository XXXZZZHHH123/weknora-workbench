package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkspaceService struct {
	db     *gorm.DB
	kbs    interfaces.KnowledgeBaseService
	shares interfaces.KBShareService
}

func NewWorkspaceService(db *gorm.DB, kbs interfaces.KnowledgeBaseService, shares interfaces.KBShareService) *WorkspaceService {
	return &WorkspaceService{db: db, kbs: kbs, shares: shares}
}

type WorkspaceInput struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	KnowledgeBaseID *string `json:"knowledge_base_id"`
	IdempotencyKey  string  `json:"idempotency_key"`
	TemplateID      *string `json:"template_id"`
	TemplateVersion *int    `json:"template_version"`
}

func workspaceCaller(ctx context.Context) (types.Caller, error) {
	caller := types.CallerFromContext(ctx).Normalize()
	if caller.TenantID == 0 || caller.UserID == "" {
		return caller, apperrors.NewUnauthorizedError("请先登录并选择空间")
	}
	return caller, nil
}

func validateWorkspaceInput(input WorkspaceInput, create bool) error {
	if create && input.Name == nil {
		return apperrors.NewBadRequestError("请输入名称")
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || utf8.RuneCountInString(*input.Name) > 128 || strings.IndexFunc(*input.Name, unicode.IsControl) >= 0 {
			return apperrors.NewBadRequestError("名称须为1至128个字符，不能包含控制字符")
		}
	}
	if input.Description != nil && utf8.RuneCountInString(*input.Description) > 2000 {
		return apperrors.NewBadRequestError("说明不能超过2000个字符")
	}
	if input.KnowledgeBaseID != nil && len(*input.KnowledgeBaseID) > 36 {
		return apperrors.NewBadRequestError("知识库ID无效")
	}
	return nil
}

func (s *WorkspaceService) readableKB(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, apperrors.NewBadRequestError("请选择知识库")
	}
	kb, err := s.kbs.GetKnowledgeBaseByIDOnly(ctx, id)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if err != nil || kb == nil || kb.DeletedAt.Valid {
		return nil, apperrors.NewNotFoundError("知识库不存在或无权访问")
	}
	_, err = access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, s.shares, nil)
	if err != nil {
		return nil, apperrors.NewNotFoundError("知识库不存在或无权访问")
	}
	return kb, nil
}

func projectQuery(db *gorm.DB, caller types.Caller) *gorm.DB {
	return db.Model(&types.AssistantProject{}).Where("tenant_id = ? AND user_id = ?", caller.TenantID, caller.UserID)
}

func (s *WorkspaceService) project(ctx context.Context, db *gorm.DB, id string, lock bool) (*types.AssistantProject, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	q := projectQuery(db.WithContext(ctx), caller)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row types.AssistantProject
	if err := q.First(&row, "id = ?", id).Error; err != nil {
		return nil, workspaceDBError(err)
	}
	return &row, nil
}

func workspaceDBError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperrors.NewNotFoundError("记录不存在或无权访问")
	}
	return err
}

func (s *WorkspaceService) ListProjects(ctx context.Context) ([]*types.AssistantProject, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]*types.AssistantProject, 0)
	if err := projectQuery(s.db.WithContext(ctx), caller).Order("updated_at DESC, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := s.sessionQuery(ctx, caller, row.ID).Count(&row.SessionCount).Error; err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (s *WorkspaceService) GetProject(ctx context.Context, id string) (*types.AssistantProject, error) {
	row, err := s.project(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	caller, _ := workspaceCaller(ctx)
	err = s.sessionQuery(ctx, caller, id).Count(&row.SessionCount).Error
	return row, err
}

func (s *WorkspaceService) CreateProject(ctx context.Context, input WorkspaceInput) (*types.AssistantProject, error) {
	if input.TemplateID != nil || input.TemplateVersion != nil {
		return nil, apperrors.NewBadRequestError("项目不接受研究模板参数")
	}
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateWorkspaceInput(input, true); err != nil {
		return nil, err
	}
	row := &types.AssistantProject{ID: uuid.NewString(), TenantID: caller.TenantID, UserID: caller.UserID, Name: *input.Name}
	if input.Description != nil {
		row.Description = *input.Description
	}
	if input.KnowledgeBaseID != nil && *input.KnowledgeBaseID != "" {
		if _, err = s.readableKB(ctx, *input.KnowledgeBaseID); err != nil {
			return nil, err
		}
		row.KnowledgeBaseID = *input.KnowledgeBaseID
	}
	return row, s.db.WithContext(ctx).Create(row).Error
}

func (s *WorkspaceService) UpdateProject(ctx context.Context, id string, input WorkspaceInput) (*types.AssistantProject, error) {
	if input.TemplateID != nil || input.TemplateVersion != nil {
		return nil, apperrors.NewBadRequestError("项目不接受研究模板参数")
	}
	if err := validateWorkspaceInput(input, false); err != nil {
		return nil, err
	}
	if input.KnowledgeBaseID != nil && *input.KnowledgeBaseID != "" {
		if _, err := s.readableKB(ctx, *input.KnowledgeBaseID); err != nil {
			return nil, err
		}
	}
	var row *types.AssistantProject
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		row, err = s.project(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if input.Name != nil {
			row.Name = *input.Name
		}
		if input.Description != nil {
			row.Description = *input.Description
		}
		if input.KnowledgeBaseID != nil {
			row.KnowledgeBaseID = *input.KnowledgeBaseID
		}
		return tx.Save(row).Error
	})
	return row, err
}

func (s *WorkspaceService) DeleteProject(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.project(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err = tx.Where("project_id = ? AND tenant_id = ? AND user_id = ?", id, row.TenantID, row.UserID).Delete(&types.AssistantProjectSession{}).Error; err != nil {
			return err
		}
		return tx.Delete(row).Error
	})
}

func (s *WorkspaceService) sessionQuery(ctx context.Context, caller types.Caller, id string) *gorm.DB {
	return s.db.WithContext(ctx).Model(&types.Session{}).Joins("JOIN assistant_project_sessions aps ON aps.session_id = sessions.id AND aps.tenant_id = sessions.tenant_id AND aps.user_id = sessions.user_id").Where("aps.project_id = ? AND sessions.tenant_id = ? AND sessions.user_id = ?", id, caller.TenantID, caller.UserID).Where("EXISTS (SELECT 1 FROM messages history_message WHERE history_message.session_id = sessions.id AND history_message.role = ? AND history_message.deleted_at IS NULL)", "user")
}

func (s *WorkspaceService) ProjectSessions(ctx context.Context, id string, page, size int) ([]*types.Session, int64, error) {
	if page < 1 || page > 100000 || size < 1 || size > 100 {
		return nil, 0, apperrors.NewBadRequestError("分页参数无效")
	}
	if _, err := s.project(ctx, s.db, id, false); err != nil {
		return nil, 0, err
	}
	caller, _ := workspaceCaller(ctx)
	var count int64
	rows := make([]*types.Session, 0)
	q := s.sessionQuery(ctx, caller, id)
	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	err := q.Select("sessions.*").Order("sessions.updated_at DESC, sessions.id").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, count, err
}

func (s *WorkspaceService) AssignSession(ctx context.Context, projectID, sessionID string, remove bool) error {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.project(ctx, tx, projectID, true); err != nil {
			return err
		}
		var session types.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND user_id = ?", sessionID, caller.TenantID, caller.UserID).First(&session).Error; err != nil {
			return workspaceDBError(err)
		}
		if remove {
			return tx.Where("tenant_id = ? AND user_id = ? AND project_id = ? AND session_id = ?", caller.TenantID, caller.UserID, projectID, sessionID).Delete(&types.AssistantProjectSession{}).Error
		}
		row := types.AssistantProjectSession{TenantID: caller.TenantID, UserID: caller.UserID, ProjectID: projectID, SessionID: sessionID}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "session_id"}}, DoUpdates: clause.AssignmentColumns([]string{"project_id"})}).Create(&row).Error
	})
}

func (s *WorkspaceService) ListWorkbenches(ctx context.Context, kbID string) ([]*types.ResearchWorkbench, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if kbID != "" {
		if _, err := s.readableKB(ctx, kbID); err != nil {
			return nil, err
		}
	}
	rows := make([]*types.ResearchWorkbench, 0)
	visible := make([]*types.ResearchWorkbench, 0)
	q := s.db.WithContext(ctx).Where("tenant_id = ?", caller.TenantID)
	if kbID != "" {
		q = q.Where("knowledge_base_id = ?", kbID)
	}
	if err := q.Order("updated_at DESC, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		kb, err := s.readableKB(ctx, row.KnowledgeBaseID)
		if err != nil {
			continue
		}
		row.KnowledgeBaseName = kb.Name
		row.ScopeTenantID = row.TenantID
		row.CanManage = canManageWorkbench(caller, row)
		visible = append(visible, row)
	}
	if err := s.loadWorkbenchOnboarding(ctx, visible); err != nil {
		return nil, err
	}
	return visible, nil
}

func canManageWorkbench(caller types.Caller, row *types.ResearchWorkbench) bool {
	return row != nil && caller.Role.HasPermission(types.TenantRoleContributor) &&
		(row.UserID == caller.UserID || caller.Role.HasPermission(types.TenantRoleAdmin))
}

func (s *WorkspaceService) workbench(ctx context.Context, db *gorm.DB, id string, edit bool) (*types.ResearchWorkbench, error) {
	row, _, err := s.workbenchWithSource(ctx, db, id, edit)
	return row, err
}

func (s *WorkspaceService) workbenchWithSource(ctx context.Context, db *gorm.DB, id string, edit bool) (*types.ResearchWorkbench, *types.KnowledgeBase, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	var row types.ResearchWorkbench
	q := db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, caller.TenantID)
	if edit {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&row).Error; err != nil {
		return nil, nil, workspaceDBError(err)
	}
	kb, err := s.readableKB(ctx, row.KnowledgeBaseID)
	if err != nil {
		return nil, nil, err
	}
	if edit && !canManageWorkbench(caller, &row) {
		return nil, nil, apperrors.NewForbiddenError("仅创建者或空间管理员可修改工作台")
	}
	row.KnowledgeBaseName = kb.Name
	row.ScopeTenantID = row.TenantID
	row.CanManage = canManageWorkbench(caller, &row)
	return &row, kb, nil
}

func (s *WorkspaceService) GetWorkbench(ctx context.Context, id string) (*types.ResearchWorkbench, error) {
	row, err := s.workbench(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	_, defaultKB, err := s.defaultKnowledgeBase(ctx, caller)
	if err != nil {
		return nil, err
	}
	if defaultKB != nil && !defaultKB.DeletedAt.Valid {
		if _, err = s.readableKB(ctx, defaultKB.ID); err != nil {
			return nil, err
		}
		row.DefaultKnowledgeBase = &types.ResearchWorkbenchSource{KnowledgeBaseID: defaultKB.ID,
			TenantID: defaultKB.TenantID, Kind: macroKnowledgeBaseKind, Name: defaultKB.Name}
	}
	if err = s.loadWorkbenchOnboarding(ctx, []*types.ResearchWorkbench{row}); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *WorkspaceService) CreateWorkbench(ctx context.Context, input WorkspaceInput) (*types.ResearchWorkbench, error) {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !caller.Role.HasPermission(types.TenantRoleContributor) {
		return nil, apperrors.NewForbiddenError("需要空间编辑权限")
	}
	if err := validateWorkspaceInput(input, true); err != nil {
		return nil, err
	}
	if input.KnowledgeBaseID == nil {
		return nil, apperrors.NewBadRequestError("请选择知识库")
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 || strings.IndexFunc(input.IdempotencyKey, unicode.IsControl) >= 0 {
		return nil, apperrors.NewBadRequestError("创建请求标识无效")
	}
	kb, err := s.readableKB(ctx, *input.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	templateID, templateVersion, err := workbenchTemplate(input)
	if err != nil {
		return nil, err
	}
	row := &types.ResearchWorkbench{ID: uuid.NewString(), TenantID: caller.TenantID, UserID: caller.UserID, Name: *input.Name, KnowledgeBaseID: kb.ID, KnowledgeBaseName: kb.Name, IdempotencyKey: input.IdempotencyKey,
		WorkspaceMode: types.ResearchWorkbenchModeIsolated, TemplateID: templateID, TemplateVersion: templateVersion}
	if input.Description != nil {
		row.Description = *input.Description
	}
	raw, _ := json.Marshal([]any{row.Name, row.Description, row.KnowledgeBaseID, row.WorkspaceMode, row.TemplateID, row.TemplateVersion})
	hash := sha256.Sum256(raw)
	row.RequestHash = hex.EncodeToString(hash[:])
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Coordinate with source deletion after the earlier access check. SQLite
		// needs the writer reservation before reading to avoid lock upgrades.
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Exec("UPDATE knowledge_bases SET id = id WHERE id = ? AND tenant_id = ? AND deleted_at IS NULL", kb.ID, kb.TenantID).Error; err != nil {
				return err
			}
		}
		var activeKB types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", kb.ID, kb.TenantID).First(&activeKB).Error; err != nil {
			return workspaceDBError(err)
		}
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "idempotency_key"}}, DoNothing: true}).Create(row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}
		var existing types.ResearchWorkbench
		if err := tx.Unscoped().Where("tenant_id = ? AND user_id = ? AND idempotency_key = ?", caller.TenantID, caller.UserID, input.IdempotencyKey).First(&existing).Error; err != nil {
			return err
		}
		if existing.DeletedAt.Valid || (existing.RequestHash != row.RequestHash && !legacyWorkbenchRetry(existing, input, row)) {
			return apperrors.NewConflictError("该创建请求标识已用于其他内容，请重新发起创建")
		}
		row = &existing
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A repeated request must recheck the current binding after a later edit.
	return s.GetWorkbench(ctx, row.ID)
}

// Keep service-backed authorization outside the transaction: SQLite may have
// one connection, which the transaction already owns. Recheck live bindings and
// source rows on that same connection before writing, in KB -> workbench order.
func (s *WorkspaceService) mutateWorkbench(ctx context.Context, verified *types.ResearchWorkbench, sources []*types.KnowledgeBase, apply func(*gorm.DB, *types.ResearchWorkbench) error) error {
	caller, err := workspaceCaller(ctx)
	if err != nil {
		return err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, source := range sources {
			if tx.Dialector.Name() == "sqlite" {
				if err := tx.Exec("UPDATE knowledge_bases SET id = id WHERE id = ? AND tenant_id = ? AND deleted_at IS NULL", source.ID, source.TenantID).Error; err != nil {
					return err
				}
			}
			var active types.KnowledgeBase
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", source.ID, source.TenantID).First(&active).Error; err != nil {
				return workspaceDBError(err)
			}
		}
		var current types.ResearchWorkbench
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ?", verified.ID, caller.TenantID).First(&current).Error; err != nil {
			return workspaceDBError(err)
		}
		if !canManageWorkbench(caller, &current) {
			return apperrors.NewForbiddenError("仅创建者或空间管理员可修改工作台")
		}
		if current.KnowledgeBaseID != verified.KnowledgeBaseID {
			return apperrors.NewConflictError("工作台知识库绑定已变更，请刷新后重试")
		}
		return apply(tx, &current)
	})
}

func (s *WorkspaceService) UpdateWorkbench(ctx context.Context, id string, input WorkspaceInput) (*types.ResearchWorkbench, error) {
	if input.TemplateID != nil || input.TemplateVersion != nil {
		return nil, apperrors.NewBadRequestError("工作台模板和版本在创建时固定，不能通过编辑修改")
	}
	if err := validateWorkspaceInput(input, false); err != nil {
		return nil, err
	}
	var target *types.KnowledgeBase
	if input.KnowledgeBaseID != nil {
		var err error
		target, err = s.readableKB(ctx, *input.KnowledgeBaseID)
		if err != nil {
			return nil, err
		}
	}
	verified, source, err := s.workbenchWithSource(ctx, s.db, id, true)
	if err != nil {
		return nil, err
	}
	sources := []*types.KnowledgeBase{source}
	if target != nil && (target.ID != source.ID || target.TenantID != source.TenantID) {
		sources = append(sources, target)
	}
	err = s.mutateWorkbench(ctx, verified, sources, func(tx *gorm.DB, row *types.ResearchWorkbench) error {
		if input.Name != nil {
			row.Name = *input.Name
		}
		if input.Description != nil {
			row.Description = *input.Description
		}
		if input.KnowledgeBaseID != nil {
			if row.WorkspaceMode == types.ResearchWorkbenchModeIsolated && row.KnowledgeBaseID != *input.KnowledgeBaseID {
				return apperrors.NewConflictError("独立工作台不能更换主知识库，请基于目标知识库新建工作台")
			}
			row.KnowledgeBaseID = *input.KnowledgeBaseID
		}
		return tx.Save(row).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetWorkbench(ctx, id)
}

func (s *WorkspaceService) DeleteWorkbench(ctx context.Context, id string) error {
	verified, source, err := s.workbenchWithSource(ctx, s.db, id, true)
	if err != nil {
		return err
	}
	return s.mutateWorkbench(ctx, verified, []*types.KnowledgeBase{source}, func(tx *gorm.DB, row *types.ResearchWorkbench) error {
		return tx.Delete(row).Error
	})
}
