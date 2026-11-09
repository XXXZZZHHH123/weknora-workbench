package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// The planner interprets the full human request. Tool names and answer words
// never decide what the user requested; actual executions only prevent an
// action from bypassing the ordinary completion check.
func (e *AgentEngine) nonTextDeliveryRequirements() []taskDeliveryRequirement {
	if e == nil || e.taskPlan == nil {
		return nil
	}
	var requirements []taskDeliveryRequirement
	for _, requirement := range e.taskPlan.DeliveryRequirements {
		if requirement.Kind != "text" {
			requirements = append(requirements, requirement)
		}
	}
	return requirements
}

func chartDeliveryExecution(call types.ToolCall) bool {
	if call.ExecutionName() != renderChartToolName {
		return false
	}
	if call.ExecutionArgs()["operation"] == "render" {
		return true
	}
	return call.Result != nil && call.Result.Data["kind"] == "data_chart"
}

func hasArtifactExecution(state *types.AgentState) bool {
	if state == nil {
		return false
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if chartDeliveryExecution(call) {
				return true
			}
			switch call.ExecutionName() {
			case agenttools.ToolGenerateImage, agenttools.ToolWriteSandboxFile, agenttools.ToolEditSandboxFile:
				return true
			}
		}
	}
	return false
}

func (e *AgentEngine) needsDeliveryReview(state *types.AgentState) bool {
	return len(e.nonTextDeliveryRequirements()) > 0 || hasArtifactExecution(state)
}

func (e *AgentEngine) hasExplicitTextDeliveryPlan() bool {
	if e == nil || e.taskPlan == nil || len(e.taskPlan.DeliveryRequirements) == 0 {
		return false
	}
	for _, requirement := range e.taskPlan.DeliveryRequirements {
		if requirement.Kind != "text" {
			return false
		}
	}
	return true
}

func (e *AgentEngine) candidateTaskDigest(query string, conversation []chat.Message) string {
	// Bind all review/reuse paths, including chart receipts and warning
	// delivery, to the same complete task contract and runtime source scope.
	// This packet contains only existing non-secret routing and permission
	// metadata; credentials and provider configuration never enter it.
	var scope any
	if e.config != nil {
		scope = struct {
			KnowledgeBases            []string
			KnowledgeIDs              []string
			SearchTargets             types.SearchTargets
			QuestionOrigin            *types.QuestionOrigin
			PageContext               *types.PageContext
			SharedAgentReadOnly       bool
			CitationsEnabled          bool
			EvidenceReferencesEnabled bool
		}{e.config.KnowledgeBases, e.config.KnowledgeIDs, e.config.SearchTargets,
			e.config.QuestionOrigin, e.config.PageContext, e.config.SharedAgentReadOnly,
			e.config.CitationsEnabled(), e.config.EvidenceReferencesEnabled}
	}
	return reviewDigest(struct {
		Query        string
		Conversation []chat.Message
		Plan         map[string]any
		Scope        any
	}{query, e.semanticReviewConversation(conversation), e.taskReviewPlan(), scope})
}

type answerDeliveryCheck struct {
	Status     string `json:"status"`
	Assessment string `json:"assessment"`
}

var answerDeliveryCheckSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["status","assessment"],"properties":{"status":{"type":"string","enum":["delivered","blocked","missing"]},"assessment":{"type":"string","minLength":1,"maxLength":900}}}`)

// A delivery check is required only for an explicit model-produced non-text
// contract. Legacy plans keep their protocol, while actual artifact execution
// still receives the same semantic review and chart evidence companion.
func (e *AgentEngine) withDeliveryCheckSchema(schema json.RawMessage) json.RawMessage {
	if len(e.nonTextDeliveryRequirements()) == 0 {
		return schema
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(schema, &object) != nil {
		return schema
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(object["properties"], &properties) != nil {
		return schema
	}
	properties["delivery_check"] = answerDeliveryCheckSchema
	object["properties"], _ = json.Marshal(properties)
	var required []string
	_ = json.Unmarshal(object["required"], &required)
	required = append(required, "delivery_check")
	object["required"], _ = json.Marshal(required)
	encoded, err := json.Marshal(object)
	if err != nil {
		return schema
	}
	return encoded
}

// This checks execution identity, not business intent or semantic correctness.
// The model separately judges the requested dimensions, units and coverage.
func chartIncludedInCandidate(candidate string, state *types.AgentState) bool {
	if state == nil {
		return false
	}
	images := visibleChartImageHandles(candidate)
	if len(images) == 0 {
		return false
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if !chartDeliveryExecution(call) || call.Result == nil || !call.Result.Success || call.Result.Data["kind"] != "data_chart" || call.Result.Data["status"] != "ok" {
				continue
			}
			// Live delivery is typed; persisted tool results decode into maps.
			encoded, err := json.Marshal(call.Result.Data["artifact"])
			if err != nil {
				continue
			}
			var artifact struct {
				Handle string `json:"handle"`
			}
			if json.Unmarshal(encoded, &artifact) != nil {
				continue
			}
			handle, valid := types.ParseResourcePath(artifact.Handle)
			if valid && images[handle] {
				return true
			}
		}
	}
	return false
}

func (e *AgentEngine) validateDeliveryCheck(check *answerDeliveryCheck, candidate string, state *types.AgentState) (bool, error) {
	if len(e.nonTextDeliveryRequirements()) == 0 {
		return false, nil
	}
	if check == nil || strings.TrimSpace(check.Assessment) == "" {
		return false, fmt.Errorf("non-text delivery review requires delivery_check with status and assessment")
	}
	switch check.Status {
	case "blocked":
		// The independent reviewer must verify that the actual candidate plainly
		// states the unresolved blocker; program text matching cannot judge it.
		return false, nil
	case "missing":
		e.answerReviewFeedback = "答复尚未交付用户要求的结果，也未诚实说明实际未完成的部分。请按完整任务和真实执行证据补全。"
		e.answerReviewAction = reviewReviseAnswer
		return true, nil
	case "delivered":
		for _, requirement := range e.nonTextDeliveryRequirements() {
			if requirement.Kind == "chart" && !chartIncludedInCandidate(candidate, state) {
				e.answerReviewFeedback = "用户要求的图尚未包含在答复中。使用本轮真实成功绘图结果交付图片；未成功时明确说明未完成，不能以数据表代替。"
				e.answerReviewAction = reviewReviseAnswer
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("delivery_check status must be delivered, blocked or missing")
	}
}

const artifactDeliveryReviewInstructions = `Check the requested deliverables separately from factual prose. delivery_requirements is the planner's interpretation, not new user authority: resolve each requirement against the full current question and conversation. A requested chart is not delivered by a table, a filename, a promise, or a successful unrelated image. Inspect chart_delivery_companion and its exact successful result: match the actual chart_spec, selected_columns, ALL selected_rows, source_mapping, units and coverage to the requested metric, dimensions, ordering and scope. Technical row IDs cannot stand in for the requested dimension merely because they are accepted as categories. Check horizontal versus vertical tables: rows containing dimension labels and rows containing measurements must not be plotted as two observations of one metric. A semantic_validation receipt is evidence of a separate check, not permission to ignore the actual candidate or selected data. Restore any relevant omitted result paths with the registered reader. Do not require a second image rendering or invent new values.
If the schema requires delivery_check, return it with status=delivered only when every requested non-text result is actually included and semantically correct; status=blocked only when the candidate truthfully states which requested result remains unfinished and its actual bounded blocker; status=missing when the result is omitted, replaced with another format, or only promised. assessment briefly identifies the checked mapping/scope or blocker. A missing delivery requires complete=false. An honest blocker may be complete as an answer, but must not present the requested work as successful. A tool failure or missing result is never an optional process aside to hide.`
