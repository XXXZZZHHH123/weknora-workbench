package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestEvidenceCompletionShadowReportsDifferenceAndLeavesCandidate(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	engine := newTestEngine(t, &mockChat{})
	engine.config.WorkbenchDefault, engine.config.AnswerReviewMode = true, "standard"
	engine.toolRegistry = qa4ReplayRegistry()
	engine.evidenceCompletionEnabled()
	var logs bytes.Buffer
	log := logrus.New()
	log.SetOutput(&logs)
	ctx := context.WithValue(t.Context(), types.LoggerContextKey, logrus.NewEntry(log))
	decision := candidateDecision{Candidate: "A verified draft.", Disposition: candidateAccepted}
	state := &types.AgentState{}
	checked, handled := engine.checkEvidenceCompletion(ctx, decision, state, "delivery")
	require.False(t, handled)
	require.Equal(t, decision, checked)
	require.Contains(t, logs.String(), "no_validated_coverage_target")
	logs.Reset()
	engine.evidenceCompletion.result = []coverageCompletion{{GapID: "observation-gap", State: coverageOpen, Reason: "primary_unverified"}}
	checked, handled = engine.checkEvidenceCompletion(ctx, decision, state, "delivery")
	require.False(t, handled)
	require.Equal(t, decision, checked)
	require.Empty(t, engine.answerReviewAction)
	require.Empty(t, engine.answerReviewFeedback)
	require.Zero(t, engine.evidenceCompletion.recoveryDecisions)
	for _, field := range []string{"disposition_changed=true", "candidate_disposition=accepted", "local_disposition=need_source"} {
		require.Contains(t, logs.String(), field)
	}
}

type coverageRuntimeIndex struct {
	*sourceDiscoveryKnowledgeService
	document *types.Knowledge
	calls    int
}

func (index *coverageRuntimeIndex) HybridSearch(_ context.Context, _ string, params types.SearchParams) ([]*types.SearchResult, error) {
	index.calls++
	return []*types.SearchResult{{ID: "contract-supporting", KnowledgeID: index.document.ID,
		KnowledgeBaseID: index.document.KnowledgeBaseID, KnowledgeTitle: index.document.Title, Content: "Section on the observed duration.", Score: 0.99,
		SourceLocators: types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 8, Mapping: "exact", SourceHash: strings.Repeat("a", 64)}}}}, nil
}

func TestEvidenceCompletionNamedFirstDiscoveryAndRead(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	t.Run("available", func(t *testing.T) { runEvidenceCompletionNamedFirst(t, true, true) })
	t.Run("available_without_read_metadata", func(t *testing.T) { runEvidenceCompletionNamedFirst(t, true, false) })
	t.Run("unavailable", func(t *testing.T) { runEvidenceCompletionNamedFirst(t, false, false) })
}

