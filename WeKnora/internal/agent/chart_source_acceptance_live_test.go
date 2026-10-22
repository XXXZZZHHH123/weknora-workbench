package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// These tests never create a database row. Only the two explicitly opted-in
// live cases use a provider, with the same encrypted development chat config.
// The mixed-digit resource identity deliberately exercises numeric validation.
const chartSourceAcceptanceHandle = "resource://A1b2C3d4E5f6G7h8I9j0Kl"

func chartSourceAcceptanceAnalysis(large bool) types.ToolCall {
	coverage := map[string]any{"expected_count": 5, "observed_count": 4}
	if large {
		// A real-shaped audit field exceeded the old 16 KiB provenance budget.
		coverage["observed_dates"] = strings.Repeat("synthetic coverage audit;", 900)
	}
	data := map[string]any{
		"kind": "structured_analysis", "status": "ok", "dataset_id": "synthetic-dataset",
		"result_id": "synthetic-analysis", "source": map[string]any{"file": "synthetic-observations.xlsx", "sha256": strings.Repeat("ab", 32)},
		"columns": []string{"date", "reading"},
		"rows":    [][]any{{"2025-01-01", 12.0}, {"2025-01-02", 18.0}, {"2025-01-04", nil}, {"2025-01-07", 15.0}, {"2025-01-10", 9.0}},
		"inputs": map[string]any{"synthetic-series": map[string]any{
			"series":      map[string]any{"series_id": "synthetic-series", "sheet": "observations", "metric": "reading", "unit": "kg"},
			"input_count": 5, "valid_count": 4, "actual_first_date": "2025-01-01", "actual_last_date": "2025-01-10", "observation_coverage": coverage,
		}},
		"sql": "SELECT date, reading FROM synthetic_observations ORDER BY date", "input_count": 5,
		"requested_range":    map[string]any{"from": "2025-01-01", "to": "2025-01-10"},
		"returned_row_count": 5, "input_complete": true, "output_truncated": false,
		"notice": "Isolated synthetic observations. Null is a missing reading, never a zero or an interpolated observation.",
	}
	output, _ := json.Marshal(data) // Constant synthetic primitives are JSON-safe.
	return types.ToolCall{ID: "synthetic-analysis-call", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: data, Output: string(output)}}
}

type chartSourceAcceptanceTool struct{ agenttools.BaseTool }

func (tool *chartSourceAcceptanceTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return chartSourceAcceptanceAnalysis(false).Result, nil
}

type chartSourceAcceptanceWriter struct{ requests []chart.SaveRequest }

func (writer *chartSourceAcceptanceWriter) SaveChart(_ context.Context, req chart.SaveRequest) (*chart.Delivery, error) {
	writer.requests = append(writer.requests, req)
	digest := sha256.Sum256(req.PNG)
	return &chart.Delivery{Handle: chartSourceAcceptanceHandle, Index: 0, FileName: "chart-" + hex.EncodeToString(digest[:8]) + ".png"}, nil
}

