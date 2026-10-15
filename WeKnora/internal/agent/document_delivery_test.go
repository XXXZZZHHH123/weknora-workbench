package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFinalSynthesisRejectsToolRequestsInsteadOfPublishingProgress(t *testing.T) {
	model := &mockChat{}
	for range 3 {
		model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{
			Content:   "I'll verify the remaining details before finalizing.",
			ToolCalls: []types.LLMToolCall{{ID: "unfinished", Function: types.FunctionCall{Name: "unexecuted_tool", Arguments: "{}"}}},
			Done:      true, FinishReason: "tool_calls",
		}}})
	}
	engine := newTestEngine(t, model)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	state := &types.AgentState{}
	require.NoError(t, engine.streamFinalAnswerToEventBus(context.Background(), "完成先前任务", state, "s", emptyMessages()))
	require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, incompleteAnswerFallback, visible)
	require.Len(t, model.calls, 3)
}

func TestFinalSynthesisSemanticallyRejectsLongProgressUpdates(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{Content: "我会继续核对并随后为你整理，请稍候。", Done: true, FinishReason: "stop"}}},
		completenessVerdict(false),
		{chunks: []types.StreamResponse{{Content: "本次没有生成文件，因为所需执行环境不可用。", Done: true, FinishReason: "stop"}}},
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	state := &types.AgentState{}
	require.NoError(t, engine.streamFinalAnswerToEventBus(context.Background(), "整理成附件", state, "s", []chat.Message{{Role: "user", Content: "将上文整理成文件"}}))
	require.Equal(t, "本次没有生成文件，因为所需执行环境不可用。", state.FinalAnswer)
	require.Len(t, model.calls, 4)
}

func TestWorkspaceCompletionUsesActualExecutionEvidenceAndContext(t *testing.T) {
	model := &mockChat{responses: []mockResponse{completenessVerdict(false)}}
	engine := newTestEngine(t, model)
	engine.config.SandboxConfigID = "selected-by-admin"
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "shell_exec", Result: &types.ToolResult{Success: true, Output: `{"exit_code":1,"stderr":"conversion failed"}`}}}}}}
	repair, err := engine.answerNeedsRepair(context.Background(), "继续上一个任务", "已经完成并生成了可下载的附件。", "", state, []chat.Message{{Role: "user", Content: "请制作可下载的文件并保留来源"}})
	require.NoError(t, err)
	require.True(t, repair)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	require.Contains(t, model.calls[0][1].Content, "conversion failed")
	require.Contains(t, model.calls[0][1].Content, "保留来源")
	require.NotEmpty(t, input["execution_evidence"])
}

func TestSandboxAnswerIsBufferedUntilCompletionReview(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{Content: "我已完成文件生成，文件可以下载。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.config.SandboxConfigID = "selected-by-admin"
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), nil, 0, "s")
	require.NoError(t, err)
	require.False(t, response.AnswerStreamed)
	done, visible := recorder.snapshot()
	require.Zero(t, done)
	require.Empty(t, visible)
}