func runEvidenceCompletionNamedFirst(t *testing.T, documentRoute, readMetadata bool) {
	t.Helper()
	const (
		query = "Use the equipment register first for the duration on 2021-06-10."
		date  = "2021-06-10"
		body  = "The test pump's elapsed operating time on 2021-06-10 was thirteen hours."
	)
	model := &mockChat{}
	engine := newTestEngine(t, model, withCitationsEnabled(true))
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled, engine.config.AnswerReviewMode = true, true, "standard"
	engine.toolRegistry = agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool()
	engine.toolRegistry.RegisterTool(base)
	engine.toolRegistry.PrepareStructuredProtocol()
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, structuredCodecManifest()) {
		engine.toolRegistry.RegisterTool(tool)
	}
	document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7, Title: "Inspection report",
		FileName: "inspection-report.pdf", FileType: "pdf", FileHash: strings.Repeat("a", 64), ParseStatus: types.ParseStatusCompleted}
	index := &coverageRuntimeIndex{sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{}, document: document}
	knowledge := &plannedDocumentKnowledge{document: document}
	locator := types.SourceLocators{{Type: types.SourceLocatorPDF, Page: 8, Mapping: "exact", SourceHash: strings.Repeat("a", 64)}}
	repository := &citationContractChunkRepository{rows: []*types.Chunk{{ID: "contract-supporting", KnowledgeID: document.ID,
		KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7, ChunkType: types.ChunkTypeText, IsEnabled: true,
		Content: "# 第8页\n\n" + body + " A backup pump was checked for pressure.", SourceLocators: locator}}}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}}
	ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "approved-reader", Role: types.TenantRoleViewer}))
	if documentRoute {
		engine.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(index, knowledge, &citationContractChunkService{repo: repository}, targets, nil, nil))
		engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(knowledge, &citationContractChunkService{repo: repository}, targets))
		engine.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(knowledge, targets))
		agenttools.PrepareDocumentScope(engine.toolRegistry, query)
	}
	engine.taskPlan = &taskEvidencePlan{
		Goals: []string{"Find the observed equipment duration"}, CoverageTargets: []taskCoverageTarget{
			{GoalIndex: 0, RequestBasis: query, StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "named_first", PrimaryRef: "equipment register", Metric: "equipment duration",
				Period: map[string]any{"from": date, "to": date, "grain": "day"}, SourceTime: "observed_period",
			}},
		},
	}
	series := map[string]any{"series_id": "duration", "sheet": "operating hours", "column": "B", "metric": "duration", "grain": "day"}
	scope := map[string]any{"knowledge_base_id": "contract-library"}
	source := map[string]any{"sha256": strings.Repeat("b", 64), "file": "equipment register"}
	const native = "structured_query_series"
	args := map[string]any{"dataset_id": "dataset", "series_id": "duration", "start_date": date, "end_date": date}
	catalogue := types.ToolCall{ID: "catalogue", Name: "structured_list_series", Args: map[string]any{"dataset_id": "dataset"},
		Result: &types.ToolResult{Success: true, Data: map[string]any{"source": source, "source_context": scope, "dataset_id": "dataset", "truncated": false,
			"_structured_execution": map[string]any{"canonical_operation": "list_series", "effect_class": "read", "scope_mode": "knowledge_base"},
			"series":                []any{series}}}}
	gapCall := types.ToolCall{ID: "empty-primary-read", Name: native, Args: args,
		Result: &types.ToolResult{Success: true, Data: map[string]any{"source": source, "source_context": scope, "dataset_id": "dataset", "status": "no_data", "truncated": false,
			"_structured_execution": map[string]any{"canonical_operation": "query_series", "effect_class": "read", "scope_mode": "knowledge_base"},
			"series":                series, "valid_count": 0, "filters": map[string]any{"series_id": "duration", "start_date": date, "end_date": date, "value_filters": []any{}}}}}
	restoreQA4ToolResult(t, ctx, engine.toolRegistry, catalogue)
	restoreQA4ToolResult(t, ctx, engine.toolRegistry, gapCall)
	step := types.AgentStep{Iteration: 0, ToolCalls: []types.ToolCall{catalogue, gapCall}}
	state := &types.AgentState{CurrentRound: 1, RoundSteps: []types.AgentStep{step}}
	messages := []chat.Message{{Role: "system", Content: "Use authorized observations."}, {Role: "user", Content: query}}
	engine.reviewConversation, engine.reviewConversationReady = conversationThroughLastHuman(messages), true
	messages = engine.appendToolResults(messages, step)
	engine.refreshEvidenceCompletion(ctx, query, state, messages)
	ref := func(kind string) string {
		t.Helper()
		for ref, obs := range engine.evidenceCompletion.refs {
			if obs.Kind == kind {
				return ref
			}
		}
		t.Fatalf("no %s in %v", kind, engine.evidenceCompletion.record.Evidence)
		return ""
	}
	declare := func(ref, role, gap string, extras map[string]any) map[string]any {
		out := map[string]any{"goal_index": 0, "selection_ref": ref, "role": role, "gap_id": gap, "period_from": date, "period_to": date, "grain": "day"}
		for k, v := range extras {
			out[k] = v
		}
		return out
	}
	var guards loopGuards
	run := func(reply mockResponse) {
		t.Helper()
		model.responses = append(model.responses, reply)
		_, err := engine.runReActIteration(ctx, state, &messages, engine.buildToolsForLLM(), "s", "m", query, &guards)
		require.NoError(t, err)
		state.CurrentRound++
	}
	wire := func(value any) string {
		t.Helper()
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		return string(encoded)
	}
	draft := func(answer string, d map[string]any) mockResponse {
		return chatAnswer(answer + coverageAnswerTag + wire([]any{d}) + coverageAnswerClose)
	}
	const limitation = "The equipment register has no matching entry on 2021-06-10."
	run(draft(limitation, declare(ref("directory"), "primary", "", nil)))
	first, ok := engine.evidenceCompletionGap()
	require.True(t, ok)
	if !documentRoute {
		require.Equal(t, coverageExhausted, first.State)
		require.Equal(t, "no_authorized_supplemental_route", first.Reason)
		require.Equal(t, "evidence_completion_exhausted", engine.turnReview.Last.ReasonCode)
		require.False(t, engine.candidateApprovalCurrent(state.FinalAnswer, state))
		require.True(t, state.IsComplete)
		require.Contains(t, state.FinalAnswer, "equipment register")
		require.Contains(t, state.FinalAnswer, date)
		require.Len(t, model.calls, 1)
		require.Zero(t, guards.evidenceRecoveryAttempts)
		require.Zero(t, index.calls)
		return
	}
	require.Equal(t, "discover", first.NextRead)
	require.False(t, state.IsComplete)
	for _, tool := range engine.evidenceCompletionTools(engine.buildToolsForLLM()) {
		if tool.Function.Name == native {
			require.Contains(t, string(tool.Function.Parameters), coverageArgument)
		}
	}
	declaration := declare("", "supplement", first.GapID, nil)
	wrong := func(id, tool string, args map[string]any) types.LLMToolCall {
		args[coverageArgument] = declaration
		return types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: tool, Arguments: wire(args)}}
	}
	engine.processEvidenceCompletionCalls(ctx, query, state, messages, []types.LLMToolCall{
		wrong("repeated-series", "structured_list_series", map[string]any{"dataset_id": "dataset"}),
		wrong("document-directory", agenttools.ToolListDocuments, map[string]any{}),
		wrong("blank-search", agenttools.ToolSearchKnowledge, map[string]any{"query": "  "}),
	})
	require.Empty(t, engine.evidenceCompletion.recoveryCalls)
	require.Zero(t, index.calls)
	require.Empty(t, engine.evidenceCompletion.record.Attempts, "a rejected directory or missing query does not spend discovery")
	require.NotNil(t, engine.evidenceCompletion.pending)
	require.Equal(t, first.GapID, engine.evidenceCompletion.pending.GapID)
	require.Equal(t, "discover", engine.evidenceCompletion.pending.NextRead)

	run(chatToolCall("targeted-search", agenttools.ToolSearchKnowledge, wire(map[string]any{
		"query": "equipment duration 2021-06-10", coverageArgument: declare("", "supplement", first.GapID, map[string]any{"unknown": true}),
	})))
	require.Equal(t, 1, index.calls)
	navigation := ref("navigation")
	handle := engine.modelContext.RegisterDocument(document.ID)
	require.True(t, strings.HasPrefix(handle, "doc://"))
	sourceScope := map[string]any{"selection_reason": "The authorized search led to the requested report and page.",
		"sources": []any{map[string]any{"knowledge_id": handle, "expected_file_name": document.FileName, "page": 8}}}
	readDeclaration := declare(navigation, "supplement", first.GapID, map[string]any{"page": 8})
	require.Nil(t, engine.evidenceCompletion.pending)
	decisions, priorBindings := engine.evidenceCompletion.recoveryDecisions, len(engine.evidenceCompletion.record.Bindings)
	wrongReads := []types.LLMToolCall{
		{ID: "neighbor-page", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: wire(map[string]any{
			"id": handle, "page": 9, "source_scope": sourceScope, coverageArgument: readDeclaration,
		})}},
		{ID: "unbound-document", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: wire(map[string]any{
			"id": "different-document", "page": 8, coverageArgument: readDeclaration,
		})}},
	}
	engine.modelContext.DecodeToolCalls(wrongReads)
	engine.processEvidenceCompletionCalls(ctx, query, state, messages, wrongReads)
	require.Nil(t, engine.evidenceCompletion.pending, "neither rejected read should activate the pending action")
	require.Equal(t, decisions, engine.evidenceCompletion.recoveryDecisions)
	require.Len(t, engine.evidenceCompletion.record.Bindings, priorBindings+1, "the valid source association survives a mismatched recovery action")
	require.Empty(t, engine.evidenceCompletion.recoveryCalls)
	readArgs := map[string]any{
		"id": handle, "page": 8, "source_scope": sourceScope,
	}
	if readMetadata {
		readArgs[coverageArgument] = readDeclaration
	}
	run(chatToolCall("matching-read", agenttools.ToolReadDocument, wire(readArgs)))
	read := state.RoundSteps[len(state.RoundSteps)-1].ToolCalls[0]
	require.True(t, read.Result.Success, fmt.Sprint(read.Result))
	require.Equal(t, document.ID, read.ExecutionArgs()["id"], "the runtime gate receives the decoded source handle")
	bodyRef := ref("body")
	bodyClaim := func(subject, metric string) map[string]any {
		return declare(bodyRef, "supplement", first.GapID, map[string]any{
			"span": body, "date": date, "value_text": "thirteen", "unit": "hours", "require_unit": true,
			"subject": subject, "metric": metric,
		})
	}
	const subject, metric = "test pump", "elapsed operating time"
	bindings := len(engine.evidenceCompletion.record.Bindings)
	for _, bad := range []map[string]any{bodyClaim("backup pump", metric), bodyClaim(subject, "pressure"), bodyClaim(strings.Repeat("a", 129), metric)} {
		_, ok := engine.acceptCoverageDeclaration(ctx, query, messages, json.RawMessage(wire(bad)))
		require.False(t, ok)
	}
	actual := engine.evidenceCompletion.refs[bodyRef]
	conflicting := actual
	conflicting.Metric = "pressure"
	engine.evidenceCompletion.refs[bodyRef] = conflicting
	_, ok = engine.acceptCoverageDeclaration(ctx, query, messages, json.RawMessage(wire(bodyClaim(subject, metric))))
	require.False(t, ok)
	engine.evidenceCompletion.refs[bodyRef] = actual
	require.Len(t, engine.evidenceCompletion.record.Bindings, bindings)
	remaining, _ := engine.evidenceCompletionGap()
	require.NotEqual(t, coverageSatisfied, remaining.State)

	const answer = "The equipment register has no matching duration entry on 2021-06-10. The Inspection report gives an elapsed operating time of thirteen hours for the test pump on 2021-06-10."
	citation := engine.modelContext.CitableChunkHandle("contract-supporting")
	require.NotEmpty(t, citation)
	run(draft(answer+`<ref id="`+citation+`"/>`, bodyClaim(subject, metric)))
	require.True(t, state.IsComplete)
	engine.emitCompletionEvent(ctx, state, "s", "m", time.Now())
	require.Equal(t, answer+"〔Inspection report〕", state.FinalAnswer)
	require.False(t, types.HasAgentAnswerMarkup(state.FinalAnswer))
	require.Len(t, state.KnowledgeRefs, 1)
	require.Equal(t, "contract-supporting", state.KnowledgeRefs[0].ID)
	require.Equal(t, candidateAccepted, engine.turnReview.Last.Disposition)
	require.Len(t, engine.evidenceCompletion.record.Attempts, 2)
	require.Equal(t, []string{"discover", "read"}, []string{engine.evidenceCompletion.record.Attempts[0].Action, engine.evidenceCompletion.record.Attempts[1].Action})
	require.Equal(t, coverageSatisfied, engine.evidenceCompletion.result[0].State)
	require.Equal(t, "matching_supplement", engine.evidenceCompletion.result[0].Reason)
	require.Contains(t, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Raw, metric)
	require.Equal(t, subject, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Subject)
	require.Equal(t, metric, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Metric)
	require.Len(t, model.calls, 4)
}

