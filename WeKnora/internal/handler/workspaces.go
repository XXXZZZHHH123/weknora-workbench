package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/gin-gonic/gin"
)

type WorkspaceHandler struct{ service *service.WorkspaceService }

func NewWorkspaceHandler(s *service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{service: s}
}

func (h *WorkspaceHandler) GetDefaultKnowledgeBase(c *gin.Context) {
	row, err := h.service.GetDefaultKnowledgeBase(c.Request.Context())
	workspaceReply(c, row, err)
}

func (h *WorkspaceHandler) InitializeDefaultKnowledgeBase(c *gin.Context) {
	row, err := h.service.InitializeDefaultKnowledgeBase(c.Request.Context())
	workspaceReply(c, row, err)
}

func workspaceInput(c *gin.Context) (service.WorkspaceInput, bool) {
	var input service.WorkspaceInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("请求参数无效"))
		return input, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		_ = c.Error(apperrors.NewBadRequestError("请求参数无效"))
		return input, false
	}
	return input, true
}

func workspaceReply(c *gin.Context, data any, err error) {
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

func (h *WorkspaceHandler) ListProjects(c *gin.Context) {
	rows, err := h.service.ListProjects(c.Request.Context())
	workspaceReply(c, rows, err)
}
func (h *WorkspaceHandler) GetProject(c *gin.Context) {
	row, err := h.service.GetProject(c.Request.Context(), c.Param("id"))
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) CreateProject(c *gin.Context) {
	input, ok := workspaceInput(c)
	if !ok {
		return
	}
	row, err := h.service.CreateProject(c.Request.Context(), input)
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) UpdateProject(c *gin.Context) {
	input, ok := workspaceInput(c)
	if !ok {
		return
	}
	row, err := h.service.UpdateProject(c.Request.Context(), c.Param("id"), input)
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) DeleteProject(c *gin.Context) {
	workspaceReply(c, nil, h.service.DeleteProject(c.Request.Context(), c.Param("id")))
}
func (h *WorkspaceHandler) ProjectSessions(c *gin.Context) {
	page, e1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, e2 := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if e1 != nil || e2 != nil {
		_ = c.Error(apperrors.NewBadRequestError("分页参数无效"))
		return
	}
	rows, total, err := h.service.ProjectSessions(c.Request.Context(), c.Param("id"), page, size)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows, "total": total})
}
func (h *WorkspaceHandler) AssignSession(c *gin.Context) {
	workspaceReply(c, nil, h.service.AssignSession(c.Request.Context(), c.Param("id"), c.Param("session_id"), false))
}
func (h *WorkspaceHandler) RemoveSession(c *gin.Context) {
	workspaceReply(c, nil, h.service.AssignSession(c.Request.Context(), c.Param("id"), c.Param("session_id"), true))
}
func (h *WorkspaceHandler) ListWorkbenches(c *gin.Context) {
	rows, err := h.service.ListWorkbenches(c.Request.Context(), c.Query("knowledge_base_id"))
	workspaceReply(c, rows, err)
}
func (h *WorkspaceHandler) GetWorkbench(c *gin.Context) {
	row, err := h.service.GetWorkbench(c.Request.Context(), c.Param("id"))
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) CreateWorkbench(c *gin.Context) {
	input, ok := workspaceInput(c)
	if !ok {
		return
	}
	row, err := h.service.CreateWorkbench(c.Request.Context(), input)
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) UpdateWorkbench(c *gin.Context) {
	input, ok := workspaceInput(c)
	if !ok {
		return
	}
	row, err := h.service.UpdateWorkbench(c.Request.Context(), c.Param("id"), input)
	workspaceReply(c, row, err)
}
func (h *WorkspaceHandler) DeleteWorkbench(c *gin.Context) {
	workspaceReply(c, nil, h.service.DeleteWorkbench(c.Request.Context(), c.Param("id")))
}
