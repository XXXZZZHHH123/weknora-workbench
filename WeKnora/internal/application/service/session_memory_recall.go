package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/memorycontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *sessionService) agentMemoryRecallRequest(ctx context.Context, req *types.QARequest, modelID string, multiTurn bool, history []chat.Message) interfaces.MemoryRecallRequest {
	request := interfaces.MemoryRecallRequest{Query: req.Query, ModelID: modelID, SessionID: req.Session.ID}
	if !multiTurn {
		return request
	}
	for _, message := range history {
		if message.Role == "user" || message.Role == "assistant" {
			request.Conversation = append(request.Conversation, interfaces.MemoryConversationMessage{Role: message.Role, Content: message.Content})
		}
	}
	request.ContextIncomplete = true
	if s.sessionRepo == nil || s.messageRepo == nil {
		return request
	}
	readCtx, err := memorycontext.SessionReadContext(ctx)
	if err != nil {
		return request
	}
	tenantID := types.CallerFromContext(readCtx).TenantID
	session, err := loadSessionForRead(readCtx, s.sessionRepo, tenantID, types.SessionOwnerIDFromContext(readCtx), req.Session.ID)
	if err != nil || session == nil || session.ID != req.Session.ID || session.TenantID != tenantID {
		return request
	}
	conversation, err := memorycontext.LoadCompleted(readCtx, s.messageRepo.GetMessagesBySession, req.Session.ID, req.AssistantMessageID, req.UserMessageID)
	if err != nil {
		return request
	}
	request.Conversation, request.ContextIncomplete = conversation, false
	return request
}