func TestEvidenceCompletionPendingAcceptsFirstBindingWithoutGapID(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.toolRegistry = qa4ReplayRegistry()
	e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{
		{GoalIndex: 0, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "named_first"}},
		{GoalIndex: 1, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "named_first"}},
	}}
	e.refreshEvidenceCompletion(t.Context(), "Read the equipment duration.", &types.AgentState{}, nil)
	turn := &e.evidenceCompletion
	turn.pending = &coverageCompletion{GoalIndex: 0, GapID: "current-gap", State: coverageOpen, NextRead: "read", BudgetRemaining: 1}
	observation := coverageEvidence{Kind: "directory", SourceDomain: "structured", SourceID: "register", KnowledgeBaseID: "library", SeriesID: "duration", EvidenceID: "directory", JSONPointer: "/data/series/0", Revision: turn.revision, AuthScope: turn.scope}
	turn.seenObservations = map[string]bool{}
	turn.record.Evidence = []coverageEvidence{observation}
	turn.refs = map[string]coverageEvidence{"verified-field": observation}
	call := func(id, add string) types.LLMToolCall {
		return types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: "structured_query_series", Arguments: `{"dataset_id":"register","series_id":"duration","task_coverage":{"role":"primary","selection_ref":"verified-field",` + add + `}}`}}
	}
	calls := []types.LLMToolCall{
		call("wrong-goal", `"goal_index":1`),
		call("wrong-gap", `"goal_index":0,"gap_id":"stale-gap"`),
		call("matching-goal", `"goal_index":0`),
	}
	e.processEvidenceCompletionCalls(t.Context(), "Read the equipment duration.", &types.AgentState{}, nil, calls)
	require.Equal(t, map[string]bool{"wrong-gap": true, "matching-goal": true}, turn.recoveryCalls)
	require.Len(t, turn.record.Bindings, 2)
	require.Equal(t, 1, turn.record.Bindings[0].GoalIndex)
	require.Equal(t, 0, turn.record.Bindings[1].GoalIndex)
	require.Equal(t, "register", turn.record.Bindings[1].SourceID)
}

