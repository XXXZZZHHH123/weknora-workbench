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

func TestAgentReplanningProgressForwardsRuntimeSnapshotWithoutAnswer(t *testing.T) {
	stream := &capturingStreamManager{}
	h := &AgentStreamHandler{ctx: context.Background(), sessionID: "s", assistantMessageID: "m", streamManager: stream}
	const notice = "读取和纠错暂未取得新证据，正在调整取证计划。"
	require.NoError(t, h.handleProgress(context.Background(), event.Event{ID: "source-replan", Data: event.AgentProgressData{
		Stage: "replanning", Content: notice, Iteration: 3,
	}}))
	require.Len(t, stream.events, 1)
	require.EqualValues(t, "progress", stream.events[0].Type)
	require.Equal(t, "source-replan", stream.events[0].Data["event_id"])
	require.Equal(t, "replanning", stream.events[0].Data["stage"])
	require.Equal(t, 3, stream.events[0].Data["iteration"])
	require.Equal(t, notice, stream.events[0].Content)
	require.True(t, stream.events[0].Done)
	require.Empty(t, h.finalAnswer)
	require.Empty(t, h.answerSegments)
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
