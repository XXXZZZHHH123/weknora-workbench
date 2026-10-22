package handler

import (
	"encoding/base64"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/models/imagegen"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// checkImageGenerationDraft is reached only through the existing authenticated
// remote/check route (Admin/manage_models), and only on explicit image test
// requests. It never creates a model, message, resource or generation record.
func (h *InitializationHandler) checkImageGenerationDraft(c *gin.Context, req *ModelTestRequest) {
	middleware.OmitBodyLogging(c)
	ctx := c.Request.Context()
	// Preserve the route's normal RBAC/API-key gate, and fail closed without its
	// authenticated tenant context. Machine callers need no artificial user ID.
	tenantID, validTenant := types.TenantIDFromContext(ctx)
	if !validTenant || tenantID == 0 || types.CallerFromContext(ctx).TenantID != tenantID {
		_ = c.Error(errors.NewUnauthorizedError("An authenticated workspace is required"))
		return
	}
	if !req.ImageGenerationTest {
		_ = c.Error(errors.NewBadRequestError("Image generation testing requires an explicit imageGenerationTest action"))
		return
	}
	if req.Source != "" && req.Source != string(types.ModelSourceRemote) {
		_ = c.Error(errors.NewBadRequestError("Image generation testing requires a remote model"))
		return
	}
	if req.ModelID != "" {
		if h.modelService == nil || len(req.ModelID) > types.ModelIDMaxLen || strings.TrimSpace(req.ModelID) != req.ModelID {
			_ = c.Error(errors.NewBadRequestError("The saved image generation model is unavailable"))
			return
		}
		stored, err := h.modelService.GetModelByID(ctx, req.ModelID)
		if err != nil || stored == nil || stored.ID != req.ModelID || (!stored.IsBuiltin && stored.TenantID != tenantID) {
			_ = c.Error(errors.NewNotFoundError("The saved image generation model is unavailable"))
			return
		}
		if stored.Type != types.ModelTypeImageGeneration || stored.Source != types.ModelSourceRemote || stored.Status != types.ModelStatusActive {
			_ = c.Error(errors.NewBadRequestError("The saved model must be an active remote image generation model"))
			return
		}
		// Builtin models are global configuration. A tenant admin cannot borrow a
		// global credential to probe a modified draft endpoint.
		if stored.IsBuiltin && !types.IsSystemAdminFromContext(ctx) {
			_ = c.Error(errors.NewForbiddenError("Only system administrators may test builtin model configuration"))
			return
		}
		fillModelTestSecretsFromStored(req, stored)
	}
	c.Header("Cache-Control", "no-store")
	respond := func(available bool, message string, image any, diagnostic map[string]any) {
		data := gin.H{"available": available, "message": message}
		if diagnostic != nil {
			data["diagnostic"] = diagnostic
		}
		if image != nil {
			data["image"] = image
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
	}
	model := h.buildTestModel(req, types.ModelTypeImageGeneration, types.ModelSourceRemote)
	if err := modelruntime.ValidateRow(model.Name, model.Type, &model.Parameters); err != nil {
		respond(false, "生图配置无效，请检查模型、接口地址和参数。", nil, map[string]any{"kind": "configuration"})
		return
	}
	generator, err := imagegen.New(imagegen.ConfigFromModel(model))
	if err != nil {
		respond(false, "生图配置无效，请检查模型、接口地址、API Key和参数。", nil, map[string]any{"kind": "configuration"})
		return
	}
	prompt := strings.TrimSpace(req.ImagePrompt)
	if prompt == "" {
		// A connection-test fixture, not a business-question classifier or answer.
		prompt = "A simple blue circle on a plain white background."
	}
	// The shared generator makes exactly one bounded request, decodes real image
	// bytes and validates MIME/dimensions. Do not add retry or chat fallback here.
	generated, err := generator.Generate(ctx, imagegen.Request{Prompt: prompt})
	if err != nil || generated == nil {
		var provider *imagegen.ProviderError
		if stderrors.As(err, &provider) {
			respond(false, provider.Error()+"；本次请求不会自动重试。", nil, provider.Diagnostic())
		} else {
			respond(false, "生图测试失败或被中断。请检查接口权限、参数及供应商状态；本次请求不会自动重试。", nil, map[string]any{"kind": "image_generation_failed"})
		}
		return
	}
	respond(true, "已生成测试图片；未保存模型配置，也未归档到聊天。", gin.H{
		"data_url": "data:" + generated.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(generated.Data),
		"width":    generated.Width, "height": generated.Height, "mime_type": generated.MIMEType,
	}, nil)
}
