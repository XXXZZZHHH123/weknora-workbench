package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type exhaustedChartModel struct {
	chartAxisReviewModel
	synthesisDeadline time.Time
	cancelSynthesis   context.CancelFunc
}

func (m *exhaustedChartModel) ChatStream(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	if options != nil && strings.Contains(options.RuntimeNotice, "以下 JSON 是已核对的绘图数据") {
		m.synthesisDeadline, _ = ctx.Deadline()
		if m.cancelSynthesis != nil {
			m.cancelSynthesis()
		}
	}
	return m.mockChat.ChatStream(ctx, messages, options)
}

func TestExhaustedParentDeliversLastRoundChartAfterChildFailure(t *testing.T) {
	const (
		query   = "画月均收盘价并说明走势。"
		image   = "![图表](resource://abcdefghijklmnopqrstuv)"
		caption = "这份来源的有效记录截至2023年9月22日，2023年10—12月没有有效记录。"
		trend   = "月均收盘价先小幅上升，三月回落，随后逐月走高。"
	)
	for _, variant := range []string{"analysis", "remaining child", "no prose", "ungrounded prose", "provider error", "cancel after render", "cancel during synthesis", "stale receipt", "no result"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(chartContext(t))
			defer cancel()
			runtime, unowned := engineCollaboration(t)
			binding, err := runtime.AcquireParent(ctx, unowned.Scope)
			require.NoError(t, err)
			runtime.Run = func(context.Context, collaboration.Work) (collaboration.Result, error) {
				return collaboration.Result{}, errors.New("未能核对字段")
			}
			_, err = runtime.Spawn(binding.Context(ctx), binding.Scope, "spawn", "source_check", "template", "{}", "核对来源字段")
			require.NoError(t, err)
			runtime.Tick(ctx)
			require.Eventually(t, func() bool {
				members, err := binding.List(ctx, "")
				return err == nil && len(members) == 1 && members[0].Status == "failed"
			}, time.Second, time.Millisecond)
			if variant == "remaining child" {
				_, err = runtime.Spawn(binding.Context(ctx), binding.Scope, "other", "pending", "template", "{}", "继续查证")
				require.NoError(t, err)
			}

			source := monthlyChartSource()
			source.Result.Data["_structured_execution"] = agenttools.StructuredOperationDescriptor{
				Operation: "analyze_table", ToolName: source.Name, Generation: 1, ScopeMode: "knowledge_base", EffectClass: "read",
			}
			sourceJSON, err := json.Marshal(source.Result.Data)
			require.NoError(t, err)
			source.Result.Output = string(sourceJSON)
			tool, writer, state, args := monthlyRenderTool(t, ctx, source)
			args["delivery_mode"] = "chart_with_analysis"
			if variant == "no result" {
				args["source_id"] = "unknown-source"
			}
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			model := &exhaustedChartModel{chartAxisReviewModel: chartAxisReviewModel{response: &types.ChatResponse{
				Content:      `{"required_x_type":"category","assessment":"实际绘图行是同一指标的逐月记录。","chart_issues":[],"pending_deliverables":["说明走势"]}`,
				FinishReason: "stop",
			}}}
			model.responses = []mockResponse{{chunks: []types.StreamResponse{{
				Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{
					ID: "chart-call", Type: "function", Function: types.FunctionCall{Name: renderChartToolName, Arguments: string(raw)},
				}},
			}}}}
			switch variant {
			case "analysis", "remaining child", "cancel during synthesis":
				model.responses = append(model.responses, chatAnswer(trend), sourceAcceptedVerdict())
			case "no prose":
				model.responses = append(model.responses, chatAnswer(""))
			case "ungrounded prose":
				model.responses = append(model.responses, chatAnswer("月均收盘价升至999999999。"))
			}
			if variant == "cancel during synthesis" {
				model.cancelSynthesis = cancel
			}

			e := newTestEngine(t, model, withMaxIterations(1))
			e.config.WorkbenchDefault, e.config.TaskPlanningEnabled = true, true
			e.SetCollaboration(binding)
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(tool)
			tool.reviewPlan = e.chartPlanReviewer(query, state)
			messages := e.appendToolResults([]chat.Message{{Role: "user", Content: query}}, state.RoundSteps[0])
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			var completion event.AgentCompleteData
			e.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				completion = evt.Data.(event.AgentCompleteData)
				return nil
			})
			e.eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
				result := evt.Data.(event.AgentToolResultData)
				if result.ToolName == renderChartToolName && result.Success {
					switch variant {
					case "cancel after render":
						cancel()
					case "stale receipt":
						state.RoundSteps[0].ToolCalls[0].Result.Data["rows"].([][]any)[0][1] = 14000
					}
				}
				return nil
			})

			_, err = e.executeLoop(ctx, state, query, messages, nil, "session", "message")
			done, streamed := recorder.snapshot()
			if variant != "no result" {
				require.Len(t, writer.requests, 1)
				_, pngErr := png.Decode(bytes.NewReader(writer.requests[0].PNG))
				require.NoError(t, pngErr)
			} else {
				require.Empty(t, writer.requests)
			}
			switch variant {
			case "cancel after render", "cancel during synthesis", "stale receipt", "no result":
				switch variant {
				case "cancel after render", "cancel during synthesis":
					require.ErrorIs(t, err, context.Canceled)
				default:
					require.Error(t, err)
				}
				require.False(t, state.IsComplete)
				require.Empty(t, completion.FinalAnswer)
				require.Empty(t, streamed)
				require.Zero(t, done)
				if variant != "cancel during synthesis" {
					require.True(t, model.synthesisDeadline.IsZero(), "no text synthesis without current evidence and a live request")
				}
				return
			}
			require.NoError(t, err)
			require.True(t, state.IsComplete)
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, streamed)
			require.Equal(t, streamed, completion.FinalAnswer, "the persistence event carries the same answer as SSE")
			require.Equal(t, 1, strings.Count(streamed, image))
			require.Contains(t, streamed, caption)
			require.False(t, model.synthesisDeadline.IsZero())
			if variant == "no prose" || variant == "ungrounded prose" || variant == "provider error" {
				require.Contains(t, streamed, "文字分析未能完成。")
				require.NotContains(t, streamed, trend)
				require.NotContains(t, streamed, "999999999")
				require.LessOrEqual(t, len(model.calls), 2)
				return
			}
			require.Contains(t, streamed, trend)
			require.Len(t, model.calls, 3, "one render round, one synthesis and the ordinary answer review")
			require.Equal(t, "none", model.opts[1].ToolChoice)
			require.Empty(t, model.opts[1].Tools)
			require.Contains(t, model.opts[1].RuntimeNotice, `["2023-09","17280.625"]`)
			if variant == "remaining child" {
				members, readErr := binding.List(t.Context(), "")
				require.NoError(t, readErr)
				require.Equal(t, "cancelled", members[1].Status)
				require.Contains(t, model.calls[1][len(model.calls[1])-1].Content, "cancelled")
			}
		})
	}
}
