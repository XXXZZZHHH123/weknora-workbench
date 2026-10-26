package handler

import (
	"bytes"
	"context"
	"github.com/Tencent/WeKnora/internal/ingestion"
	"github.com/gin-gonic/gin"
	"os"
	"time"
)

// ValidateIngestionTask is a fixed-purpose worker callback. It does not accept
// user bearer tokens, source paths or model-selected destinations.
func (h *KnowledgeHandler) ValidateIngestionTask(c *gin.Context) {
	key, err := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	if err != nil {
		c.JSON(503, gin.H{"valid": false})
		return
	}
	claim, err := ingestion.Verify(c.Request, bytes.TrimSpace(key), time.Now())
	if err != nil {
		c.JSON(401, gin.H{"valid": false})
		return
	}
	validator, ok := h.kgService.(interface {
		ValidateIngestionSource(context.Context, ingestion.Claim) error
	})
	if !ok {
		c.JSON(503, gin.H{"valid": false})
		return
	}
	if err = validator.ValidateIngestionSource(c.Request.Context(), claim); err != nil {
		c.JSON(409, gin.H{"valid": false})
		return
	}
	c.JSON(200, gin.H{"valid": true})
}