func TestEvidenceCompletionOptionalPendingReadUsesCurrentBinding(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	const (
		day      = "2021-06-10"
		baseArgs = `"dataset_id":"verified-register","series_id":"duration","start_date":"` + day + `","end_date":"` + day + `"`
		gapID    = "current-gap"
	)
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.toolRegistry = qa4ReplayRegistry()
	e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "named_first"}}}}
	e.refreshEvidenceCompletion(t.Context(), "Read the equipment duration.", &types.AgentState{}, nil)
	turn := &e.evidenceCompletion
	turn.pending = &coverageCompletion{GapID: gapID, State: coverageOpen, NextRead: "read", BudgetRemaining: 1}
	turn.record.Bindings = []coverageBinding{{Role: "primary", SourceID: "verified-register", KnowledgeBaseID: "library", Sheet: "observations", SeriesID: "duration", PeriodFrom: day, PeriodTo: day}}
	observation := coverageEvidence{Kind: "directory", SourceDomain: "structured", SourceID: "verified-register", KnowledgeBaseID: "library", Sheet: "observations", SeriesID: "duration", EvidenceID: "directory", JSONPointer: "/data/series/0", Revision: turn.revision, AuthScope: turn.scope}
	turn.seenObservations = map[string]bool{}
	turn.record.Evidence = []coverageEvidence{observation}
	turn.refs = map[string]coverageEvidence{"verified-field": observation}
	call := func(id, name, args string) types.LLMToolCall {
		return types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: name, Arguments: "{" + args + "}"}}
	}
	calls := []types.LLMToolCall{
		call("wrong-field", "structured_query_series", strings.Replace(baseArgs, `"duration"`, `"pressure"`, 1)),
		call("wrong-period", "structured_query_series", strings.ReplaceAll(baseArgs, day, "2021-06-11")),
		call("wrong-source", "structured_query_series", strings.Replace(baseArgs, `"verified-register"`, `"other-register"`, 1)),
		call("wrong-domain", agenttools.ToolReadDocument, `"id":"verified-register","page":8`),
		call("wrong-action", "structured_list_series", `"dataset_id":"verified-register"`),
		call("wrong-library", "structured_query_series", baseArgs+`,"knowledge_base_id":"other-library"`),
		call("wrong-inspection-sheet", "structured_inspect_source", `"dataset_id":"verified-register","sheet":"other","ranges":["A3:B5"]`),
		call("bound-inspection", "structured_inspect_source", `"dataset_id":"verified-register","sheet":"observations","ranges":["A3:B5"]`),
		call("invalid-declaration", "structured_query_series", baseArgs+`,"task_coverage":{"goal_index":0,"gap_id":"current-gap","unknown":true}`),
		call("stale-gap", "structured_query_series", baseArgs+`,"task_coverage":{"goal_index":0,"gap_id":"stale-gap","role":"primary","selection_ref":"verified-field"}`),
		call("valid-gap", "structured_query_series", baseArgs+`,"task_coverage":{"goal_index":0,"gap_id":"current-gap"}`),
		call("over-budget", "structured_query_series", baseArgs),
	}
	e.processEvidenceCompletionCalls(t.Context(), "Read the equipment duration.", &types.AgentState{}, nil, calls)
	require.Equal(t, map[string]bool{"bound-inspection": true, "invalid-declaration": true, "stale-gap": true, "valid-gap": true}, turn.recoveryCalls)
	require.Len(t, turn.record.Bindings, 2, "only the valid source association can add a binding")
	require.Empty(t, turn.declared)
	require.Len(t, turn.record.Evidence, 1)
}