func chartSourceAcceptanceEngine(t *testing.T, client chat.Chat, writer *chartSourceAcceptanceWriter) *AgentEngine {
	t.Helper()
	e := newTestEngine(t, client)
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
	e.config.Temperature, e.config.MaxIterations = 0, 10
	e.config.MaxContextTokens, e.config.MaxCompletionTokens, e.config.LLMCallTimeout = 131072, 4096, 180
	thinking := false
	e.config.Thinking, e.config.ReasoningEffort = &thinking, "off"
	e.config.ReviewReasoningEffort, e.config.SourceReviewReasoningEffort = "minimal", "auto"
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(&chartSourceAcceptanceTool{BaseTool: agenttools.NewBaseTool("structured_analyze_table",
		"Read the isolated synthetic observation table already authorized for this task. Returns exact dates, readings, units, source identity and missing observations. No arguments are required; this tool cannot access any other data.",
		json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`))})
	e.SetChartWriter(writer)
	return e
}

const chartSourceAcceptanceQuestion = "请先读取已授权的合成观测表，综合比较前段和后段的变化，解释缺失观测对趋势判断的限制，再绘制按真实日期间隔的PNG折线图。不要补齐缺失值。回复用简短中文结论和实际保存图片的Markdown，不能虚构下载地址。"

// This proves execution/receipt wiring only. Semantic acceptance is established
// separately by TestChartSourceLiveSmartEngine's unmodified real model verdict.
func TestChartSourceEngineAcceptanceWiring(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "critic_repair"}[repair], func(t *testing.T) {
			call := chartSourceAcceptanceAnalysis(false)
			tables, _ := chartSources([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}})
			require.Len(t, tables, 1)
			args, err := json.Marshal(map[string]any{"operation": "render", "source_id": tables[0].ID, "x_column": 0, "y_columns": []int{1}, "chart_type": "line", "x_type": "time", "delivery_mode": "chart_with_analysis"})
			require.NoError(t, err)
			plan, err := json.Marshal(taskEvidencePlan{Goals: []string{"Compare the observations and deliver the actual chart."}, Evidence: []string{"Exact synthetic observation rows."}, ToolNames: []string{"structured_analyze_table", "render_chart"}, CompletionCriteria: []string{"Ground the interpretation and image in the observed rows."}, ReasoningDepth: "synthesis"})
			require.NoError(t, err)
			responses := []mockResponse{
				chatToolCall(call.ID, call.Name, `{}`),
				chatToolCall("chart-inspect", "render_chart", `{"operation":"inspect"}`),
				chatToolCall("chart-render", "render_chart", string(args)),
				chatAnswer("观测先升后降，缺失处不能推断连续走势。\n\n![图表](res://0001)"),
			}
			if repair {
				responses = append(responses, sourceConsistencyVerdict(`{"complete":false,"feedback":"请明确只描述已观测的前后两段，缺失区间不推断。","user_notice":"请核对缺失观测的限制说明。","next_action":"revise_answer"}`),
					chatAnswer("已观测的前段上升、后段下降；缺失区间无法判断连续走势。\n\n![图表](res://0001)"))
			}
			responses = append(responses, sourceAcceptedVerdict())
			model := &taskPlannerChat{plan: &types.ChatResponse{Content: string(plan), FinishReason: "stop"}, mockChat: mockChat{responses: responses}}
			writer := &chartSourceAcceptanceWriter{}
			e := chartSourceAcceptanceEngine(t, model, writer)
			snapshot := chartSourceObserveTerminalTask(e)
			state, err := e.Execute(chartContext(t), "session", "message", chartSourceAcceptanceQuestion, nil)
			require.NoError(t, err)
			chartSourceAcceptanceAssert(t, e, state, writer, snapshot())
			if repair {
				require.Equal(t, 2, e.turnReview.Checks)
				require.Equal(t, 1, e.turnReview.Repairs)
			}
		})
	}
}

// Observe the real task before executeLoop's defer releases its turn-local
// conversation. A repaired generation transcript can contain a user-role JSON
// evidence packet; that is not the original human-task snapshot.
func chartSourceObserveTerminalTask(e *AgentEngine) func() []chat.Message {
	var mu sync.Mutex
	var snapshot []chat.Message
	e.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if ok && data.Done && e.turnReview.Committed {
			mu.Lock()
			snapshot = e.semanticReviewConversation(e.turnReview.Conversation)
			mu.Unlock()
		}
		return nil
	})
	return func() []chat.Message {
		mu.Lock()
		defer mu.Unlock()
		return append([]chat.Message(nil), snapshot...)
	}
}

func chartSourceAcceptanceAssert(t *testing.T, e *AgentEngine, state *types.AgentState, writer *chartSourceAcceptanceWriter, taskSnapshot []chat.Message) {
	t.Helper()
	require.NotNil(t, state)
	require.True(t, state.IsComplete)
	require.Equal(t, "synthesis", e.taskReasoningDepth(), "a direct-task verdict does not exercise the independent source critic")
	require.Len(t, writer.requests, 1)
	_, err := png.Decode(bytes.NewReader(writer.requests[0].PNG))
	require.NoError(t, err)
	require.Contains(t, state.FinalAnswer, "]("+chartSourceAcceptanceHandle+")")
	require.NotContains(t, state.FinalAnswer, "sandbox:")
	require.NotContains(t, state.FinalAnswer, "未能完成自动核验")
	require.Greater(t, e.turnReview.Checks, 0)
	require.Empty(t, e.turnReview.Last.Unsupported)
	require.Equal(t, candidateAccepted, e.turnReview.Last.Disposition)
	require.NotEmpty(t, e.turnReview.Last.NumericFeedback, "actual successful structured data must enter the numeric guard")
	require.NotEmpty(t, e.turnReview.Last.CandidateHash)
	require.NotEmpty(t, e.turnReview.Last.TaskHash)
	require.NotEmpty(t, e.turnReview.Last.EvidenceHash)
	// The observer retains the actual human snapshot before deferred cleanup;
	// no generation/repair packet is reclassified as a human instruction.
	require.True(t, e.turnReview.Committed)
	require.NotEmpty(t, taskSnapshot)
	require.Equal(t, candidateTextDigest(state.FinalAnswer), e.turnReview.Last.CandidateHash, "the delivered answer must be the exact accepted candidate, not a fallback or a later draft")
	require.Equal(t, reviewDigest(struct {
		Query        string
		Conversation []chat.Message
	}{e.turnReview.Query, taskSnapshot}), e.turnReview.Last.TaskHash)
	require.Equal(t, reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation)), e.turnReview.Last.EvidenceHash)
	operations := map[string]bool{}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Result != nil && call.Result.Success {
				if call.ExecutionName() == "structured_analyze_table" {
					operations["analysis"] = true
				}
				if call.ExecutionName() == "render_chart" {
					operation, _ := call.Args["operation"].(string)
					operations[operation] = true
				}
			}
		}
	}
	require.True(t, operations["analysis"] && operations["inspect"] && operations["render"], "must execute the full analysis → inspect → render path")
}

type chartSourceObservedCall struct {
	Method       string              `json:"method"`
	Messages     []chat.Message      `json:"messages"`
	Content      string              `json:"content"`
	ToolCalls    []types.LLMToolCall `json:"tool_calls,omitempty"`
	FinishReason string              `json:"finish_reason,omitempty"`
	Critic       bool                `json:"critic"`
}

// Observation only: request/response evidence is synthetic; no model config,
// headers or credentials enter these records. Never rewrite a model verdict.
type chartSourceObservedChat struct {
	inner chat.Chat
	mu    sync.Mutex
	calls []chartSourceObservedCall
}

func (client *chartSourceObservedChat) GetModelName() string { return client.inner.GetModelName() }
func (client *chartSourceObservedChat) GetModelID() string   { return client.inner.GetModelID() }

func (client *chartSourceObservedChat) record(method string, messages []chat.Message) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := chartSourceObservedCall{Method: method, Messages: append([]chat.Message(nil), messages...)}
	for _, message := range messages {
		var input map[string]any
		if message.Role == "user" && json.Unmarshal([]byte(message.Content), &input) == nil && input["candidate_answer"] != nil && input["source_tool_results"] != nil {
			call.Critic = true
		}
	}
	client.calls = append(client.calls, call)
	return len(client.calls) - 1
}

func (client *chartSourceObservedChat) Chat(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	index := client.record("chat", messages)
	response, err := client.inner.Chat(ctx, messages, opts)
	if err != nil {
		return nil, fmt.Errorf("live chat failed (%T)", err)
	}
	if response != nil {
		client.mu.Lock()
		client.calls[index].Content, client.calls[index].ToolCalls, client.calls[index].FinishReason = response.Content, response.ToolCalls, response.FinishReason
		client.mu.Unlock()
	}
	return response, nil
}

func (client *chartSourceObservedChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	index := client.record("stream", messages)
	stream, err := client.inner.ChatStream(ctx, messages, opts)
	if err != nil {
		return nil, fmt.Errorf("live stream failed (%T)", err)
	}
	out := make(chan types.StreamResponse)
	go func() {
		defer close(out)
		for chunk := range stream {
			client.mu.Lock()
			client.calls[index].Content += chunk.Content
			client.calls[index].ToolCalls = append(client.calls[index].ToolCalls, chunk.ToolCalls...)
			if chunk.FinishReason != "" {
				client.calls[index].FinishReason = chunk.FinishReason
			}
			client.mu.Unlock()
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func chartSourceLiveClient(t *testing.T) *chartSourceObservedChat {
	t.Helper()
	path := os.Getenv("WEKNORA_CHART_SOURCE_MODEL_FILE")
	if path == "" {
		t.Skip("requires explicit private development-model opt-in; makes paid chat calls")
	}
	read := func(path string) []byte {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Zero(t, info.Mode().Perm()&0o077, "private model inputs must not be group/world readable")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return data
	}
	var keys map[string]string
	require.NoError(t, json.Unmarshal(read(os.Getenv("WEKNORA_CHART_SOURCE_KEY_FILE")), &keys))
	require.Len(t, keys["SYSTEM_AES_KEY"], 32)
	t.Setenv("SYSTEM_AES_KEY", keys["SYSTEM_AES_KEY"])
	var raw struct {
		ID, Name   string
		Type       types.ModelType
		Source     types.ModelSource
		Parameters json.RawMessage
	}
	require.NoError(t, json.Unmarshal(read(path), &raw))
	require.Equal(t, types.ModelTypeKnowledgeQA, raw.Type)
	model := &types.Model{ID: raw.ID, Name: raw.Name, Type: raw.Type, Source: raw.Source}
	require.NoError(t, model.Parameters.Scan([]byte(raw.Parameters)))
	client, err := chat.NewChat(chat.ConfigFromModel(model, "", ""), nil)
	require.NoError(t, err)
	t.Logf("configured_model_id=%s configured_model_name=%s", model.ID, model.Name)
	return &chartSourceObservedChat{inner: client}
}

func chartSourceWriteEvidence(t *testing.T, label string, client *chartSourceObservedChat, writer *chartSourceAcceptanceWriter, extra any) {
	t.Helper()
	dir := os.Getenv("WEKNORA_CHART_SOURCE_OUTPUT_DIR")
	if dir == "" {
		return
	}
	require.NoError(t, os.MkdirAll(dir, 0o700))
	client.mu.Lock()
	data, err := json.MarshalIndent(map[string]any{"calls": client.calls, "result": extra}, "", "  ")
	client.mu.Unlock()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, label+"-trace.json"), data, 0o600))
	for i, request := range writer.requests {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.png", label, i)), request.PNG, 0o600))
	}
}

func TestChartSourceLiveQuickAnalysis(t *testing.T) {
	client := chartSourceLiveClient(t)
	writer := &chartSourceAcceptanceWriter{}
	defer func() { chartSourceWriteEvidence(t, "quick", client, writer, nil) }()
	call := chartSourceAcceptanceAnalysis(true)
	inputs, err := json.Marshal(call.Result.Data["inputs"])
	require.NoError(t, err)
	require.Greater(t, len(inputs), 16384)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	tables, omitted := chartSources(steps)
	require.Len(t, tables, 1, "real-shaped large analysis audit must not silently erase an otherwise valid table")
	require.Zero(t, omitted)
	ctx, cancel := context.WithTimeout(chartContext(t), 180*time.Second)
	defer cancel()
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	messages := []chat.Message{{Role: "system", Content: "An authorized structured_analyze_table call has already succeeded. Its exact source rows are available through render_chart inspect. Use that catalog, preserve missing values, and deliver the saved PNG. " + dataChartInstructions}, {Role: "user", Content: "请把已经读取的合成观测表绘成按实际日期间隔的折线PNG。缺失值保持断点，只要图片，不需要解释。"}}
	thinking := false
	opts := &chat.ChatOptions{Temperature: 0, MaxCompletionTokens: 2048, Thinking: &thinking, ReasoningEffort: api.ReasoningOff, Tools: []chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: tool.Name(), Description: tool.Description(), Parameters: tool.Parameters()}}}}
	operations := []string{}
	final := ""
	for round := 0; round < 5; round++ {
		response, err := client.Chat(ctx, messages, opts)
		require.NoError(t, err)
		require.NotNil(t, response)
		if len(response.ToolCalls) == 0 {
			final = strings.TrimSpace(response.Content)
			break
		}
		assistant := chat.Message{Role: "assistant", Content: response.Content, ReasoningContent: response.ReasoningContent, ReasoningSignature: response.ReasoningSignature, ReasoningMetadata: response.ReasoningMetadata}
		for _, call := range response.ToolCalls {
			assistant.ToolCalls = append(assistant.ToolCalls, chat.ToolCall{ID: call.ID, Type: "function", Function: chat.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		messages = append(messages, assistant)
		for _, call := range response.ToolCalls {
			require.Equal(t, tool.Name(), call.Function.Name)
			var args map[string]any
			require.NoError(t, json.Unmarshal([]byte(call.Function.Arguments), &args))
			operation, _ := args["operation"].(string)
			operations = append(operations, operation)
			execCtx := agenttools.WithToolExecContext(ctx, &agenttools.ToolExecContext{SessionID: "session", AssistantMessageID: "message", ToolCallID: call.ID})
			result, err := tool.Execute(execCtx, json.RawMessage(call.Function.Arguments))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			body, err := json.Marshal(result)
			require.NoError(t, err)
			messages = append(messages, chat.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: string(body)})
		}
		if len(writer.requests) > 0 {
			opts.ToolChoice = "none"
		}
	}
	require.GreaterOrEqual(t, len(operations), 2)
	require.Equal(t, "inspect", operations[0])
	require.Contains(t, operations, "render")
	require.Len(t, writer.requests, 1)
	_, err = png.Decode(bytes.NewReader(writer.requests[0].PNG))
	require.NoError(t, err)
	require.Regexp(t, `^!\[[^\]\r\n]*\]\(resource://A1b2C3d4E5f6G7h8I9j0Kl\)$`, final)
	t.Logf("large_inputs_bytes=%d operations=%v png_bytes=%d", len(inputs), operations, len(writer.requests[0].PNG))
}

func TestChartSourceLiveSmartEngine(t *testing.T) {
	client := chartSourceLiveClient(t)
	writer := &chartSourceAcceptanceWriter{}
	e := chartSourceAcceptanceEngine(t, client, writer)
	snapshot := chartSourceObserveTerminalTask(e)
	var state *types.AgentState
	defer func() {
		chartSourceWriteEvidence(t, "smart", client, writer, map[string]any{"state": state, "review": e.turnReview, "plan": e.taskPlan})
	}()
	ctx, cancel := context.WithTimeout(chartContext(t), 300*time.Second)
	defer cancel()
	var err error
	state, err = e.Execute(ctx, "session", "message", chartSourceAcceptanceQuestion, nil)
	require.NoError(t, err)
	chartSourceAcceptanceAssert(t, e, state, writer, snapshot())
	criticCalls := 0
	for _, call := range client.calls {
		if call.Critic {
			criticCalls++
		}
	}
	require.Greater(t, criticCalls, 0, "the real source critic must receive the actual candidate and immutable tool evidence")
	t.Logf("planner_depth=%s critic_calls=%d candidate_checks=%d disposition=%s png_bytes=%d final_answer=%s", e.taskReasoningDepth(), criticCalls, e.turnReview.Checks, e.turnReview.Last.Disposition, len(writer.requests[0].PNG), state.FinalAnswer)
}
