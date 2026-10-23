package session

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

func (h *Handler) resolveResearchWorkbenchScope(ctx context.Context, session *types.Session) (types.ResearchWorkbenchScope, error) {
	var scope types.ResearchWorkbenchScope
	if session.ResearchWorkbenchID == "" {
		return scope, nil
	}
	if _, err := uuid.Parse(session.ResearchWorkbenchID); err != nil || h.researchWorkbenches == nil || h.knowledgebaseService == nil {
		return scope, errors.NewNotFoundError("研究工作台不存在或无权访问")
	}
	caller := types.CallerFromContext(ctx)
	row, err := h.researchWorkbenches.GetWorkbench(ctx, session.ResearchWorkbenchID)
	if err != nil || row == nil || row.ID != session.ResearchWorkbenchID || (row.WorkspaceMode != "isolated" && row.WorkspaceMode != "legacy") ||
		row.TenantID != session.TenantID || row.TenantID != caller.TenantID || caller.UserID == "" {
		return scope, errors.NewNotFoundError("研究工作台不存在或无权访问")
	}
	// Workbench sharing may authorize indicator projections without raw source
	// access. Full-text chat must independently verify the caller's KB grant.
	kb, err := h.knowledgebaseService.GetKnowledgeBaseByIDOnly(ctx, row.KnowledgeBaseID)
	if err != nil || kb == nil || kb.ID != row.KnowledgeBaseID || kb.TenantID == 0 || kb.DeletedAt.Valid {
		return scope, errors.NewNotFoundError("绑定的知识库不存在或无权访问")
	}
	if _, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer, h.kbShareService, nil); err != nil {
		return scope, errors.NewForbiddenError("工作台全文问答需要来源知识库查看权限")
	}
	return types.ResearchWorkbenchScope{ID: row.ID, TenantID: row.TenantID, UserID: caller.UserID,
		KnowledgeBaseID: kb.ID, KnowledgeBaseTenantID: kb.TenantID}, nil
}

func (h *Handler) bindResearchQuestionScope(ctx context.Context, session *types.Session, request *CreateKnowledgeQARequest) (context.Context, error) {
	if session.ResearchWorkbenchID == "" {
		return ctx, nil
	}
	scope, err := h.resolveResearchWorkbenchScope(ctx, session)
	if err != nil {
		return ctx, err
	}
	if request.AgentSourceTenantID != 0 || (request.AgentID != "" && request.AgentID != types.BuiltinQuickAnswerID && request.AgentID != types.BuiltinSmartReasoningID) ||
		request.WebSearchEnabled || request.LocalBrowserEnabled || len(request.MCPServiceIDs) > 0 || len(request.SkillNames) > 0 {
		return ctx, errors.NewBadRequestError("工作台问答使用绑定资料范围，请在独立对话中使用其他助手或外部工具")
	}
	kbIDs, knowledgeIDs := mergeKnowledgeTargets(request.KnowledgeBaseIDs, request.KnowledgeIDs, request.MentionedItems)
	for _, id := range kbIDs {
		if id != scope.KnowledgeBaseID {
			return ctx, errors.NewForbiddenError("问答来源必须属于当前工作台绑定知识库")
		}
	}
	for _, item := range request.MentionedItems {
		if item.Type == "mcp" || item.Type == "skill" || item.KBID != "" && item.KBID != scope.KnowledgeBaseID {
			return ctx, errors.NewForbiddenError("问答来源必须属于当前工作台绑定知识库")
		}
	}
	for _, id := range knowledgeIDs {
		if h.knowledgeService == nil {
			return ctx, errors.NewForbiddenError("无法核验文件所属知识库")
		}
		file, err := h.knowledgeService.GetKnowledgeByIDOnly(ctx, id)
		if err != nil || file == nil || file.KnowledgeBaseID != scope.KnowledgeBaseID || file.DeletedAt.Valid {
			return ctx, errors.NewForbiddenError("问答文件不属于当前工作台绑定知识库")
		}
	}
	request.KnowledgeBaseIDs = []string{scope.KnowledgeBaseID}
	return types.WithResearchWorkbenchScope(ctx, scope), nil
}

func isolatedResearchAgent(original *types.CustomAgent, scope types.ResearchWorkbenchScope) *types.CustomAgent {
	agent := &types.CustomAgent{ID: types.BuiltinQuickAnswerID, IsBuiltin: true, TenantID: scope.TenantID, WorkbenchDefault: true}
	if original != nil {
		// Builtin configuration may be cached across requests. Keep model choices
		// while cloning every nested capability before narrowing this session.
		if raw, err := json.Marshal(original.Config); err == nil {
			_ = json.Unmarshal(raw, &agent.Config)
		}
	}
	agent.Config.AgentMode = types.AgentModeSmartReasoning
	agent.Config.AgentType = types.AgentTypeCustom
	agent.Config.MultiTurnEnabled = true
	agent.Config.SystemPromptID, agent.Config.ContextTemplateID = "", ""
	agent.Config.SystemPrompt = workbenchDefaultPrompt
	if agent.Config.MaxIterations >= 0 && agent.Config.MaxIterations < 20 {
		agent.Config.MaxIterations = 20
	}
	agent.Config.KBSelectionMode = "selected"
	agent.Config.KnowledgeBases = []string{scope.KnowledgeBaseID}
	agent.Config.AllowedTools = []string{tools.ToolSearchKnowledge, tools.ToolReadDocument, tools.ToolListDocuments}
	if strings.TrimSpace(os.Getenv("STRUCTURED_DATA_URL")) != "" {
		agent.Config.AllowedTools = append(agent.Config.AllowedTools, tools.ToolStructuredData)
	}
	if strings.TrimSpace(os.Getenv("COMMODITY_RESEARCH_URL")) != "" {
		agent.Config.AllowedTools = append(agent.Config.AllowedTools, tools.ToolCommodityResearch)
	}
	disabled := false
	agent.Config.MemoryEnabled = &disabled
	agent.Config.MCPSelectionMode, agent.Config.SkillsSelectionMode = "none", "none"
	agent.Config.MCPServices, agent.Config.SelectedSkills = nil, nil
	agent.Config.SandboxConfigID = ""
	agent.Config.SystemPrompt += "\n本轮已由服务器固定到一个研究工作台：指标和观测只来自绑定知识库当前已确认的结构化来源，研究工具提供本台与当前账号的研究成果。空白、未导入、失效或缺失来源如实说明，不读取其他工作台或历史研究库数值补齐。"
	return agent
}
