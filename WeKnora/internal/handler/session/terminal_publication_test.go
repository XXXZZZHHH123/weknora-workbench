package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type terminalPlaceholderProvider struct{}

func (terminalPlaceholderProvider) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("unexpected non-streamed request")
}

func (terminalPlaceholderProvider) GetModelName() string { return "terminal-replay" }
func (terminalPlaceholderProvider) GetModelID() string   { return "terminal-replay" }

func (terminalPlaceholderProvider) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	chunks := make(chan types.StreamResponse, 2)
	chunks <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "模型未能完成回答核验。"}
	chunks <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "本次未返回完整结论，请稍后重试。", Done: true, FinishReason: "stop"}
	close(chunks)
	return chunks, nil
}

func TestEngineTerminalPublicationPersistsTheStreamedEvidenceGap(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	ctx := t.Context()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
	repo := repository.NewMessageRepository(db)
	message := &types.Message{SessionID: "session", Role: "assistant"}
	_, err = repo.CreateMessage(ctx, message)
	require.NoError(t, err)

	stream := &completionEventRecorder{}
	bus := event.NewEventBus()
	h := NewAgentStreamHandler(ctx, message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, bus, nil, nil, nil)
	h.Subscribe()
	engine := agent.NewAgentEngine(&types.AgentConfig{MaxIterations: 3}, terminalPlaceholderProvider{}, agenttools.NewToolRegistry(), bus, nil, nil, message.SessionID, "")
	state, err := engine.Execute(ctx, message.SessionID, message.ID, "Explain the observed result.", nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.NoError(t, repo.UpdateMessage(ctx, message))
	require.NoError(t, h.publishCompletion(ctx))
	reloaded, err := repo.GetMessage(ctx, message.SessionID, message.ID)
	require.NoError(t, err)

	var visible strings.Builder
	var closes int
	for _, item := range stream.events {
		if item.Type == types.ResponseTypeAnswer {
			visible.WriteString(item.Content)
			if item.Done {
				closes++
			}
		}
	}
	require.Equal(t, 1, closes)
	require.NotContains(t, visible.String(), "模型未能完成回答核验")
	require.Regexp(t, "仍待核实|remains unverified", visible.String())
	require.Equal(t, visible.String(), state.FinalAnswer)
	require.Equal(t, visible.String(), reloaded.Content)
	final := stream.events[len(stream.events)-1]
	require.Equal(t, types.ResponseTypeComplete, final.Type)
	require.Equal(t, reloaded.Content, final.Data["final_content"])
}
