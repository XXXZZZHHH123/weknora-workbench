package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const (
	structuredModelPromptBytes   = 192 * 1024
	structuredModelResponseBytes = 256 * 1024
	structuredModelMaxTokens     = 8192
	structuredModelTimeout       = 120 * time.Second
)

type structuredModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type structuredModelRequest struct {
	ModelID        string                   `json:"model_id"`
	Messages       []structuredModelMessage `json:"messages"`
	MaxTokens      int                      `json:"max_tokens"`
	Temperature    *float64                 `json:"temperature,omitempty"`
	EnableThinking *bool                    `json:"enable_thinking,omitempty"`
}

func structuredModelIDValid(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// A slow completion must not reuse an earlier request-local authorization grant.
func structuredModelKBAccess(c *gin.Context, knowledge *KnowledgeHandler, kbID string, caller types.Caller, required types.OrgMemberRole) error {
	ctx := c.Request.Context()
	if types.CallerFromContext(ctx) != caller {
		return apperrors.NewForbiddenError("当前空间身份已变化，请重试")
	}
	kb, err := knowledge.kbService.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil || kb == nil || kb.ID != kbID || kb.TenantID != caller.TenantID || kb.DeletedAt.Valid {
		return apperrors.NewNotFoundError("知识库不存在或不可使用")
	}
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, required, knowledge.kbShareService, knowledge.agentShareService)
	if err != nil {
		return kbAccessHTTPError(err)
	}
	if grant.KnowledgeBase == nil || grant.KnowledgeBase.ID != kbID || grant.KnowledgeBase.DeletedAt.Valid || grant.EffectiveTenantID != caller.TenantID {
		return apperrors.NewForbiddenError("知识库访问权限已失效")
	}
	if required == types.OrgRoleEditor {
		if err := access.RequireKBWrite(grant.WithGrant(ctx), grant.KnowledgeBase); err != nil {
			return kbAccessHTTPError(err)
		}
	}
	return nil
}

func structuredModelConfigured(ctx context.Context, models *ModelHandler, caller types.Caller, modelID string) (*types.Model, error) {
	model, err := models.service.GetModelByID(ctx, modelID)
	if err != nil || model == nil || model.ID != modelID || model.DeletedAt.Valid || (!model.IsBuiltin && model.TenantID != caller.TenantID) {
		return nil, apperrors.NewNotFoundError("模型不存在或不可使用")
	}
	if model.Type != types.ModelTypeKnowledgeQA || model.Status != types.ModelStatusActive {
		return nil, apperrors.NewBadRequestError("请选择已启用的对话模型")
	}
	return model, nil
}

// StructuredModelChat performs one bounded completion using the caller's model
// configuration. It creates neither a conversation nor a knowledge entry.
func StructuredModelChat(knowledge *KnowledgeHandler, models *ModelHandler) gin.HandlerFunc {
	return structuredModelCompletion(knowledge, models, types.OrgRoleEditor)
}

// StructuredModelPlan accepts a read-authorized member's text-only planning
// request. It grants no retrieval tools or persistent mutation capabilities.
func StructuredModelPlan(knowledge *KnowledgeHandler, models *ModelHandler) gin.HandlerFunc {
	return structuredModelCompletion(knowledge, models, types.OrgRoleViewer)
}

