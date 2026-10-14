package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/Tencent/WeKnora/internal/types"
)

var structuredKnowledgeBaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type structuredKnowledgeScope struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	TenantID        uint64 `json:"knowledge_base_tenant_id"`
}

// WithSearchTargets snapshots the authenticated request scope before execution.
// Excel datasets cannot yet enforce native document/tag filters, so only whole
// knowledge-base grants may open their separate structured store.
func (t *StructuredDataTool) WithSearchTargets(targets types.SearchTargets) *StructuredDataTool {
	t.scopeConfigured = len(targets) > 0
	t.knowledgeScopes, t.scopeError = nil, nil
	owners := map[string]uint64{}
	allowed := map[string]bool{}
	for _, target := range targets {
		if target == nil {
			continue
		}
		if !structuredKnowledgeBaseID.MatchString(target.KnowledgeBaseID) || target.TenantID == 0 {
			t.scopeError = fmt.Errorf("结构化知识库授权信息无效")
			return t
		}
		if owner, exists := owners[target.KnowledgeBaseID]; exists && owner != target.TenantID {
			t.scopeError = fmt.Errorf("结构化知识库归属信息不一致")
			return t
		}
		owners[target.KnowledgeBaseID] = target.TenantID
		if searchTargetIsWholeKB(target) && !allowed[target.KnowledgeBaseID] {
			allowed[target.KnowledgeBaseID] = true
			t.knowledgeScopes = append(t.knowledgeScopes, structuredKnowledgeScope{target.KnowledgeBaseID, target.TenantID})
		}
	}
	return t
}

func (t *StructuredDataTool) resolveStructuredKnowledgeScope(raw json.RawMessage) (structuredKnowledgeScope, *types.ToolResult, error) {
	var selected string
	if raw != nil && (json.Unmarshal(raw, &selected) != nil || !structuredKnowledgeBaseID.MatchString(selected)) {
		return structuredKnowledgeScope{}, nil, fmt.Errorf("结构化知识库ID格式无效")
	}
	if t.scopeError != nil {
		return structuredKnowledgeScope{}, nil, t.scopeError
	}
	if !t.scopeConfigured {
		if raw != nil {
			return structuredKnowledgeScope{}, nil, fmt.Errorf("结构化知识库不在当前授权范围内")
		}
		return structuredKnowledgeScope{}, nil, nil
	}
	if len(t.knowledgeScopes) == 0 {
		return structuredKnowledgeScope{}, nil, fmt.Errorf("当前来源范围未授权整个结构化知识库；文档或标签范围不能扩展读取其他表格")
	}
	if raw == nil && len(t.knowledgeScopes) == 1 {
		return t.knowledgeScopes[0], nil, nil
	}
	for _, scope := range t.knowledgeScopes {
		if scope.KnowledgeBaseID == selected {
			return scope, nil, nil
		}
	}
	if raw != nil {
		return structuredKnowledgeScope{}, nil, fmt.Errorf("结构化知识库不在当前授权范围内")
	}
	data := map[string]any{
		"kind": "structured_knowledge_scope_directory", "status": "scope_selection_required",
		"knowledge_bases":    append([]structuredKnowledgeScope(nil), t.knowledgeScopes...),
		"selection_argument": "knowledge_base_id",
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return structuredKnowledgeScope{}, nil, err
	}
	return structuredKnowledgeScope{}, &types.ToolResult{Success: true, Output: string(encoded), Data: data}, nil
}

func (t *StructuredDataTool) authorizeStructuredAPIKeyScope(ctx context.Context, scope structuredKnowledgeScope, directory bool) error {
	if scope.KnowledgeBaseID != "" {
		// Reapply the current key restriction before cache lookup: a matching
		// caller and turn do not imply unchanged API-key permissions.
		return types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, scope.KnowledgeBaseID)
	}
	if directory {
		for _, allowed := range t.knowledgeScopes {
			if err := types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, allowed.KnowledgeBaseID); err != nil {
				return err
			}
		}
		return nil
	}
	if key, exists := types.TenantAPIKeyScopeFromContext(ctx); exists && !key.FullAccess {
		return fmt.Errorf("受限 API key 需要选择已授权的知识库，不能读取未绑定的旧表格目录")
	}
	return nil
}
