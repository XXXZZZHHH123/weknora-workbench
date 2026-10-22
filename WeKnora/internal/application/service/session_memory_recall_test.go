package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type memoryConversationSessions struct {
	interfaces.SessionRepository
	calls  int
	err    error
	tenant uint64
	owner  string
}

func (s *memoryConversationSessions) Get(_ context.Context, tenant uint64, owner, id string) (*types.Session, error) {
	s.calls++
	s.tenant, s.owner = tenant, owner
	if s.err != nil {
		return nil, s.err
	}
	return &types.Session{ID: id, TenantID: tenant, UserID: owner}, nil
}
func (*memoryConversationSessions) GetIMPlatform(context.Context, uint64, string) (string, error) {
	return "", nil
}

type memoryConversationMessages struct {
	interfaces.MessageRepository
	rows  []*types.Message
	calls int
}

func (m *memoryConversationMessages) GetMessagesBySession(_ context.Context, _ string, page, size int) ([]*types.Message, error) {
	m.calls++
	if page > 1 {
		return nil, nil
	}
	return m.rows, nil
}

func TestAgentMemoryRecallUsesAuthorizedCompleteConversationAndEffectiveModel(t *testing.T) {
	sessions := &memoryConversationSessions{}
	messages := &memoryConversationMessages{rows: []*types.Message{
		{ID: "old-user", SessionID: "session", RequestID: "old", Role: "user", Content: "The original task before the engine replay window", CreatedAt: time.Unix(1, 0)},
		{ID: "old-assistant", SessionID: "session", RequestID: "old", Role: "assistant", Content: "The original answer", IsCompleted: true, ContextCheckpoint: &types.ContextCheckpoint{Summary: "Existing compaction summary"}, CreatedAt: time.Unix(2, 0)},
		{ID: "current-assistant", SessionID: "session", Role: "assistant", CreatedAt: time.Unix(3, 0)},
	}}
	svc := &sessionService{sessionRepo: sessions, messageRepo: messages}
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	ctx = types.WithExecutionTenant(ctx, 99)
	ctx = context.WithValue(ctx, types.SessionTenantIDContextKey, uint64(55))
	req := &types.QARequest{Session: &types.Session{ID: "session", TenantID: 7}, Query: "Continue that task", AssistantMessageID: "current-assistant"}
	request := svc.agentMemoryRecallRequest(ctx, req, "actual-resolved-model", true, []chat.Message{{Role: "user", Content: "window tail only"}, {Role: "tool", Content: "raw tool bytes"}})
	require.False(t, request.ContextIncomplete)
	require.Equal(t, req.Query, request.Query)
	require.Equal(t, "actual-resolved-model", request.ModelID)
	require.Equal(t, "session", request.SessionID)
	require.Equal(t, uint64(7), sessions.tenant)
	require.Equal(t, "owner", sessions.owner)
	require.Len(t, request.Conversation, 3)
	require.Equal(t, "summary", request.Conversation[0].Role)
	require.Contains(t, request.Conversation[2].Content+request.Conversation[1].Content, "original task")
	for _, row := range request.Conversation {
		require.NotContains(t, row.Content, "window tail")
	}
}

func TestAgentMemoryRecallDisabledAndIncompleteHistoryBoundaries(t *testing.T) {
	sessions := &memoryConversationSessions{err: errors.New("not authorized")}
	messages := &memoryConversationMessages{}
	svc := &sessionService{sessionRepo: sessions, messageRepo: messages}
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	req := &types.QARequest{Session: &types.Session{ID: "session"}, Query: "current"}
	history := []chat.Message{{Role: "user", Content: "previous actual question"}, {Role: "assistant", Content: "previous answer"}, {Role: "tool", Content: "private raw result"}, {Role: "system", Content: "previous memory envelope"}}
	disabled := svc.agentMemoryRecallRequest(ctx, req, "model", false, history)
	require.Empty(t, disabled.Conversation)
	require.False(t, disabled.ContextIncomplete)
	require.Zero(t, sessions.calls)
	incomplete := svc.agentMemoryRecallRequest(ctx, req, "model", true, history)
	require.True(t, incomplete.ContextIncomplete)
	require.Len(t, incomplete.Conversation, 2, "fallback is labelled incomplete and excludes tool/system payloads")
	require.Zero(t, messages.calls, "authorization failure must not reach the message repository")
}
