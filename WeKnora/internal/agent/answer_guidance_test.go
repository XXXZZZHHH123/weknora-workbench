package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStandardAnswerGuidanceReachesPlannerAndExecutionWithoutChangingUserScope(t *testing.T) {
	const question = "请简要介绍这类活动的一般特点。"
	for _, mode := range []string{"standard", "strict", ""} {
		t.Run(mode, func(t *testing.T) {
			model := &taskPlannerChat{
				plan: &types.ChatResponse{Content: sourceDomainPlan(t, "none", []string{}), FinishReason: "stop"},
				mockChat: mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
					ResponseType: types.ResponseTypeAnswer, Content: "这里给出所需的一般介绍。", Done: true, FinishReason: "stop",
				}}}}},
			}
			engine := newTestEngine(t, model)
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
			engine.config.AnswerReviewMode = mode
			document := &SelectedDocumentInfo{KnowledgeID: "chosen-document", Title: "Chosen reference", FileType: "pdf"}
			engine.selectedDocs = []*SelectedDocumentInfo{document}
			memory := types.WrapMemoryForPrompt("Preference: use plain language.", "")
			engine.SetMemoryPrompt(memory)
			state, err := engine.Execute(t.Context(), "session", "message", question, nil)
			require.NoError(t, err)
			require.Equal(t, "这里给出所需的一般介绍。", state.FinalAnswer)
			require.Len(t, model.requests, 1)
			require.Len(t, model.calls, 1)
			for _, prompt := range []string{model.requests[0][0].Content, model.calls[0][0].Content} {
				if mode == "standard" {
					require.Equal(t, 1, strings.Count(prompt, standardAnswerGuidance), "planner and executor share exactly the same practical policy")
					require.Contains(t, prompt, "文件名措辞、上传顺序或数值接近程度不能证明权威或替代关系")
					require.Contains(t, prompt, "并列呈现有依据的差异并明确无法确定哪版优先")
					require.Contains(t, prompt, "字段或图题中的并列词可能是一个合并口径")
					require.Contains(t, prompt, "用户明确只用某表、某文件或已发布结构化数据时必须遵守")
					require.Contains(t, prompt, "不自行选择默认候选，也不因多候选就一律要求用户再选")
					require.Contains(t, prompt, "保留原始有效数值与样本")
				} else {
					require.NotContains(t, prompt, "<standard_answer_policy>", "strict and legacy prompts preserve their prior policy")
				}
			}
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.requests[0][1].Content), &input))
			require.Equal(t, question, input["current_user_question"])
			require.Contains(t, input["runtime_scope"], "<current_time>", "the clock remains available for actual relative-time questions")
			require.Contains(t, input["runtime_scope"], "Chosen reference")
			require.Contains(t, model.calls[0][1].Content, question)
			require.Equal(t, memory, engine.memoryPrompt, "the policy does not replace recalled memory")
			require.Contains(t, model.calls[0][0].Content, memory)
			require.Equal(t, []*SelectedDocumentInfo{document}, engine.selectedDocs)
			require.Equal(t, "chosen-document", document.KnowledgeID)
		})
	}
}
