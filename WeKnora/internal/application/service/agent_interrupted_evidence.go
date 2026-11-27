package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

// InterruptedSourceAuthorization reuses current session retrieval grants. The
// old tool result supplies identity only; it cannot grant access after restart.
func (s *sessionService) InterruptedSourceAuthorization(ctx context.Context, message *types.Message, durable bool) func(agent.InterruptedSource) bool {
	scope := message.ExecutionContext
	tenant := types.CallerFromContext(ctx).TenantID
	if tenant == 0 || s.knowledgeBaseService == nil || s.knowledgeService == nil || len(scope.TagIDs) > 0 && len(scope.TagScopes) == 0 {
		return nil
	}
	targets, err := s.buildSearchTargets(ctx, tenant, scope.KnowledgeBaseIDs, scope.KnowledgeIDs, scope.TagScopes)
	explicitScope := len(scope.KnowledgeBaseIDs) != 0 || len(scope.KnowledgeIDs) != 0 || len(scope.TagScopes) != 0
	if err != nil || len(targets) == 0 && (!durable || explicitScope) {
		return nil
	}
	checked := make(map[agent.InterruptedSource]bool)
	return func(source agent.InterruptedSource) bool {
		if allowed, seen := checked[source]; seen {
			return allowed
		}
		allowed := s.interruptedSourceCurrent(ctx, targets, source)
		checked[source] = allowed
		return allowed
	}
}

func (s *sessionService) legacyInterruptedMessage(ctx context.Context, message *types.Message) *types.Message {
	if message == nil || message.Role != "assistant" || !message.IsCompleted {
		return message
	}
	deadline, legacy := agent.LegacyInterruptedAnswer(message.Content)
	if !legacy {
		return message
	}
	if locale := message.ExecutionContext.Locale; locale != "" {
		ctx = context.WithValue(ctx, types.LanguageContextKey, locale)
	}
	updated := *message
	updated.Content = agent.RenderInterruptedEvidence(ctx, agent.InterruptedEvidenceFromSteps(message.AgentSteps),
		s.InterruptedSourceAuthorization(ctx, message, false), deadline, agent.InterruptedProgressFromMessage(message))
	updated.RunState = types.MessageRunStateInterrupted
	updated.KnowledgeReferences = nil
	updated.EvidenceReferences = nil
	return &updated
}

func (s *sessionService) interruptedSourceCurrent(ctx context.Context, targets types.SearchTargets, source agent.InterruptedSource) bool {
	if source.KnowledgeID == "" || source.KnowledgeBaseID == "" {
		return false
	}
	scopedKnowledge := source.KnowledgeID
	if source.Structured {
		scopedKnowledge = "" // Native structured reads require a whole library grant.
	}
	if len(targets) > 0 && !tools.SearchTargetsIncludeSource(targets, source.KnowledgeBaseID, scopedKnowledge) {
		return false
	}
	kb, err := s.knowledgeBaseService.GetKnowledgeBaseByIDOnly(ctx, source.KnowledgeBaseID)
	if err != nil || kb == nil || kb.ID != source.KnowledgeBaseID || kb.DeletedAt.Valid {
		return false
	}
	owner, err := resolveKBReadTenant(ctx, kb, s.kbShareService)
	if err != nil || owner == 0 || types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, kb.ID) != nil {
		return false
	}
	ctx = types.WithExecutionTenant(ctx, owner)
	knowledge, err := s.knowledgeService.GetKnowledgeByID(ctx, source.KnowledgeID)
	if err != nil || knowledge == nil || knowledge.ID != source.KnowledgeID || knowledge.TenantID != owner ||
		knowledge.KnowledgeBaseID != kb.ID || knowledge.DeletedAt.Valid || knowledge.EnableStatus == "disabled" ||
		knowledge.ParseStatus != types.ParseStatusCompleted {
		return false
	}
	if source.Structured {
		hash := source.FileHash
		if len(knowledge.FileHash) == 64 {
			hash = source.SHA256
		}
		return hash != "" && strings.EqualFold(hash, knowledge.FileHash)
	}
	if source.ChunkID == "" || s.chunkService == nil {
		return false
	}
	chunk, err := s.chunkService.GetChunkByIDOnly(ctx, source.ChunkID)
	if err != nil || chunk == nil || chunk.ID != source.ChunkID || chunk.KnowledgeID != knowledge.ID ||
		chunk.KnowledgeBaseID != kb.ID || chunk.TenantID != owner || !chunk.IsEnabled || chunk.DeletedAt.Valid {
		return false
	}
	if source.ParentChunkID != "" && (chunk.ParentChunkID != source.ParentChunkID || chunk.ChunkType != types.ChunkTypeImageOCR) {
		return false
	}
	if source.ContentSHA256 != "" {
		digest := sha256.Sum256([]byte(chunk.Content))
		return hex.EncodeToString(digest[:]) == source.ContentSHA256
	}
	return true
}
