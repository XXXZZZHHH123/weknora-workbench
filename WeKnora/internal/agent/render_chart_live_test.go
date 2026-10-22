package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Opt-in paid chat acceptance. All source rows are isolated synthetic fixtures;
// no user document or image-generation provider is read or invoked.
func TestRenderChartLiveModel(t *testing.T) {
	path := os.Getenv("WEKNORA_CHART_LIVE_MODEL_FILE")
	if path == "" {
		t.Skip("set private development model inputs to run live chat-tool acceptance")
	}
	if keyPath := os.Getenv("WEKNORA_CHART_LIVE_KEY_FILE"); keyPath != "" {
		data, err := os.ReadFile(keyPath)
		require.NoError(t, err)
		var keys map[string]string
		require.NoError(t, json.Unmarshal(data, &keys))
		require.Len(t, keys["SYSTEM_AES_KEY"], 32)
		t.Setenv("SYSTEM_AES_KEY", keys["SYSTEM_AES_KEY"])
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw struct {
		ID, Name   string
		Type       types.ModelType
		Source     types.ModelSource
		Parameters json.RawMessage
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, types.ModelTypeKnowledgeQA, raw.Type)
	model := &types.Model{ID: raw.ID, Name: raw.Name, Type: raw.Type, Source: raw.Source}
	require.NoError(t, model.Parameters.Scan([]byte(raw.Parameters)))
	require.NotEmpty(t, model.Parameters.APIKey)
	client, err := chat.NewChat(chat.ConfigFromModel(model, "", ""), nil)
	require.NoError(t, err)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{queryChartFixture()}}}
	writer := &chartWriterFixture{}
	ctx, cancel := context.WithTimeout(chartContext(t), 90*time.Second)
	defer cancel()
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	messages := []chat.Message{
		{Role: "system", Content: "Fulfill the user's request using available tools. A source tool has already read an authorized synthetic data table. Inspect the chart source catalog before rendering. Do not invent data or fill gaps. " + dataChartInstructions},
		{Role: "user", Content: "请把已经读取的这组三个日期的观测数据绘成折线图。用PNG图片，只要图，不用解释。缺失值保持断点，不要改动原值。"},
	}
	thinking := false
	options := &chat.ChatOptions{Temperature: 0, MaxCompletionTokens: 2048, Thinking: &thinking, ReasoningEffort: api.ReasoningOff,
		Tools: []chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: tool.Name(), Description: tool.Description(), Parameters: tool.Parameters()}}}}
	finalAnswer := ""
	for round := 0; round < 5; round++ {
		response, err := client.Chat(ctx, messages, options)
		require.NoError(t, err)
		require.NotNil(t, response)
		if len(response.ToolCalls) == 0 {
			require.NotEmpty(t, writer.requests, "model answered without delivering a data chart")
			finalAnswer = strings.TrimSpace(response.Content)
			break
		}
		assistant := chat.Message{Role: "assistant", Content: response.Content, ReasoningContent: response.ReasoningContent, ReasoningSignature: response.ReasoningSignature, ReasoningMetadata: response.ReasoningMetadata}
		for _, call := range response.ToolCalls {
			assistant.ToolCalls = append(assistant.ToolCalls, chat.ToolCall{ID: call.ID, Type: "function", Function: chat.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		messages = append(messages, assistant)
		for _, call := range response.ToolCalls {
			require.Equal(t, tool.Name(), call.Function.Name)
			execCtx := agenttools.WithToolExecContext(ctx, &agenttools.ToolExecContext{SessionID: "session", AssistantMessageID: "message", ToolCallID: call.ID})
			result, err := tool.Execute(execCtx, json.RawMessage(call.Function.Arguments))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			body, err := json.Marshal(result)
			require.NoError(t, err)
			messages = append(messages, chat.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: string(body)})
			t.Logf("round=%d tool=%s saved=%d", round+1, call.Function.Name, len(writer.requests))
		}
		if len(writer.requests) > 0 {
			options.ToolChoice = "none"
		}
	}
	require.Len(t, writer.requests, 1)
	_, err = png.Decode(bytes.NewReader(writer.requests[0].PNG))
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^!\[[^\]\r\n]*\]\(resource://abcdefghijklmnopqrstuv\)$`), finalAnswer, "a chart-only request must finish with only the saved picture")
	t.Logf("final_answer=%s", finalAnswer)
	if output := os.Getenv("WEKNORA_CHART_LIVE_OUTPUT_FILE"); output != "" {
		require.NoError(t, os.WriteFile(output, writer.requests[0].PNG, 0o600))
	}
}
