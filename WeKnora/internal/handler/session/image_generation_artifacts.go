package session

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

func (h *AgentStreamHandler) SetArtifactMessageReader(reader interface {
	GetMessage(context.Context, string, string) (*types.Message, error)
}) {
	h.artifactMessageReader = reader
}

func (h *AgentStreamHandler) refreshPersistedArtifacts(ctx context.Context) {
	if h.artifactMessageReader == nil || h.assistantMessage == nil {
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	readCtx = context.WithValue(readCtx, types.TenantIDContextKey, h.tenantID)
	message, err := h.artifactMessageReader.GetMessage(readCtx, h.sessionID, h.assistantMessageID)
	if err != nil || message == nil {
		logger.Warnf(readCtx, "Could not refresh persisted message artifacts")
		return
	}
	h.mu.Lock()
	h.assistantMessage.Artifacts = mergeArtifactLists(message.Artifacts, h.assistantMessage.Artifacts)
	h.mu.Unlock()
}