func TestEvidenceCompletionRetainsNativeDraftWithoutDeclaration(t *testing.T) {
	const question = "What value did the register report?"
	const native = "structured_query_series"
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", enabled)
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: question,
				StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", Metric: "elapsed runtime", Required: []string{"scoped_value"}}}}}
			ctx := t.Context()
			call := coverageSourceFixture(t, "query_series", `{"dataset_id":"register","series_id":"runtime"}`, `{
				"dataset_id":"register","status":"ok","truncated":false,
				"source":{"file":"register.xlsx","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sheet":"Values"},
				"series":{"series_id":"runtime","metric":"elapsed runtime","last_valid_date":"2025-01-01","header_cells":["B1"]},
				"rows":[{"date":"2025-01-01","date_cell":"A2","source_cell":"B2","value":13,"value_status":"valid"}],
				"filters":{"series_id":"runtime","value_filters":[]}
			}`)
			require.Equal(t, native, call.Name)
			wire, err := json.Marshal(call.Result.Data)
			require.NoError(t, err)
			call.Result.Output = string(wire)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			conversation := []chat.Message{{Role: "user", Content: question + " Was it 97?"}}
			draft := "The elapsed runtime was 13."
			e.refreshEvidenceCompletion(ctx, question, state, conversation)
			decision := e.checkCandidate(ctx, question, draft, "", state, conversation, false)
			if enabled == "false" {
				require.Equal(t, candidateAccepted, decision.Disposition)
				require.Equal(t, draft, e.turnReview.CoverageDraft.Candidate)
				require.False(t, e.evidenceCompletion.enabled)
				return
			}
			require.Equal(t, candidateNeedSource, decision.Disposition)
			require.Equal(t, draft, e.turnReview.CoverageDraft.Candidate)
			for _, unrelated := range []string{
				"The elapsed runtime was 97.", // The number occurs only in the question.
				"Pressure was 13.",            // The same numeric literal belongs to a different field.
				"The source contains useful information.",
			} {
				e.checkCandidate(ctx, question, unrelated, "", state, conversation, false)
				current, ok := e.currentCoverageDraft(ctx, state)
				require.True(t, ok, unrelated)
				require.Equal(t, draft, current, unrelated)
			}
			call.Result.PreflightRejected = true
			_, ok := e.currentCoverageDraft(ctx, state)
			require.False(t, ok, "a changed execution receipt invalidates the retained draft")
			replacement := call
			replacementResult := *call.Result
			replacement.ID, replacementResult.PreflightRejected = "new-query", false
			replacement.Result = &replacementResult
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{replacement}})
			const revised = "The register's elapsed runtime was 13."
			e.checkCandidate(ctx, question, revised, "", state, conversation, false)
			current, ok := e.currentCoverageDraft(ctx, state)
			require.True(t, ok)
			require.Equal(t, revised, current, "an equally supported current answer can replace a stale draft")
		})
	}
}

