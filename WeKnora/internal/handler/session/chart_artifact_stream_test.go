package session

import (
	"context"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChartArtifactPublishedAndAvailableToStopSnapshot(t *testing.T) {
	message := &types.Message{ID: "chart-message", SessionID: "chart-session"}
	h := NewAgentStreamHandler(context.Background(), message.SessionID, message.ID, "request", 7, time.Time{}, message, &completionEventRecorder{}, event.NewEventBus(), nil, nil, nil)
	artifact := types.MessageArtifact{URL: "resource://abcdefghijklmnopqrstuv", FileName: "chart.png", FileType: ".png", SourcePath: "data-chart://execution"}
	h.SetArtifactMessageReader(generatedArtifactMessageFixture{message: &types.Message{ID: message.ID, SessionID: message.SessionID, Artifacts: types.MessageArtifacts{artifact}}})
	require.NoError(t, h.handleToolResult(t.Context(), event.Event{ID: "chart-result", Data: event.AgentToolResultData{ToolName: agenttools.ToolRenderChart, Success: true}}))
	require.Equal(t, types.MessageArtifacts{artifact}, message.Artifacts)
	require.NoError(t, h.handleComplete(t.Context(), event.Event{ID: "chart-complete", Data: event.AgentCompleteData{MessageID: message.ID, FinalAnswer: "[Data chart](resource://abcdefghijklmnopqrstuv)"}}))
	require.NotNil(t, h.completionEvent)
	views := h.completionEvent.Data["artifacts"].([]map[string]interface{})
	require.Len(t, views, 1)
	require.Equal(t, ".png", views[0]["file_type"])
	require.Equal(t, "resource://abcdefghijklmnopqrstuv", views[0]["handle"])
}
