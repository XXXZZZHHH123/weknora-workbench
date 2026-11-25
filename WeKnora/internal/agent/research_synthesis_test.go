package agent

import (
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestPeriodSynthesisRulesReachAnswerGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, excerpts, rule string
	}{
		{"four repeated issues", "第一期：运量预测维持。第二期：运量预测维持。第三期：运量预测维持。第四期：运量预测维持。", "相同表述合在一起，并说明出现在哪几期"},
		{"later revision", "第一期预测为80；第二期维持；第三期上调到90。", "按同一指标和口径写清调整前后的数值和方向，标明披露期次"},
		{"same revision reprinted", "第三期转述一次运量上调；第四期转载同一公告。", "多期转载同一次调整时合并说明，列出所见期次，只计为一次调整"},
		{"monthly and weekly reports", "第二期简报引用上月的运量月报，也引用当周的运输周报。", "分别说明月报、周报等不同类型的内容，区分材料本身的期次和它引用的报告期次"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{chatAnswer("材料支持按所给范围归纳。")}}
			engine := newTestEngine(t, model)
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.config.WorkbenchDefault = true
			engine.config.AnswerReviewMode = "standard"
			_, err := engine.Execute(t.Context(), "session", "message", "请综合以下材料。\n"+tc.excerpts, nil)
			require.NoError(t, err)
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, tc.excerpts)
			prompt := model.calls[0][0].Content
			require.Contains(t, prompt, "先按期次和报告类型整理已读的相关事实，区分相同表述与后期更新")
			require.Contains(t, prompt, tc.rule)
			require.Contains(t, prompt, "无需为凑齐期次追加读取")
			require.NotContains(t, researchSynthesisInstructions, tc.rule, "the shared source-review prompt does not impose this writing guidance")
		})
	}
}

func TestPeriodSynthesisKeepsOrdinaryDefinitionPath(t *testing.T) {
	const definition = "载重吨表示船舶可以搭载的总重量上限。"
	model := &taskPlannerChat{
		plan:     &types.ChatResponse{Content: sourceDomainPlan(t, "none", []string{}), FinishReason: "stop"},
		mockChat: mockChat{responses: []mockResponse{chatAnswer(definition)}},
	}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	engine.config.AnswerReviewMode = "standard"
	reader := newCountingTool(agenttools.ToolReadDocument)
	engine.toolRegistry.RegisterTool(reader)

	state, err := engine.Execute(t.Context(), "session", "message", "什么是载重吨？", nil)
	require.NoError(t, err)
	require.Equal(t, definition, state.FinalAnswer)
	require.Zero(t, reader.calls)
	require.Len(t, model.requests, 1, "only the existing task-planning call")
	require.Len(t, model.calls, 1, "direct draft and delivery with no additional review")
	require.Contains(t, model.calls[0][0].Content, "稳定概念可用可靠通用知识解释")
}
