package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/gin-gonic/gin"
)

type WorkspaceNotificationHandler struct {
	service *service.WorkspaceNotificationService
}

func NewWorkspaceNotificationHandler(s *service.WorkspaceNotificationService) *WorkspaceNotificationHandler {
	return &WorkspaceNotificationHandler{service: s}
}

func notificationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWorkspaceNotificationForbidden):
		c.Error(apperrors.NewForbiddenError("无权访问此空间的通知"))
	case errors.Is(err, service.ErrWorkspaceNotificationNotFound):
		c.Error(apperrors.NewNotFoundError("通知不存在"))
	default:
		c.Error(apperrors.NewInternalServerError("通知暂时无法加载"))
	}
}

func (h *WorkspaceNotificationHandler) List(c *gin.Context) {
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	afterID, limit := parseAuditCursor(c)
	page, err := h.service.List(c.Request.Context(), tenantID, afterID, limit)
	if err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": page.Data, "next_cursor": page.NextCursor, "unread_count": page.UnreadCount})
}

func (h *WorkspaceNotificationHandler) MarkRead(c *gin.Context) {
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("notification_id"), 10, 64)
	if err != nil || id == 0 {
		c.Error(apperrors.NewBadRequestError("无效的通知 ID"))
		return
	}
	if err := h.service.MarkRead(c.Request.Context(), tenantID, id); err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
