package service

import (
	"context"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
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
func (s *knowledgeService) DeleteKnowledgeFolder(ctx context.Context, kbID, path string) error {
	ctx, kb, normalized, err := s.writableDirectory(ctx, kbID, path)
	if err != nil {
		return err
	}
	return s.repo.DeleteKnowledgeFolder(ctx, kb.TenantID, kbID, normalized)
}
