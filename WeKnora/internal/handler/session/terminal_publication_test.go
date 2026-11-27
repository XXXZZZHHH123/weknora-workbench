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

type terminalReplayProvider struct {
	chunks []types.StreamResponse
}

func (terminalReplayProvider) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("unexpected non-streamed request")
}

func (terminalReplayProvider) GetModelName() string { return "terminal-replay" }
func (terminalReplayProvider) GetModelID() string   { return "terminal-replay" }

func (p terminalReplayProvider) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	chunks := make(chan types.StreamResponse, len(p.chunks))
	for _, chunk := range p.chunks {
		chunks <- chunk
	}
	close(chunks)
	return chunks, nil
}

func TestEngineTerminalPublicationPersistsTheStreamedEvidenceGap(t *testing.T) {
	for _, flag := range []string{"true", "false"} {
		for _, tc := range []struct {
			name, prefix, suffix, finish, want string
			absent                             string
		}{
			{"legacy placeholder", "模型未能完成回答核验。", "本次未返回完整结论，请稍后重试。", "stop", "", "模型未能完成回答核验"},
			{"empty draft", "", "", "stop", "", ""},
			{"partial protocol tag", "The stated conclusion is available.", "<task_cover", "length", "The stated conclusion is available.", "<task_"},
			{"exact text", "", `{"status":"ready"}`, "stop", `{"status":"ready"}`, ""},
		} {
			t.Run(flag+"/"+tc.name, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", flag)
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
				provider := terminalReplayProvider{chunks: []types.StreamResponse{
					{ResponseType: types.ResponseTypeAnswer, Content: tc.prefix},
					{ResponseType: types.ResponseTypeAnswer, Content: tc.suffix, Done: true, FinishReason: tc.finish},
				}}
				engine := agent.NewAgentEngine(&types.AgentConfig{MaxIterations: 3}, provider, agenttools.NewToolRegistry(), bus, nil, nil, message.SessionID, "")
				state, err := engine.Execute(ctx, message.SessionID, message.ID, "Explain the observed result.", nil)
				require.NoError(t, err)
				require.True(t, state.IsComplete)
				require.NoError(t, repo.UpdateMessage(ctx, message))
				require.NoError(t, h.publishCompletion(ctx))
				reloaded, err := repo.GetMessage(ctx, message.SessionID, message.ID)
				require.NoError(t, err)

				var visible strings.Builder
				var closes int
				first := ""
				for _, item := range stream.events {
					if item.Type != types.ResponseTypeAnswer {
						continue
					}
					if item.Data["replace"] == true {
						visible.Reset()
					}
					if first == "" && item.Content != "" {
						first = item.Content
					}
					visible.WriteString(item.Content)
					if item.Done {
						closes++
					}
				}
				require.Equal(t, 1, closes)
				if tc.absent != "" {
					require.NotContains(t, first, tc.absent)
					require.NotContains(t, visible.String(), tc.absent)
				}
				if tc.want != "" {
					require.Equal(t, tc.want, visible.String())
				} else {
					require.Equal(t, first, visible.String())
					require.Contains(t, visible.String(), "Explain the observed result.")
					require.Regexp(t, "没有取得可核实的来源回执|No verifiable source receipt was obtained", visible.String())
				}
				require.Equal(t, visible.String(), state.FinalAnswer)
				require.Equal(t, visible.String(), reloaded.Content)
				final := stream.events[len(stream.events)-1]
				require.Equal(t, types.ResponseTypeComplete, final.Type)
				require.Equal(t, reloaded.Content, final.Data["final_content"])
			})
		}
	}
}

func TestTerminalCorrectionBeforeCompletePersistsOnlyCurrentBytes(t *testing.T) {
	ctx := t.Context()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Message{}, &types.MessageArtifactRecord{}, &types.MessageEvidenceRecord{}))
	repo := repository.NewMessageRepository(db)
	message := &types.Message{SessionID: "session", Role: "assistant"}
	_, err = repo.CreateMessage(ctx, message)
	require.NoError(t, err)
	stream, bus := &completionEventRecorder{}, event.NewEventBus()
	h := NewAgentStreamHandler(ctx, message.SessionID, message.ID, "request", 7, time.Time{}, message, stream, bus, nil, nil, nil)
	h.Subscribe()

	const old = "The earlier reading was 47.25 from the former source."
	const current = "The earlier source changed before completion. The current source confirms the requested interval only."
	const answerID = "retained-answer"
	for _, data := range []event.AgentFinalAnswerData{{Content: old}, {Content: current, Replace: true}, {Done: true}} {
		require.NoError(t, bus.Emit(ctx, event.Event{ID: answerID, SessionID: message.SessionID, Type: event.EventAgentFinalAnswer, Data: data}))
	}
	require.NoError(t, bus.Emit(ctx, event.Event{ID: "complete", SessionID: message.SessionID, Type: event.EventAgentComplete,
		Data: event.AgentCompleteData{MessageID: message.ID, FinalAnswer: current, EvidenceRefs: []interface{}{}, KnowledgeRefs: []interface{}{}}}))
	require.NoError(t, repo.UpdateMessage(ctx, message))
	require.NoError(t, h.publishCompletion(ctx))
	reloaded, err := repo.GetMessage(ctx, message.SessionID, message.ID)
	require.NoError(t, err)
	require.Equal(t, current, reloaded.Content)
	require.Empty(t, reloaded.EvidenceReferences)
	require.Empty(t, reloaded.KnowledgeReferences)
	require.Equal(t, current, h.finalAnswer)

	answers := 0
	for _, item := range stream.events {
		switch item.Type {
		case types.ResponseTypeAnswer:
			answers++
			require.Equal(t, answerID, item.ID)
			if item.Data["replace"] == true {
				require.Equal(t, current, item.Content)
			}
		case types.ResponseTypeComplete:
			require.Equal(t, current, item.Data["final_content"])
		}
	}
	require.Equal(t, 3, answers)
}
