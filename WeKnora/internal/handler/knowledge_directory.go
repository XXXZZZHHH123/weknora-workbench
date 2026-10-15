package handler

import (
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *KnowledgeHandler) directoryWriteContext(c *gin.Context) (string, bool) {
	_, kbID, effectiveTenant, permission, err := h.validateKnowledgeBaseWriteAccessWithKBID(c, c.Param("id"))
	if err != nil {
		_ = c.Error(err)
		return "", false
	}
	if permission != types.OrgRoleAdmin && permission != types.OrgRoleEditor {
		_ = c.Error(errors.NewForbiddenError("No permission to modify knowledge"))
		return "", false
	}
	if err = h.requireKBOwnershipOrAdmin(c, kbID); err != nil {
		_ = c.Error(err)
		return "", false
	}
	c.Request = c.Request.WithContext(types.WithExecutionTenant(c.Request.Context(), effectiveTenant))
	return kbID, true
}
func (h *KnowledgeHandler) CreateKnowledgeFolder(c *gin.Context) {
	var body struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(errors.NewBadRequestError("请输入文件夹路径"))
		return
	}
	kbID, ok := h.directoryWriteContext(c)
	if !ok {
		return
	}
	row, err := h.kgService.CreateKnowledgeFolder(c.Request.Context(), kbID, body.Path)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": row})
}
func (h *KnowledgeHandler) DeleteKnowledgeFolder(c *gin.Context) {
	kbID, ok := h.directoryWriteContext(c)
	if !ok {
		return
	}
	if err := h.kgService.DeleteKnowledgeFolder(c.Request.Context(), kbID, c.Query("path")); err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": nil})
}
