package handler

import (
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/gin-gonic/gin"
)

func (h *WorkspaceHandler) SetWorkbenchPin(c *gin.Context) {
	var input struct {
		IsPinned *bool `json:"is_pinned"`
	}
	if !workspacePermissionInput(c, &input) {
		return
	}
	if input.IsPinned == nil {
		_ = c.Error(apperrors.NewBadRequestError("请选择置顶状态"))
		return
	}
	row, err := h.service.SetWorkbenchPin(c.Request.Context(), c.Param("id"), *input.IsPinned)
	workspaceReply(c, row, err)
}
