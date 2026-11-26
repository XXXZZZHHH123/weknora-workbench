package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type qa4ReplayReply struct {
	After                                                 int
	Stage, Content, Action, Feedback, Disposition, Reason string
	Usage                                                 types.TokenUsage
}

type qa4ReplayModel struct {
	mockChat
	t           *testing.T
	plan        json.RawMessage
	replies     []qa4ReplayReply
	counts      map[string]int
	chunkHandle func(string) string
}

func (m *qa4ReplayModel) Chat(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	require.Equal(m.t, "agent_task_plan", purpose)
	require.Zero(m.t, m.counts[purpose], "extra planner request")
	m.counts[purpose]++
	return &types.ChatResponse{Content: string(m.plan), FinishReason: "stop"}, nil
}

func (m *qa4ReplayModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	stage := "candidate"
	switch {
	case purpose == "discovery_delivery":
		require.True(m.t, isDiscoveryDeliveryReviewFormat(opts.Format))
		var input struct{ Phase string }
		require.NoError(m.t, json.Unmarshal([]byte(messages[1].Content), &input))
		stage = input.Phase
	case purpose == "structured_result_delivery":
		require.True(m.t, isStructuredResultReviewFormat(opts.Format))
		stage = "result"
	case strings.Contains(opts.RuntimeNotice, "<preliminary_answer>"):
		stage = "budget"
		deadline, ok := ctx.Deadline()
		require.True(m.t, ok)
		require.Equal(m.t, 35*time.Second, time.Until(deadline))
	default:
		require.Equal(m.t, "agent_round", purpose)
		if len(m.replies) > 0 && m.replies[0].Stage == "timeout" {
			m.replies = m.replies[1:]
			m.counts["timeout"]++
			started := time.Now()
			<-ctx.Done()
			require.Equal(m.t, 25*time.Second, time.Since(started))
			return nil, ctx.Err()
		}
	}
	require.NotEmpty(m.t, m.replies, "undeclared %s model call", stage)
	reply := m.replies[0]
	require.Equal(m.t, reply.Stage, stage, "last message: %s", messages[len(messages)-1].Content)
	m.replies = m.replies[1:]
	m.counts[stage]++
	// When only a verdict survived, generate its wire envelope as a stub.
	if reply.Content == "" && (stage == "progress" || stage == "delivery") {
		if reply.Action == "recover" && reply.Feedback == "" {
			reply.Feedback = "Continue source investigation for the unmet user request."
		}
		return finalSynthesisChunks(discoveryReply(reply.Action, reply.Feedback).chunks...), nil
	}
	if reply.Content == "" && stage == "result" && reply.Action == "keep" {
		reply.Content = `{"action":"keep","edits":[]}`
	}
	require.NotEmpty(m.t, reply.Content, "missing replay response for %s", stage)
	reply.Content = documentAnswerCitation.ReplaceAllStringFunc(reply.Content, func(tag string) string {
		handle := m.chunkHandle(documentAnswerCitation.FindStringSubmatch(tag)[1])
		require.NotEmpty(m.t, handle, "candidate cites an unavailable chunk")
		return `<ref id="` + handle + `" />`
	})
	if isReviewedAnswerFormat(opts.Format) {
		content, err := json.Marshal(map[string]string{"answer": reply.Content})
		require.NoError(m.t, err)
		reply.Content = string(content)
	}
	return finalSynthesisChunks(deliveryReply(reply.Content, "stop", reply.Usage).chunks...), nil
}

func qa4ReplayRegistry(steps ...types.AgentStep) *agenttools.ToolRegistry {
	registry := agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool()
	registry.RegisterTool(base)
	registry.PrepareStructuredProtocol()
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, structuredCodecManifest()) {
		registry.RegisterTool(tool)
	}
	store := &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}}
	var targets types.SearchTargets
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success || call.Name != agenttools.ToolListDocuments {
				continue
			}
			kb := sourceNavigationString(call.Result.Data, "knowledge_base_id")
			if !targets.ContainsKB(kb) {
				targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb})
			}
			for _, row := range sourceNavigationRows(call.Result.Data["documents"]) {
				id := sourceNavigationString(row, "knowledge_id")
				store.byID[id] = &types.Knowledge{ID: id, KnowledgeBaseID: kb, Title: sourceNavigationString(row, "title"), FileName: sourceNavigationString(row, "file_name"), FileType: sourceNavigationString(row, "file_type"), ParseStatus: types.ParseStatusCompleted}
			}
		}
	}
	registry.RegisterTool(agenttools.NewReadDocumentTool(store, nil, targets))
	registry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, store, nil, targets, nil, nil))
	registry.RegisterTool(agenttools.NewListDocumentsTool(store, targets))
	agenttools.PrepareDocumentScope(registry)
	return registry
}