func TestEvidenceCompletionLatestReusesOnlyCurrentModelSelectedSeries(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.toolRegistry = qa4ReplayRegistry()
	ctx := t.Context()
	call := coverageSourceFixture(t, "query_series",
		`{"dataset_id":"register","series_id":"duration","start_date":"2025-02-01","end_date":"2025-02-28"}`, `{
		"dataset_id":"register","status":"no_data","valid_count":0,"truncated":false,
		"source":{"file":"register.xlsx","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sheet":"Values"},
		"series":{"series_id":"duration","metric":"duration","header_cells":["B1"],"last_valid_date":"2025-01-31"},
		"filters":{"series_id":"duration","start_date":"2025-02-01","end_date":"2025-02-28","value_filters":[]}
	}`)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	e.refreshEvidenceCompletion(ctx, "Read duration for February.", state, nil)
	key, name, args := e.coverageSameFieldLatestRead(state)
	require.NotEmpty(t, key)
	require.Equal(t, "structured_query_series", name)
	require.Equal(t, map[string]any{"dataset_id": "register", "knowledge_base_id": "authorized-library",
		"series_id": "duration", "operation": "latest", "limit": 1}, args)
	t.Run("exhausted recovery does not execute a native read", func(t *testing.T) {
		t.Setenv("STRUCTURED_DATA_URL", "")
		t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
		toolCalls := 0
		e.eventBus.On(event.EventAgentToolCall, func(_ context.Context, _ event.Event) error {
			toolCalls++
			return nil
		})
		original := e.evidenceCompletion
		for _, tc := range []struct {
			name, stop string
			pending    *coverageCompletion
			decisions  int
			attempts   int
		}{
			{name: "turn stopped", stop: "model_call_time"},
			{name: "pending exhausted", pending: &coverageCompletion{State: coverageExhausted, NextRead: "read", BudgetRemaining: 1}},
			{name: "no pending allowance", pending: &coverageCompletion{State: coverageOpen, NextRead: "read"}},
			{name: "runtime decisions exhausted", decisions: maxEvidenceRecoveryAttempts},
			{name: "loop attempts exhausted", attempts: maxEvidenceRecoveryAttempts},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e.evidenceCompletion = original
				t.Cleanup(func() { e.evidenceCompletion = original })
				turn := &e.evidenceCompletion
				turn.stopReason, turn.pending, turn.recoveryDecisions = tc.stop, tc.pending, tc.decisions
				guards := loopGuards{evidenceRecoveryAttempts: tc.attempts}
				var messages []chat.Message
				current := *state
				read, err := e.executeCoverageDraftRead(ctx, "Read duration for February.", &current, &messages, &guards, "session", "turn", types.AgentStep{}, candidateDecision{})
				require.NoError(t, err)
				require.False(t, read)
				require.Zero(t, toolCalls)
				require.Equal(t, state.RoundSteps, current.RoundSteps)
				require.Empty(t, turn.latestAttempts)
				require.Equal(t, tc.attempts, guards.evidenceRecoveryAttempts)
			})
		}
	})
	gap := e.evidenceCompletion.record.Evidence[0]
	stale := gap
	stale.SnapshotHash = strings.Repeat("b", 64)
	e.evidenceCompletion.record.Evidence[0] = stale
	_, name, args = e.coverageSameFieldLatestRead(state)
	require.Empty(t, name)
	require.Nil(t, args)
	e.evidenceCompletion.record.Evidence[0] = gap
	e.evidenceCompletion.latestAttempts = map[string]bool{key: true}
	_, name, _ = e.coverageSameFieldLatestRead(state)
	require.Empty(t, name, "one matching field cannot take a second automatic shot")

	other := gap
	other.SeriesID, other.Metric, other.Column = "pressure", "pressure", "C"
	turn := &e.evidenceCompletion
	turn.record.Evidence = append(turn.record.Evidence, other)
	_, name, _ = e.coverageSameFieldLatestRead(state)
	require.Empty(t, name, "independent fields without a model association leave the selection unresolved")
	turn.result = []coverageCompletion{
		{GoalIndex: 0, PinnedFacts: []coverageFact{{Role: "coverage_boundary", Evidence: gap}}},
		{GoalIndex: 1, PinnedFacts: []coverageFact{{Role: "coverage_boundary", Evidence: other}}},
	}
	_, name, args = e.coverageSameFieldLatestRead(state)
	require.Equal(t, "structured_query_series", name)
	require.Equal(t, "pressure", args["series_id"], "one attempted field does not consume another associated goal's read")
}

func TestEvidenceCompletionAdvanceHonorsBothRecoveryCounts(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	for _, tc := range []struct {
		name, stop, expectedStop string
		decisions, attempts      int
	}{
		{name: "turn stopped", stop: "model_call_time", expectedStop: "model_call_time"},
		{name: "runtime decisions exhausted", decisions: maxEvidenceRecoveryAttempts, expectedStop: "recovery_budget_exhausted"},
		{name: "loop attempts exhausted", attempts: maxEvidenceRecoveryAttempts, expectedStop: "recovery_budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
			e.evidenceCompletionEnabled()
			turn := &e.evidenceCompletion
			turn.stopReason, turn.recoveryDecisions = tc.stop, tc.decisions
			turn.result = []coverageCompletion{{GapID: "verified-gap", State: coverageOpen, NextRead: "discover",
				OpenItems: []string{"attributed_supplement"}, BudgetRemaining: 2}}
			guards := loopGuards{evidenceRecoveryAttempts: tc.attempts}
			var messages []chat.Message
			e.advanceEvidenceCompletion(t.Context(), "Find the reported duration.", &types.AgentState{}, &messages, &guards, "session")
			require.Nil(t, turn.pending)
			require.Equal(t, tc.expectedStop, turn.stopReason)
			require.Equal(t, tc.decisions, turn.recoveryDecisions)
			require.Equal(t, tc.attempts, guards.evidenceRecoveryAttempts)
			require.Zero(t, e.turnReview.Repairs)
			require.Empty(t, messages)
		})
	}
}

