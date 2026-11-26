package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

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
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	t.Run("available", func(t *testing.T) { runEvidenceCompletionNamedFirst(t, true) })
	t.Run("unavailable", func(t *testing.T) { runEvidenceCompletionNamedFirst(t, false) })
}

func runEvidenceCompletionNamedFirst(t *testing.T, documentRoute bool) {
	t.Helper()
	const (
		query = "Use the equipment register first for the duration on 2021-06-10."
		date  = "2021-06-10"
		body  = "The test pump's elapsed operating time on 2021-06-10 was thirteen hours."
	)
	model := &mockChat{}
	engine := newTestEngine(t, model)
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
	source := map[string]any{"sha256": strings.Repeat("b", 64)}
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
	const limitation = "The equipment register has no matching date."
	run(draft(limitation, declare(ref("directory"), "primary", "", nil)))
	first, ok := engine.evidenceCompletionGap()
	require.True(t, ok)
	if !documentRoute {
		require.Equal(t, coverageExhausted, first.State)
		require.Equal(t, "no_authorized_supplemental_route", first.Reason)
		require.Equal(t, candidateEvidencePartial, engine.turnReview.Last.Disposition)
		require.False(t, engine.candidateApprovalCurrent(state.FinalAnswer, state))
		require.True(t, state.IsComplete)
		require.Equal(t, limitation, state.FinalAnswer)
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
	run(mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "tool_calls",
		ToolCalls: []types.LLMToolCall{
			wrong("repeated-series", "structured_list_series", map[string]any{"dataset_id": "dataset"}),
			wrong("document-directory", agenttools.ToolListDocuments, map[string]any{}),
			wrong("blank-search", agenttools.ToolSearchKnowledge, map[string]any{"query": "  "}),
		},
	}}})
	rejected := state.RoundSteps[len(state.RoundSteps)-1].ToolCalls
	require.Len(t, rejected, 3)
	for _, call := range rejected {
		require.NotNil(t, call.Result, call.ID)
		require.True(t, call.Result.PreflightRejected, call.ID)
	}
	require.Zero(t, index.calls)
	require.Empty(t, engine.evidenceCompletion.record.Attempts, "a rejected directory or missing query does not spend discovery")
	require.NotNil(t, engine.evidenceCompletion.pending)
	require.Equal(t, first.GapID, engine.evidenceCompletion.pending.GapID)
	require.Equal(t, "discover", engine.evidenceCompletion.pending.NextRead)

	run(chatToolCall("targeted-search", agenttools.ToolSearchKnowledge, wire(map[string]any{
		"query": "equipment duration 2021-06-10", coverageArgument: declare("", "supplement", first.GapID, nil),
	})))
	require.Equal(t, 1, index.calls)
	navigation := ref("navigation")
	handle := engine.modelContext.RegisterDocument(document.ID)
	require.True(t, strings.HasPrefix(handle, "doc://"))
	sourceScope := map[string]any{"selection_reason": "The authorized search led to the requested report and page.",
		"sources": []any{map[string]any{"knowledge_id": handle, "expected_file_name": document.FileName, "page": 8}}}
	run(chatToolCall("matching-read", agenttools.ToolReadDocument, wire(map[string]any{
		"id": handle, "page": 8, "source_scope": sourceScope, coverageArgument: declare(navigation, "supplement", first.GapID, map[string]any{"page": 8}),
	})))
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

	const answer = "The register has no matching date. The inspection report gives a duration of thirteen hours."
	run(draft(answer, bodyClaim(subject, metric)))
	require.True(t, state.IsComplete)
	require.Equal(t, answer, state.FinalAnswer)
	require.Equal(t, candidateAccepted, engine.turnReview.Last.Disposition)
	require.Len(t, engine.evidenceCompletion.record.Attempts, 2)
	require.Equal(t, []string{"discover", "read"}, []string{engine.evidenceCompletion.record.Attempts[0].Action, engine.evidenceCompletion.record.Attempts[1].Action})
	require.Equal(t, coverageSatisfied, engine.evidenceCompletion.result[0].State)
	require.Equal(t, "matching_supplement", engine.evidenceCompletion.result[0].Reason)
	require.Contains(t, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Raw, metric)
	require.Equal(t, subject, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Subject)
	require.Equal(t, metric, engine.evidenceCompletion.result[0].PinnedFacts[1].Evidence.Metric)
	require.Len(t, model.calls, 5)
}

func TestEvidenceCompletionOnlyScopeKeepsSourceCallsInsideBinding(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	e := newTestEngine(t, &mockChat{})
	e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
	e.toolRegistry = qa4ReplayRegistry()
	e.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}},
		CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only"}}}}
	e.evidenceCompletionEnabled()
	e.evidenceCompletion.record.Bindings = []coverageBinding{{GoalIndex: 0, Role: "primary", SourceID: "verified-register"}}
	ctx := t.Context()
	state := &types.AgentState{}
	call := func(id, name, arguments string) types.LLMToolCall {
		return types.LLMToolCall{ID: id, Function: types.FunctionCall{Name: name, Arguments: arguments}}
	}
	calls := []types.LLMToolCall{
		call("document", agenttools.ToolSearchKnowledge, `{"query":"duration"}`),
		call("other-register", "structured_query_series", `{"dataset_id":"other-register","series_id":"duration"}`),
		call("verified-register", "structured_query_series", `{"dataset_id":"verified-register","series_id":"duration"}`),
	}
	e.processEvidenceCompletionCalls(ctx, "Use only the selected register.", state, nil, calls)
	require.Len(t, e.evidenceCompletion.blocked, 2)
	require.Contains(t, e.evidenceCompletion.blocked, "document")
	require.Contains(t, e.evidenceCompletion.blocked, "other-register")
	require.NotContains(t, e.evidenceCompletion.blocked, "verified-register")
	require.False(t, e.coveragePrefetchSelection(nil)[agenttools.ToolSearchKnowledge])
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
			e.evidenceCompletion.record.Bindings = []coverageBinding{{Role: "primary", SourceID: first, KnowledgeBaseID: kb}}
			for _, id := range []string{first, second, third} {
				require.Equal(t, id == first || id == second && tc.allowSecond,
					e.coverageMatchesPinnedTarget(target, coverageEvidence{SourceID: id, KnowledgeBaseID: kb}), id)
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
			require.NotContains(t, e.evidenceCompletion.blocked, "first")
			require.Contains(t, e.evidenceCompletion.blocked, "third")
			require.Contains(t, e.evidenceCompletion.blocked, "extra-document")
			_, blockedSecond := e.evidenceCompletion.blocked["second"]
			_, blockedPair := e.evidenceCompletion.blocked["compare-pair"]
			require.Equal(t, !tc.allowSecond, blockedSecond)
			require.Equal(t, !tc.allowSecond, blockedPair)
		})
	}
}
