package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// Only non-secret runtime policy is readable by processing clients. A shared
// KB uses its owner's policy after a fresh KB grant, never the caller's space.
func (h *TenantHandler) GetPDFProcessingSettings(c *gin.Context) {
	ctx := c.Request.Context()
	tenant, _ := types.TenantInfoFromContext(ctx)
	if tenant == nil {
		c.Error(errors.NewForbiddenError("空间身份无效"))
		return
	}
	if id := c.GetHeader("X-Knowledge-Base-ID"); id != "" {
		kb, err := h.kbService.GetKnowledgeBaseByID(ctx, id)
		if err != nil || kb == nil {
			c.Error(errors.NewForbiddenError("没有当前知识库权限"))
			return
		}
		grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: types.CallerFromContext(ctx)}, kb, types.OrgRoleViewer, h.kbShareService, h.agentShareService)
		if err != nil || grant == nil || grant.KnowledgeBase == nil || grant.KnowledgeBase.ID != id {
			c.Error(errors.NewForbiddenError("没有当前知识库权限"))
			return
		}
		tenant, err = h.service.GetTenantByID(grant.WithGrant(ctx), kb.TenantID)
		if err != nil || tenant == nil {
			c.Error(errors.NewNotFoundError("知识库所属空间不可用"))
			return
		}
	}
	policy := types.EffectivePDFProcessingSettings(tenant.ParserEngineConfig)
	c.JSON(http.StatusOK, gin.H{"data": policy, "defaults": types.DefaultPDFProcessingSettings(),
		"can_edit": dto.CanViewIntegrationSecrets(ctx) && c.GetHeader("X-Knowledge-Base-ID") == ""})
}

func (h *TenantHandler) updatePDFProcessingSettings(c *gin.Context) {
	ctx := c.Request.Context()
	if !dto.CanViewIntegrationSecrets(ctx) {
		c.Error(errors.NewForbiddenError("只有管理员可修改PDF处理设置"))
		return
	}
	var policy types.PDFProcessingSettings
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		c.Error(errors.NewValidationError("PDF处理设置格式无效"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.Error(errors.NewValidationError("PDF处理设置只能包含一个对象"))
		return
	}
	if err := policy.Validate(); err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	tenant, _ := types.TenantInfoFromContext(ctx)
	if tenant == nil {
		c.Error(errors.NewForbiddenError("空间身份无效"))
		return
	}
	cfg := types.ParserEngineConfig{}
	if tenant.ParserEngineConfig != nil {
		cfg = *tenant.ParserEngineConfig
	}
	cfg.PDFProcessing = &policy
	tenant.ParserEngineConfig = &cfg
	if _, err := h.service.UpdateTenant(ctx, tenant); err != nil {
		c.Error(errors.NewInternalServerError("保存PDF处理设置失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy, "defaults": types.DefaultPDFProcessingSettings(), "can_edit": true})
}
