package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *knowledgeService) writableDirectory(ctx context.Context, kbID, path string) (context.Context, *types.KnowledgeBase, string, error) {
	normalized, err := normalizeTargetFolderPath(ctx, path)
	if err != nil {
		return ctx, nil, "", err
	}
	if normalized == "" || normalized != strings.TrimSpace(path) {
		return ctx, nil, "", apperrors.NewBadRequestError("请输入有效的文件夹路径，最多16层")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return ctx, nil, "", err
	}
	if kb == nil || kb.ID != kbID {
		return ctx, nil, "", apperrors.NewNotFoundError("知识库不存在")
	}
	ctx, err = requireKBWrite(ctx, kb)
	return ctx, kb, normalized, err
}

func (s *knowledgeService) CreateKnowledgeFolder(ctx context.Context, kbID, path string) (*types.KnowledgeFolder, error) {
	ctx, kb, normalized, err := s.writableDirectory(ctx, kbID, path)
	if err != nil {
		return nil, err
	}
	return s.repo.CreateKnowledgeFolder(ctx, kb.TenantID, kbID, normalized)
}
func (s *knowledgeService) DeleteKnowledgeFolder(ctx context.Context, kbID, path string, recursive ...bool) error {
	ctx, kb, normalized, err := s.writableDirectory(ctx, kbID, path)
	if err != nil {
		return err
	}
	if len(recursive) > 0 && recursive[0] {
		return s.deleteKnowledgeFolderRecursive(ctx, kb, normalized)
	}
	return s.repo.DeleteKnowledgeFolder(ctx, kb.TenantID, kbID, normalized)
}

func incompleteFolderDeletion(ctx context.Context, err error, deleted int) error {
	logger.Warnf(ctx, "Directory cleanup incomplete after %d documents: %v", deleted, err)
	return apperrors.NewConflictError("文件夹删除未完成，可重试；已完成的文件删除会保留，目录将在全部处理完成后移除").
		WithDetails(map[string]any{"retryable": true, "deleted_documents": deleted})
}

func (s *knowledgeService) deleteKnowledgeFolderRecursive(ctx context.Context, kb *types.KnowledgeBase, path string) error {
	deletion, ok := s.repo.(interfaces.KnowledgeFolderDeletionRepository)
	if !ok {
		return apperrors.NewServiceUnavailableError("当前存储暂不支持文件夹递归删除")
	}
	// Bound one synchronous request. Both its persisted reservation and any
	// parse_status=deleting rows remain available to an explicit retry.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := deletion.BeginKnowledgeFolderDeletion(ctx, kb.TenantID, kb.ID, path); err != nil {
		return err
	}
	lease, err := deletion.ClaimKnowledgeFolderDeletion(ctx, kb.TenantID, kb.ID, path)
	if err != nil {
		return err
	}
	defer func() {
		// Request cancellation must not keep a healthy-process lease occupied.
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer releaseCancel()
		if err := deletion.ReleaseKnowledgeFolderDeletion(releaseCtx, kb.TenantID, kb.ID, path, lease); err != nil {
			logger.Warnf(releaseCtx, "Failed to release directory cleanup lease: %v", err)
		}
	}()
	deleted := 0
	for batch := 0; batch < 100; batch++ {
		if err := ctx.Err(); err != nil {
			return incompleteFolderDeletion(ctx, err, deleted)
		}
		ids, err := deletion.ListKnowledgeFolderDeletionBatch(ctx, kb.TenantID, kb.ID, path, 50)
		if err != nil {
			return incompleteFolderDeletion(ctx, err, deleted)
		}
		if len(ids) == 0 {
			if err := deletion.FinishKnowledgeFolderDeletion(ctx, kb.TenantID, kb.ID, path, lease); err != nil {
				return incompleteFolderDeletion(ctx, err, deleted)
			}
			return nil
		}
		plan, err := s.planKnowledgeDelete(ctx, ids)
		if err != nil {
			return incompleteFolderDeletion(ctx, err, deleted)
		}
		// The persisted, authorized directory determines the selection. A stale
		// page may never widen deletion to a document that moved elsewhere.
		for _, row := range plan.knowledge {
			if row.TenantID != kb.TenantID || row.KnowledgeBaseID != kb.ID ||
				(row.FolderPath != path && !strings.HasPrefix(row.FolderPath, path+"/")) {
				return incompleteFolderDeletion(ctx, apperrors.NewConflictError("文件位置已变更"), deleted)
			}
		}
		if err := s.executeKnowledgeDelete(plan, false); err != nil {
			return incompleteFolderDeletion(ctx, err, deleted)
		}
		deleted += len(plan.ids)
	}
	// A final native transaction catches late writes, including legacy writers
	// that did not participate in the directory namespace reservation.
	if err := deletion.FinishKnowledgeFolderDeletion(ctx, kb.TenantID, kb.ID, path, lease); err != nil {
		return incompleteFolderDeletion(ctx, err, deleted)
	}
	return nil
}