func TestQA4HistoricalReplay(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
	t.Setenv("STRUCTURED_DATA_URL", "")
	for _, id := range []string{"r5-1-kb16", "r5-1-kb07", "r6-2-kb31", "r7-1-kb31", "r7-2-kb31", "r7-3-kb31", "r6-1-kb16", "r6-1-kb07"} {
		t.Run(id, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runQA4HistoricalReplay(t, id, "") })
		})
	}
}

func runQA4HistoricalReplay(t *testing.T, id, future string) {
	var trace struct {
		Question string
		Plan     json.RawMessage
		Steps    []types.AgentStep
		Replay   []qa4ReplayReply
	}
	raw, err := os.ReadFile("testdata/qa4/replay/" + id + ".json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &trace))
	require.NotEmpty(t, trace.Steps)
	require.NotEmpty(t, trace.Replay)
	model := &qa4ReplayModel{t: t, plan: trace.Plan, counts: make(map[string]int)}
	e := newTestEngine(t, model, withCitationsEnabled(true))
	model.chunkHandle = e.modelContext.CitableChunkHandle
	e.config.WorkbenchDefault, e.config.TaskPlanningEnabled, e.config.StructuredResultReviewEnabled = true, true, true
	e.config.AnswerReviewMode, e.config.MaxIterations = "standard", 20
	e.toolRegistry = qa4ReplayRegistry(trace.Steps...)
	e.SetDocumentImageReviewModel(model, "")
	e.SetImageDescriber(func(context.Context, []byte, string) (string, error) {
		t.Fatal("undeclared image description")
		return "", nil
	})
	e.prepareTaskPlanning()
	ctx := withAgentExecutionStart(t.Context())
	state := &types.AgentState{}
	messages := []chat.Message{{Role: "system", Content: "Answer the requested task using the authorized sources."}, {Role: "user", Content: trace.Question}}
	e.reviewConversation, e.reviewConversationReady = conversationThroughLastHuman(messages), true
	messages = e.planWorkbenchTask(ctx, state, trace.Question, messages, "session")
	require.Equal(t, len(trace.Plan) == 0 || string(trace.Plan) == "null", e.taskPlan == nil, "old or unavailable plan")
	if e.taskPlan != nil {
		for _, target := range e.taskPlan.CoverageTargets {
			require.Nil(t, target.StructuredTaskGoal, "historic text does not declare a structured scope")
		}
	}
	guards := &loopGuards{}
	sources, expectedCalls := map[string]int{}, map[string]int{"agent_task_plan": 1}
	var lastAnswer string
	for _, reply := range trace.Replay {
		expectedCalls[reply.Stage]++
		if reply.Stage == "candidate" || reply.Stage == "budget" {
			lastAnswer = reply.Content
		}
	}
	for i, step := range trace.Steps {
		require.False(t, state.IsComplete, "committed before the remaining tool batches")
		state.CurrentRound = step.Iteration
		for _, call := range step.ToolCalls {
			restoreQA4ToolResult(t, ctx, e.toolRegistry, call)
			sources[call.Name]++
		}
		state.RoundSteps = append(state.RoundSteps, step)
		messages = e.appendToolResults(messages, step)
		for len(trace.Replay) > 0 && trace.Replay[0].After == i+1 {
			model.replies = append(model.replies, trace.Replay[0])
			trace.Replay = trace.Replay[1:]
		}
		e.reviewSourceProgress(ctx, trace.Question, state, &messages, guards, "session")
		if len(model.replies) == 0 || model.replies[0].Stage != "budget" {
			require.Empty(t, e.standardAnswerBudgetReason(ctx, state, agentExecutionStart(ctx)))
		}
		for len(model.replies) > 0 {
			require.False(t, state.IsComplete, "committed before the remaining replies")
			reply, remaining := model.replies[0], len(model.replies)
			switch reply.Stage {
			case "candidate", "timeout":
				candidates := model.counts[reply.Stage]
				if future == "gap" && guards.evidenceRecoveryAttempts > 0 {
					// Replace the next recorded draft with the supported textual
					// portion of the final response, leaving the same gap and tools.
					model.replies = []qa4ReplayReply{{Stage: "candidate", Content: lastAnswer}, {Stage: "result", Action: "keep"}}
					_, err := e.runReActIteration(ctx, state, &messages, e.buildToolsForLLM(), "session", "message", trace.Question, guards)
					require.NoError(t, err)
					require.Equal(t, candidateNeedSource, e.turnReview.Last.Disposition)
					require.False(t, state.IsComplete)
					return
				}
				state.CurrentRound++
				_, err := e.runReActIteration(ctx, state, &messages, e.buildToolsForLLM(), "session", "message", trace.Question, guards)
				require.NoError(t, err)
				require.Equal(t, candidates+1, model.counts[reply.Stage])
			case "budget":
				require.Equal(t, reply.Reason, e.standardAnswerBudgetReason(ctx, state, agentExecutionStart(ctx)))
				finished, err := e.finishStandardAnswerBudget(ctx, state, trace.Question, "session", "message", &messages, guards)
				require.NoError(t, err)
				require.True(t, finished)
			default:
				t.Fatalf("unrequested %s model reply after batch %d, unsupported=%v", reply.Stage, i+1, e.turnReview.Last.Unsupported)
			}
			require.Less(t, len(model.replies), remaining)
			if reply.Disposition != "" {
				require.Equal(t, reply.Disposition, string(e.turnReview.Last.Disposition), "unsupported tokens: %v", e.turnReview.Last.Unsupported)
			}
		}
	}
	require.Empty(t, trace.Replay)
	require.Equal(t, expectedCalls, model.counts)
	require.Equal(t, countTotalToolCalls(trace.Steps), countTotalToolCalls(state.RoundSteps), "no unrecorded tool calls")
	require.True(t, e.turnReview.Committed)
	require.True(t, state.IsComplete)
	require.Len(t, e.turnReview.ClosedStreams, 1)
	if future == "retain" {
		require.Contains(t, state.FinalAnswer, "2023-09-22")
		require.Contains(t, state.FinalAnswer, "17110")
		return
	}
	require.Empty(t, future, "missing future coverage checkpoint")
	require.Equal(t, strings.TrimSpace(lastAnswer), state.FinalAnswer)
	assertQA4ReplayObservations(t, id, raw, e, trace.Steps)
	t.Logf("stub model requests=%v; replayed tool results=%v (document bindings executed locally)", model.counts, sources)
}

func restoreQA4ToolResult(t *testing.T, ctx context.Context, registry *agenttools.ToolRegistry, call types.ToolCall) {
	t.Helper()
	tool, err := registry.GetTool(call.Name)
	require.NoError(t, err)
	require.NotNil(t, call.Result)
	args, err := json.Marshal(call.Args)
	require.NoError(t, err)
	if call.Name == agenttools.ToolBindDocumentScope {
		actual, err := tool.Execute(ctx, args)
		require.NoError(t, err)
		require.Equal(t, call.Result.Success, actual.Success, actual.Error)
		require.Equal(t, call.Result.PreflightRejected, actual.PreflightRejected)
		require.Equal(t, call.Result.Error, actual.Error)
	}
	data := call.Result.Data
	saved, ok := data["_structured_execution"]
	actual, _, native := registry.StructuredInvocation(call.Name, args)
	require.Equal(t, native, ok, "descriptor presence for %s", call.Name)
	if !ok {
		return
	}
	var descriptor agenttools.StructuredOperationDescriptor
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &descriptor))
	if descriptor.ToolName != "" {
		require.Equal(t, actual.ToolName, descriptor.ToolName)
	}
	descriptor.ToolName = actual.ToolName // stable native name may be omitted from the projection
	require.Equal(t, actual.Operation, descriptor.Operation)
	require.Equal(t, actual.EffectClass, descriptor.EffectClass)
	require.Equal(t, actual.ScopeMode, descriptor.ScopeMode)
	data["_structured_execution"] = descriptor
	_, err = structuredresult.ValidatePayload(data)
	require.NoError(t, err)
	encoded, err = json.Marshal(data)
	require.NoError(t, err)
	call.Result.Output = string(encoded)
}

