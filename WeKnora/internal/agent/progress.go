package agent

import (
	"context"

	"github.com/Tencent/WeKnora/internal/event"
)

func (e *AgentEngine) emitProgress(ctx context.Context, stage string, iteration int, sessionID string) {
	_ = e.eventBus.Emit(ctx, event.Event{ID: generateEventID("progress"), Type: event.EventAgentProgress,
		SessionID: sessionID, Data: event.AgentProgressData{Stage: stage, Iteration: iteration}})
}
