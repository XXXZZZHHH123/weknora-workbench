package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

// Runtime rules also apply to agents saved before the structured protocol upgrade.
const structuredFieldTurnInstructions = `
结构化表格工具的当前规则：resolve_fields.question保留本轮用户原话，不追加上一轮日期、数值或猜测条件。连续追问通过prior_series_ids数组传递已确认实际series_id，context_sheet可用真实来源表；用户换主题或口径时重新判断。已确认字段的统计追问无需重复选择。“最近一天/最新一天”从原表取最新记录，不是今天；latest和未限定范围的统计不传date。date只填写用户明确的日期或可靠的单日追问上下文，不把系统当前日期当用户日期。
最高值、最低值、各自日期及差值是完整的复合统计任务；服务支持query.operation=statistics和各角色max/min的程序计算，必须完整回答，不能只返回最大值。
needs_clarification只表示真实未知信息；工具参数错误不得要求用户确认字段。返回确认请求后结束本轮等待用户，不擅自调用confirm_fields。返回完整answer后结束本轮，避免再次调用同一任务并改变答案。
`

const fieldConfirmationPrefix = "字段确认提交："

type structuredConfirmation struct {
	ResolutionID string            `json:"resolution_id"`
	SourceTool   string            `json:"source_tool,omitempty"`
	Selections   map[string]string `json:"selections"`
	Note         string            `json:"note"`
	Summary      string            `json:"summary,omitempty"`
}

func parseStructuredConfirmation(query string) (*structuredConfirmation, error) {
	if !strings.HasPrefix(query, fieldConfirmationPrefix) {
		return nil, nil
	}
	if len(query) > 16000 {
		return nil, fmt.Errorf("字段确认提交过长")
	}
	var submission structuredConfirmation
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(query, fieldConfirmationPrefix)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		return nil, fmt.Errorf("字段确认提交格式无效：需要结构化对象")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("字段确认提交包含多余内容")
	}
	if !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(submission.ResolutionID) || len(submission.Selections) > 8 || len([]rune(submission.Note)) > 1200 {
		return nil, fmt.Errorf("字段确认编号、选择或说明无效")
	}
	for role, id := range submission.Selections {
		if !regexp.MustCompile(`^v[1-8]$`).MatchString(role) || id == "" || len(id) > 300 {
			return nil, fmt.Errorf("字段选择无效")
		}
	}
	if len(submission.Selections) == 0 && strings.TrimSpace(submission.Note) == "" {
		return nil, fmt.Errorf("请选择字段或补充说明")
	}
	return &submission, nil
}

// The envelope is a direct human submission. Execute it once through the normal
// registry, permission checks and event pipeline; never ask an LLM to recopy IDs.
func (e *AgentEngine) executeStructuredConfirmation(ctx context.Context, state *types.AgentState, query, sessionID, messageID string) bool {
	submission, err := parseStructuredConfirmation(query)
	if submission == nil && err == nil {
		return false
	}
	started := time.Now()
	name := ""
	if err == nil {
		name, err = agenttools.StructuredConfirmationTool(ctx, e.toolRegistry, submission.SourceTool)
	}
	if err == nil {
		args, marshalErr := json.Marshal(map[string]any{"resolution_id": submission.ResolutionID, "selections": submission.Selections, "note": submission.Note})
		if marshalErr != nil {
			err = marshalErr
		} else {
			humanCtx := context.WithValue(ctx, fieldConfirmationHumanKey{}, true)
			call := e.runToolCall(humanCtx, types.LLMToolCall{ID: generateEventID("field-confirm"), Type: "function", Function: types.FunctionCall{Name: name, Arguments: string(args)}}, 0, 0, 1, sessionID, messageID)
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: 0, Timestamp: started, ToolCalls: []types.ToolCall{call}})
			e.emitToolOutcome(ctx, call, 0, sessionID)
			state.FinalAnswer = "已处理字段确认，请查看原表结果。"
			if call.Result == nil || !call.Result.Success {
				state.FinalAnswer = "字段确认未完成，请查看工具错误；不会重复提交同样的参数。"
			}
		}
	}
	if err != nil {
		state.FinalAnswer = err.Error()
	}
	state.IsComplete = true
	e.eventBus.Emit(ctx, event.Event{ID: generateEventID("answer"), Type: event.EventAgentFinalAnswer, SessionID: sessionID,
		Data: event.AgentFinalAnswerData{Content: state.FinalAnswer, Done: true}})
	e.emitCompletionEvent(ctx, state, sessionID, messageID, started)
	return true
}
