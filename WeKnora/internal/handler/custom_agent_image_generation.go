package handler

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// Validate the saved binding using the caller's workspace-scoped model lookup.
// Remote model configuration is not probed here: saving an agent must never
// trigger a paid image generation request.
func (h *CustomAgentHandler) validateAgentImageGenerationModel(ctx context.Context, cfg types.CustomAgentConfig) error {
	id := strings.TrimSpace(cfg.ImageGenerationModelID)
	if id == "" {
		return nil
	}
	if h.modelService == nil {
		return errors.NewInternalServerError("Image generation model service is unavailable")
	}
	model, err := h.modelService.GetModelByID(ctx, id)
	if err != nil || model == nil {
		return errors.NewBadRequestError("所选生图模型不存在或无权使用，请重新选择")
	}
	if model.Type != types.ModelTypeImageGeneration {
		return errors.NewBadRequestError("请选择图片生成类型的模型")
	}
	if model.Status != types.ModelStatusActive {
		return errors.NewBadRequestError("所选生图模型尚不可用")
	}
	return nil
}
