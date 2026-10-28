package handler

import (
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/gin-gonic/gin"
)

type UserFeedbackHandler struct {
	service *service.UserFeedbackService
}

func NewUserFeedbackHandler(s *service.UserFeedbackService) *UserFeedbackHandler {
	return &UserFeedbackHandler{service: s}
}

// Submit records a bug report or suggestion from the signed-in user.
func (h *UserFeedbackHandler) Submit(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	var input service.UserFeedbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("反馈参数无效"))
		return
	}
	row, err := h.service.Submit(c.Request.Context(), input, c.Request.UserAgent())
	workspaceReply(c, row, err)
}

// List returns platform-wide feedback for system administrators.
func (h *UserFeedbackHandler) List(c *gin.Context) {
	params := service.UserFeedbackListParams{Status: c.Query("status"), Kind: c.Query("kind")}
	if v := c.Query("after_id"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			_ = c.Error(apperrors.NewBadRequestError("after_id 无效"))
			return
		}
		params.AfterID = id
	}
	if v := c.Query("limit"); v != "" {
		params.Limit, _ = strconv.Atoi(v)
	}
	page, err := h.service.List(c.Request.Context(), params)
	workspaceReply(c, page, err)
}

func (h *UserFeedbackHandler) SetStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		_ = c.Error(apperrors.NewBadRequestError("反馈ID无效"))
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("状态无效"))
		return
	}
	err = h.service.SetStatus(c.Request.Context(), id, input.Status)
	workspaceReply(c, gin.H{"id": id, "status": input.Status}, err)
}