func TestQA4ReplayPendingCoverage(t *testing.T) {
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
	t.Setenv("STRUCTURED_DATA_URL", "")
	t.Run("stage_1_explicit_source_mode", func(t *testing.T) {
		t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "")
		const planJSON = `{"goals":["先查台账"],"evidence":[],"tool_names":[],"completion_criteria":["给出记录"],"coverage_targets":[{"goal_index":0,"request_basis":"先查台账","criteria":"给出记录","source_scope":"首查台账","source_mode":"named_first"}]}`
		plan, err := decodeTaskEvidencePlan(planJSON, nil, 0)
		require.NoError(t, err)
		encoded, err := json.Marshal(plan)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"source_mode":"named_first"`)
	})
	t.Run("stage_2_zero_read_gap", func(t *testing.T) {
		t.Skip("stage 2 will keep the gap open after a recovery draft with zero reads")
		synctest.Test(t, func(t *testing.T) { runQA4HistoricalReplay(t, "r6-2-kb31", "gap") })
	})
	t.Run("stage_3_budget_preserves_original_cell", func(t *testing.T) {
		t.Skip("stage 3 will pin evidence across budget synthesis")
		synctest.Test(t, func(t *testing.T) { runQA4HistoricalReplay(t, "r5-1-kb16", "retain") })
	})
}
