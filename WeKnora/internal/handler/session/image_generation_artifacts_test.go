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

type generatedArtifactMessageFixture struct{ message *types.Message }

func (f generatedArtifactMessageFixture) GetMessage(context.Context, string, string) (*types.Message, error) {
	return f.message, nil
}

func TestImageGenerationCompletionPublishesPersistedArtifactsWithoutSandbox(t *testing.T) {
	stream := &completionEventRecorder{}
	message := &types.Message{ID: "message", SessionID: "session"}
	h := NewAgentStreamHandler(context.Background(), "session", "message", "request", 7, time.Time{}, message, stream, event.NewEventBus(), nil, nil, nil)
	artifact := types.MessageArtifact{URL: "resource://abcdefghijklmnopqrstuv", FileName: "generated.png", FileType: ".png", FileSize: 72, SourcePath: "image-generation://execution"}
	h.SetArtifactMessageReader(generatedArtifactMessageFixture{message: &types.Message{ID: message.ID, SessionID: message.SessionID, Artifacts: types.MessageArtifacts{artifact}}})
	require.NoError(t, h.handleToolResult(context.Background(), event.Event{ID: "tool", Data: event.AgentToolResultData{ToolName: agenttools.ToolGenerateImage, Success: true}}))
	require.Len(t, message.Artifacts, 1, "a stop snapshot taken after delivery already includes its persistent artifact")
	require.NoError(t, h.handleComplete(context.Background(), event.Event{ID: "complete", Data: event.AgentCompleteData{MessageID: "message", FinalAnswer: "![Generated image](resource://abcdefghijklmnopqrstuv)"}}))
	require.Len(t, message.Artifacts, 1)
	require.NotNil(t, h.completionEvent)
	artifacts := h.completionEvent.Data["artifacts"].([]map[string]interface{})
	require.Len(t, artifacts, 1)
	require.Equal(t, "resource://abcdefghijklmnopqrstuv", artifacts[0]["handle"])
	require.Equal(t, 0, artifacts[0]["index"])
}
