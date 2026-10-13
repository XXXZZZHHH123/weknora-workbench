package session

import (
	"context"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAgentProgressIsStageOnlyAndNeverCompletesAnswer(t *testing.T) {
	stream := &capturingStreamManager{}
	h := &AgentStreamHandler{ctx: context.Background(), sessionID: "s", assistantMessageID: "m", streamManager: stream}
	for _, stage := range []string{"sources", "generation", "verification", "unrecognized"} {
		require.NoError(t, h.handleProgress(context.Background(), event.Event{ID: "stage", Data: event.AgentProgressData{Stage: stage, Iteration: 2}}))
	}
	require.Len(t, stream.events, 3)
	for _, evt := range stream.events {
		require.EqualValues(t, "progress", evt.Type)
		require.Empty(t, evt.Content)
		require.Equal(t, 2, evt.Data["iteration"])
	}
	require.Empty(t, h.finalAnswer)
}

func TestStructuredProgressUsesWrapperOperationWithoutChangingInnerArguments(t *testing.T) {
	streams := &capturingStreamManager{}
	h := &AgentStreamHandler{ctx: context.Background(), sessionID: "s", assistantMessageID: "m", streamManager: streams, eventStartTimes: make(map[string]time.Time)}
	require.NoError(t, h.handleToolCall(context.Background(), event.Event{ID: "call", Data: event.AgentToolCallData{
		ToolName: "structured_data", ToolCallID: "q", Operation: "query_series", Arguments: map[string]any{"operation": "rows"},
	}}))
	require.Equal(t, "query_series", streams.events[0].Data["operation"])
	require.Equal(t, map[string]any{"operation": "rows"}, streams.events[0].Data["arguments"])
}
