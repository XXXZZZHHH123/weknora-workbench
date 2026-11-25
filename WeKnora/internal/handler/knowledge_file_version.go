package handler

import (
	goerrors "errors"
	"fmt"
	"mime/multipart"
	"net/http"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

func (h *KnowledgeHandler) UploadKnowledgeFileVersion(c *gin.Context) {
	grant, err := resolveHandlerKBAccessFor(c, c.Param("id"), h.kbService, h.kbShareService, h.agentShareService, types.OrgRoleEditor)
	if err != nil {
		c.Error(err)
		return
	}
	ctx := grant.Context(c.Request.Context())
	knowledgeID := c.Param("knowledge_id")
	current, err := h.kgService.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil || current == nil || current.KnowledgeBaseID != grant.KnowledgeBase.ID || current.TenantID != grant.EffectiveTenantID {
		c.Error(errors.NewNotFoundError("Knowledge not found"))
		return
	}

	file, err := knowledgeUploadFile(c)
	if err != nil {
		c.Error(err)
		return
	}
	updated, err := h.kgService.ReplaceKnowledgeFile(ctx, knowledgeID, file, "", nil)
	var duplicate *types.DuplicateKnowledgeError
	if err != nil && !goerrors.As(err, &duplicate) {
		var appErr *errors.AppError
		if goerrors.As(err, &appErr) {
			c.Error(appErr)
			return
		}
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(errors.NewInternalServerError("Failed to replace knowledge file"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": updated})
}

func knowledgeUploadFile(c *gin.Context) (*multipart.FileHeader, error) {
	maxSizeMB := utils.GetMaxFileSizeMB()
	maxSize := maxSizeMB * 1024 * 1024
	limitUploadBody(c, maxSize)
	file, err := c.FormFile("file")
	if isRequestBodyTooLarge(err) || (err == nil && file.Size > maxSize) {
		return nil, errors.NewBadRequestError(fmt.Sprintf("文件大小不能超过%dMB", maxSizeMB))
	}
	if err != nil {
		return nil, errors.NewBadRequestError("File upload failed").WithDetails(err.Error())
	}
	return file, nil
}