func structuredModelCompletion(knowledge *KnowledgeHandler, models *ModelHandler, minimumKBRole types.OrgMemberRole) gin.HandlerFunc {
	minimumTenantRole := types.TenantRoleContributor
	if minimumKBRole == types.OrgRoleViewer {
		minimumTenantRole = types.TenantRoleViewer
	}
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		caller := types.CallerFromContext(ctx)
		principal, hasPrincipal := types.PrincipalFromContext(ctx)
		_, apiKey := types.TenantAPIKeyScopeFromContext(ctx)
		if caller.TenantID == 0 || caller.UserID == "" {
			_ = c.Error(apperrors.NewUnauthorizedError("需要登录后使用模型辅助"))
			return
		}
		if apiKey || types.IsSyntheticUserID(caller.UserID) || !hasPrincipal || principal.Type != types.PrincipalWebUser || principal.ID != caller.UserID || !caller.Role.HasPermission(minimumTenantRole) {
			_ = c.Error(apperrors.NewForbiddenError("需要有效的主平台成员身份和相应空间权限"))
			return
		}
		if knowledge == nil || knowledge.kbService == nil || models == nil || models.service == nil {
			_ = c.Error(apperrors.NewServiceUnavailableError("结构化模型服务尚未配置"))
			return
		}
		kbID := c.Param("id")
		if !structuredModelIDValid(kbID, 36) {
			_ = c.Error(apperrors.NewBadRequestError("知识库 ID 无效"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 6*structuredModelPromptBytes+8192)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var input structuredModelRequest
		var trailing any
		if decoder.Decode(&input) != nil || decoder.Decode(&trailing) != io.EOF {
			_ = c.Error(apperrors.NewBadRequestError("模型请求参数无效或内容过大"))
			return
		}
		if !structuredModelIDValid(input.ModelID, types.ModelIDMaxLen) || len(input.Messages) == 0 || len(input.Messages) > 64 || input.MaxTokens < 0 || input.MaxTokens > structuredModelMaxTokens || (input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 1)) {
			_ = c.Error(apperrors.NewBadRequestError("请选择有效模型并提供限定范围的对话参数"))
			return
		}
		messages := make([]chat.Message, 0, len(input.Messages))
		messageBytes, userMessage := 0, false
		for _, message := range input.Messages {
			messageBytes += len(message.Content)
			if (message.Role != "system" && message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Content) == "" || messageBytes > structuredModelPromptBytes {
				_ = c.Error(apperrors.NewBadRequestError("仅接受不超过 192 KiB 的文字对话内容"))
				return
			}
			userMessage = userMessage || message.Role == "user"
			messages = append(messages, chat.Message{Role: message.Role, Content: message.Content})
		}
		if !userMessage {
			_ = c.Error(apperrors.NewBadRequestError("需要提供用户消息"))
			return
		}
		if err := structuredModelKBAccess(c, knowledge, kbID, caller, minimumKBRole); err != nil {
			_ = c.Error(err)
			return
		}
		modelCtx, cancel := context.WithTimeout(types.WithExecutionTenant(ctx, caller.TenantID), structuredModelTimeout)
		defer cancel()
		model, err := structuredModelConfigured(modelCtx, models, caller, input.ModelID)
		if err != nil {
			_ = c.Error(err)
			return
		}
		client, err := models.service.GetChatModel(modelCtx, model.ID)
		if err != nil || client == nil {
			_ = c.Error(apperrors.NewServiceUnavailableError("模型初始化失败，请检查模型配置后重试"))
			return
		}
		if input.MaxTokens == 0 {
			input.MaxTokens = structuredModelMaxTokens
		}
		options := &chat.ChatOptions{MaxTokens: input.MaxTokens, Thinking: input.EnableThinking}
		if input.Temperature != nil {
			options.Temperature = *input.Temperature
		}
		response, err := client.Chat(modelCtx, messages, options)
		if err != nil || modelCtx.Err() != nil || response == nil || strings.TrimSpace(response.Content) == "" || len(response.Content) > structuredModelResponseBytes || len(response.ToolCalls) > 0 || len(response.FinishReason) > 64 || strings.IndexFunc(response.FinishReason, unicode.IsControl) >= 0 {
			// Provider errors can contain headers, credentials and source excerpts.
			_ = c.Error(apperrors.NewServiceUnavailableError("模型未返回有效结果，请调整输入或模型配置后重试"))
			return
		}
		if err := structuredModelKBAccess(c, knowledge, kbID, caller, minimumKBRole); err != nil {
			_ = c.Error(err)
			return
		}
		if _, err := structuredModelConfigured(modelCtx, models, caller, input.ModelID); err != nil {
			_ = c.Error(err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
			"model_id": model.ID, "content": response.Content, "finish_reason": response.FinishReason,
		}})
	}
}
