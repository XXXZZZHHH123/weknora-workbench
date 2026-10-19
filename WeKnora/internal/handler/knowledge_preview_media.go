package handler

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type knowledgePreviewMediaRenderer interface {
	RenderKnowledgePreviewMedia(context.Context, *types.Knowledge, int, string, string) ([]byte, error)
}

// PreviewKnowledgeMedia returns one converted original PPTX media member.
// The route performs the same viewer and KB read authorization as file preview.
func (h *KnowledgeHandler) PreviewKnowledgeMedia(c *gin.Context) {
	id := c.Param("id")
	if strings.TrimSpace(id) == "" {
		c.Error(errors.NewBadRequestError("Knowledge ID cannot be empty"))
		return
	}
	page, err := strconv.Atoi(c.Query("page"))
	part := c.Query("part")
	sourceSHA := c.Query("source_sha256")
	var decodedSHA []byte
	var hashErr error
	if len(sourceSHA) == 64 {
		decodedSHA, hashErr = hex.DecodeString(sourceSHA)
	}
	if err != nil || page < 1 || strings.TrimSpace(part) == "" || len(sourceSHA) != 64 || hashErr != nil || len(decodedSHA) != 32 {
		c.Error(errors.NewBadRequestError("Valid page, part, and source_sha256 parameters are required"))
		return
	}
	knowledge, effCtx, err := h.resolveKnowledgeAndValidateKBAccess(c, id, types.OrgRoleViewer)
	if err != nil {
		c.Error(err)
		return
	}
	renderer, ok := h.kgService.(knowledgePreviewMediaRenderer)
	if !ok {
		c.Error(errors.NewServiceUnavailableError("Media preview is unavailable"))
		return
	}
	pngBytes, err := renderer.RenderKnowledgePreviewMedia(effCtx, knowledge, page, part, sourceSHA)
	if err != nil {
		logger.Warnf(c.Request.Context(), "Source media preview failed for knowledge %s: %v", id, err)
		if appErr, ok := errors.IsAppError(err); ok {
			c.Error(appErr)
			return
		}
		c.Error(errors.NewInternalServerError("Source media preview failed"))
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "image/png", pngBytes)
}