func TestEvidenceCompletionDiscardInvalidOptionalDeclaration(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	const declaration = `{"goal_index":0,"selection_ref":"doc://999","role":"primary","unknown":"res://999"}`
	for _, tc := range []struct {
		name, query             string
		modelCopyOnly, rejected bool
	}{
		{name: "unresolved handle is only in metadata", query: "equipment duration"},
		{name: "metadata remains only in model arguments", query: "equipment duration", modelCopyOnly: true},
		{name: "unresolved handle is also in actual arguments", query: "doc://999", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			document := &types.Knowledge{ID: "contract-document", KnowledgeBaseID: "contract-library", TenantID: 7, Title: "Inspection report"}
			index := &coverageRuntimeIndex{sourceDiscoveryKnowledgeService: &sourceDiscoveryKnowledgeService{}, document: document}
			knowledge := &plannedDocumentKnowledge{document: document}
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: 7}}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(agenttools.NewSearchKnowledgeTool(index, knowledge, nil, targets, nil, nil))
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open"}}}}
			arguments := fmt.Sprintf(`{"query":%q,"task_coverage":%s}`, tc.query, declaration)
			calls := []types.LLMToolCall{{ID: "search", Function: types.FunctionCall{Name: agenttools.ToolSearchKnowledge, Arguments: arguments}}}
			e.modelContext.DecodeToolCalls(calls)
			require.Contains(t, calls[0].UnresolvedHandles, "res://999")
			if tc.modelCopyOnly {
				calls[0].Function.Arguments, _, _ = splitCoverageArgument(calls[0].Function.Arguments)
			}
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "approved-reader", Role: types.TenantRoleViewer})
			e.processEvidenceCompletionCalls(ctx, "Read the equipment duration.", &types.AgentState{}, nil, calls)
			for _, args := range []string{calls[0].Function.Arguments, calls[0].ModelArguments} {
				require.JSONEq(t, fmt.Sprintf(`{"query":%q}`, tc.query), args)
			}
			require.Empty(t, e.evidenceCompletion.record.Bindings)
			require.Empty(t, e.evidenceCompletion.declared)
			require.Empty(t, e.evidenceCompletion.record.Attempts)
			checked := e.runToolCall(ctx, calls[0], 0, 0, 0, "session", "turn")
			require.NotNil(t, checked.Result)
			if tc.rejected {
				require.Equal(t, []string{"doc://999"}, calls[0].UnresolvedHandles)
				require.True(t, checked.Result.PreflightRejected)
				require.Contains(t, checked.Result.Error, "unresolved model handles")
				require.Zero(t, index.calls)
				return
			}
			require.Empty(t, calls[0].UnresolvedHandles)
			require.True(t, checked.Result.Success, checked.Result.Error)
			require.Equal(t, 1, index.calls)
		})
	}
}

func TestEvidenceCompletionOnlyScopeKeepsSourceCallsInsideBinding(t *testing.T) {
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", mode)
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.config.SearchTargets = types.SearchTargets{
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "library", TenantID: 7},
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "unrelated", TenantID: 7},
			}
			e.toolRegistry = qa4ReplayRegistry()
			e.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}},
				CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only"}}}}
			ctx := t.Context()
			state := &types.AgentState{}
			e.refreshEvidenceCompletion(ctx, "Use only the selected register.", state, nil)
			turn := &e.evidenceCompletion
			turn.nativeSelections = map[int][]coverageEvidence{0: {{SourceDomain: "structured", KnowledgeBaseID: "library", SourceID: "verified-register"}}}
			call := func(id, name, arguments string) types.LLMToolCall {
				return types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: name, Arguments: arguments}}
			}
			calls := []types.LLMToolCall{
				call("document", agenttools.ToolSearchKnowledge, `{"query":"duration","task_coverage":null}`),
				call("other-register", "structured_query_series", `{"knowledge_base_id":"library","dataset_id":"other-register","series_id":"duration","task_coverage":{"goal_index":0,"selection_ref":"unknown"}}`),
				call("same-id-other-library", "structured_query_series", `{"knowledge_base_id":"unrelated","dataset_id":"verified-register","series_id":"duration"}`),
				call("verified-register", "structured_query_series", `{"knowledge_base_id":"library","dataset_id":"verified-register","series_id":"duration","task_coverage":{"goal_index":0,"role":"supplement"}}`),
			}
			e.processEvidenceCompletionCalls(ctx, "Use only the selected register.", state, nil, calls)
			for i, candidate := range calls {
				_, args, metadata := splitCoverageArgument(candidate.Function.Arguments)
				require.Empty(t, metadata)
				require.Equal(t, i == 3, e.coverageSourceCallAllowed(ctx, candidate, args), candidate.ID)
				if i < 3 {
					result := e.runToolCall(ctx, candidate, i, 0, 0, "session", "turn").Result
					require.True(t, result.PreflightRejected)
					require.Equal(t, coverageScopeRejection, result.Error)
				}
			}
			require.False(t, e.coveragePrefetchSelection(nil)[agenttools.ToolSearchKnowledge])
		})
	}
}

func TestEvidenceCompletionPreservesAllRequiredFactReferences(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	start, end := 10, 900
	report := coverageEvidence{EvidenceID: "report", JSONPointer: "/data/chunks/0/content", SourceTitle: strings.Repeat("文", 240),
		SpanStart: &start, SpanEnd: &end, Raw: strings.Repeat("正文", 600)}
	first := coverageCompletion{GoalIndex: 0, PinnedFacts: []coverageFact{{Role: "supplement", Evidence: report}}}
	for i := 0; i < 20; i++ {
		first.PinnedFacts = append(first.PinnedFacts, coverageFact{Role: "directory", Evidence: coverageEvidence{
			EvidenceID: "directory", JSONPointer: fmt.Sprintf("/data/items/%d", i), SourceTitle: "directory",
		}})
	}
	last := coverageCompletion{GoalIndex: 1, PinnedFacts: []coverageFact{{Role: "last_valid_value", Evidence: coverageEvidence{
		EvidenceID: "original", JSONPointer: "/data/cells/4/raw_value", Raw: json.Number("13.75"),
		SourceTitle: "register", Date: "2021-06-10", Sheet: "sheet", RowLabel: "row", Column: "B", Cell: "B5", Period: "2021-06",
	}}}}
	engine.evidenceCompletion.result = []coverageCompletion{first, last}
	items := engine.coverageHandoffFacts()
	require.Len(t, items, 22)
	require.Equal(t, "report#/data/chunks/0/content@10:900", items[0].Ref)
	require.True(t, items[0].RawOmitted)
	require.True(t, items[0].LabelClipped)
	require.Equal(t, json.Number("13.75"), items[21].Raw)
	require.Equal(t, "original#/data/cells/4/raw_value", items[21].Ref)
	require.Equal(t, "B5", items[21].Address)
	require.Equal(t, "row", items[21].Row)
	require.Equal(t, "2021-06", items[21].Period)
}

