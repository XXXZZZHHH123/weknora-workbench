package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type generatedImageServiceFixture struct {
	request types.ImageGenerationRequest
	calls   int
	err     error
}

func (s *generatedImageServiceFixture) Generate(_ context.Context, request types.ImageGenerationRequest) (*types.ImageGenerationDelivery, error) {
	s.calls++
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	return &types.ImageGenerationDelivery{SessionID: request.SessionID, MessageID: request.MessageID, ModelName: "configured-image", Width: 3, Height: 2,
		Artifacts: []types.GeneratedImageArtifact{{Index: 0, Handle: "resource://abcdefghijklmnopqrstuv", FileName: "generated.png", FileType: ".png", FileSize: 72}}}, nil
}

func TestGenerateImageToolUsesExecutionIdentityAndSavedArtifacts(t *testing.T) {
	service := &generatedImageServiceFixture{}
	tool := NewGenerateImageTool(service, "configured-model-row")
	ctx := WithToolExecContext(context.Background(), &ToolExecContext{SessionID: "owned-session", AssistantMessageID: "assistant-message", ToolCallID: "provider-call"})
	result, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"A geometric composition","size":"1024x1024"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "configured-model-row", service.request.ModelID)
	require.Equal(t, "owned-session", service.request.SessionID)
	require.Equal(t, "assistant-message", service.request.MessageID)
	require.Equal(t, "provider-call", service.request.ToolCallID)
	require.Empty(t, result.Images, "image bytes must not be sent back into the chat model")
	require.Equal(t, "image_generation", result.Data["display_type"])
	require.Contains(t, result.Output, "resource://abcdefghijklmnopqrstuv")
	require.False(t, CanRunConcurrently(tool.Name()), "paid generation remains an execution barrier")
	stored := SanitizeToolDataForPersist(tool.Name(), result.Data)
	require.Contains(t, stored, "artifacts", "history replay retains downloadable artifact metadata")
}

func TestGenerateImageToolRejectsUntrustedExecutionIDsAndReportsFailure(t *testing.T) {
	service := &generatedImageServiceFixture{}
	tool := NewGenerateImageTool(service, "model")
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"test"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Zero(t, service.calls)
	ctx := WithToolExecContext(context.Background(), &ToolExecContext{SessionID: "s", AssistantMessageID: "m", ToolCallID: "call"})
	for _, raw := range []string{`{"prompt":"test","model_id":"untrusted"}`, `{"prompt":"test","session_id":"other"}`, `{"prompt":"test"}{}`} {
		result, err = tool.Execute(ctx, json.RawMessage(raw))
		require.NoError(t, err)
		require.False(t, result.Success)
	}
	require.Zero(t, service.calls)
	service.err = errors.New("image was generated but could not be saved")
	result, err = tool.Execute(ctx, json.RawMessage(`{"prompt":"test"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotContains(t, result.Data, "artifacts")
	require.Equal(t, "failed", result.Data["status"])
}