func TestLegacyCoverageNeedsNewFactForRecoveredGoal(t *testing.T) {
	goal := 1
	original := coverageEvidence{Kind: "cell", SourceID: "register", KnowledgeBaseID: "library", SnapshotHash: "snapshot",
		Sheet: "observations", Cell: "B5", Period: "2021-06", EvidenceID: "first", ToolCallID: "first-call", Raw: json.Number("13")}
	turn := evidenceCompletionTurn{legacyGap: "recover-gap",
		legacyBaseline: map[string]bool{legacyCoverageFactIdentity(original): true}}
	result := func(index int, observation coverageEvidence) coverageCompletion {
		return coverageCompletion{GoalIndex: index, State: coverageSatisfied, PinnedFacts: []coverageFact{{Role: "scoped_value", Evidence: observation}}}
	}
	newValue := original
	newValue.EvidenceID, newValue.ToolCallID, newValue.Cell = "other-fact", "other-call", "B6"
	turn.result = []coverageCompletion{result(goal, newValue)}
	require.False(t, turn.legacyCoverageHasNewFact(), "a goal with no recovery association cannot cover the legacy gap")
	turn.legacyGoal = &goal

	repeated := original
	repeated.EvidenceID, repeated.ToolCallID = "new-id", "new-call"
	turn.result = []coverageCompletion{result(goal, repeated), result(goal+1, newValue)}
	require.False(t, turn.legacyCoverageHasNewFact(), "a replay or another goal cannot cover the legacy gap")

	turn.result = []coverageCompletion{result(goal, newValue)}
	require.True(t, turn.legacyCoverageHasNewFact())
}

func TestEvidenceCompletionOnlyComparisonUsesAuthorizedUISelections(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	const kb, first, second, third = "library", "selected-a", "selected-b", "other-c"
	for _, tc := range []struct {
		name, versionTask             string
		selectBoth, grantBoth, single bool
		allowSecond                   bool
	}{
		{name: "compare sources", versionTask: "compare_sources", selectBoth: true, grantBoth: true, allowSecond: true},
		{name: "compare versions", versionTask: "compare_versions", selectBoth: true, grantBoth: true, allowSecond: true},
		{name: "explicit single handle", versionTask: "compare_sources", selectBoth: true, grantBoth: true, single: true},
		{name: "second selection unauthorized", versionTask: "compare_versions", selectBoth: true},
		{name: "no UI selection keeps binding", versionTask: "compare_sources", grantBoth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.toolRegistry = qa4ReplayRegistry()
			if tc.grantBoth {
				e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb}}
			} else {
				e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: kb, KnowledgeIDs: []string{first}}}
			}
			if tc.selectBoth {
				e.selectedDocs = []*SelectedDocumentInfo{{KnowledgeBaseID: kb, KnowledgeID: first}, {KnowledgeBaseID: kb, KnowledgeID: second}}
			}
			target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "only", VersionTask: tc.versionTask, RestrictionBasis: "只依据所选资料",
			}}
			if tc.single {
				target.PrimaryRef = e.modelContext.RegisterDocument(first)
			}
			e.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"knowledge"}},
				CoverageTargets: []taskCoverageTarget{target}}
			e.evidenceCompletionEnabled()
			if !tc.selectBoth {
				e.evidenceCompletion.nativeSelections = map[int][]coverageEvidence{0: {{SourceDomain: "knowledge", SourceID: first, KnowledgeBaseID: kb}}}
			}
			for _, id := range []string{first, second, third} {
				require.Equal(t, id == first || id == second && tc.allowSecond,
					e.coverageEvidenceAllowed(t.Context(), &target, coverageEvidence{SourceDomain: "knowledge", SourceID: id, KnowledgeBaseID: kb}), id)
			}

			read := func(name, id string) types.LLMToolCall {
				return types.LLMToolCall{ID: name, Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: fmt.Sprintf(`{"id":%q}`, id)}}
			}
			search := func(name string, ids ...string) types.LLMToolCall {
				args, err := json.Marshal(map[string]any{"query": "duration", "document_ids": ids})
				require.NoError(t, err)
				return types.LLMToolCall{ID: name, Function: types.FunctionCall{Name: agenttools.ToolSearchKnowledge, Arguments: string(args)}}
			}
			calls := []types.LLMToolCall{
				read("first", first), read("second", second), read("third", third),
				search("compare-pair", first, second), search("extra-document", first, second, third),
			}
			e.processEvidenceCompletionCalls(t.Context(), "只依据所选资料", &types.AgentState{}, nil, calls)
			for i, call := range calls {
				_, args, _ := splitCoverageArgument(call.Function.Arguments)
				require.Equal(t, i == 0 || tc.allowSecond && (i == 1 || i == 3),
					e.coverageSourceCallAllowed(t.Context(), call, args), call.ID)
			}
		})
	}
}
