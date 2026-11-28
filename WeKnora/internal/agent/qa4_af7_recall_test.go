package agent

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type af7RecallArchive struct {
	Point           af5SupplementTrace
	PointPublished  string
	PointReadChunks map[string][]string
	PointBinding    json.RawMessage
	LastValid       af6LastValidTrace
}

// Tool arguments and receipts are from the first Af6 run (KB-31 events 3–25
// and KB-16 events 4–15). The log truncates both plans, so the coverage targets
// retain the prior replay shape and use the observed queries and selections.
func loadAf7RecallArchive(t *testing.T) af7RecallArchive {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/qa4_af7_recall.json.gz")
	require.NoError(t, err)
	defer file.Close()
	body, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.UseNumber()
	var archive af7RecallArchive
	require.NoError(t, decoder.Decode(&archive))
	return archive
}

func TestQA4Af7GapKnowledgeRecallFlags(t *testing.T) {
	for _, test := range []struct {
		name, recall, scoped, disabledParent string
		wantRecall, wantScoped               bool
	}{
		{name: "default", wantScoped: true},
		{name: "malformed opt-in", recall: "yes", wantScoped: true},
		{name: "explicit opt-out", recall: "false", wantScoped: true},
		{name: "explicit opt-in", recall: "true", wantRecall: true, wantScoped: true},
		{name: "legacy recall", recall: "true", scoped: "false", wantRecall: true},
		{name: "both off", recall: "false", scoped: "false"},
		{name: "publication off", recall: "true", disabledParent: "WEKNORA_AGENT_VERIFIED_PUBLICATION"},
		{name: "relaxed evidence off", recall: "true", disabledParent: "WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE"},
		{name: "final read off", recall: "true", disabledParent: "WEKNORA_AGENT_FINAL_READ_ANSWER"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "true")
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", test.recall)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", test.scoped)
			if test.disabledParent != "" {
				t.Setenv(test.disabledParent, "false")
			}
			require.Equal(t, test.wantRecall, gapKnowledgeRecallEnabled())
			require.Equal(t, test.wantScoped, scopedReadRecoveryEnabled())
		})
	}
}

func TestQA4Af7Af6PointRecallReplay(t *testing.T) {
	for _, test := range []struct {
		name, recall, scoped string
		prebind              bool
	}{
		{name: "default off"},
		{name: "explicit on", recall: "true"},
		{name: "Af5 recall", recall: "true", scoped: "false"},
		{name: "bound default off", prebind: true},
		{name: "bound both off", recall: "false", scoped: "false", prebind: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			configureAf5Publication(t, true)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "true")
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", test.recall)
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", test.scoped)
			archive := loadAf7RecallArchive(t)
			trace := archive.Point
			data := loadAf5SupplementFixture(t) // Same index query and page chunk IDs as Af6.
			engine, state, index, model, ctx := af6PointSetup(t, data, trace, false)
			require.Equal(t, "no_data", trace.Steps[2].ToolCalls[0].Result.Data["status"])
			if test.prebind {
				binder, err := engine.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
				require.NoError(t, err)
				result, err := binder.Execute(ctx, archive.PointBinding)
				require.NoError(t, err)
				require.True(t, result.Success, result.Error)
				require.Len(t, agenttools.CurrentDocumentScopeIDs(engine.toolRegistry), len(archive.PointReadChunks))
				record, attempts := engine.terminalCoverageRecord(ctx, state, true)
				require.Empty(t, engine.finalSupplementReadCalls(ctx, state, record, attempts, finalCoverageReadLimit))
			}
			start := len(state.RoundSteps)
			af6PointAccept(t, engine, state, ctx, trace.Answer)
			if !test.prebind && test.scoped != "false" {
				_, binding := af3CollaborationReplayStore(t)
				engine.SetCollaboration(binding)
				read := engine.recoverScopedFinalReadingsBeforeClose(ctx, state, binding.Scope.SessionID, binding.Scope.ID)
				require.Equal(t, test.recall == "true", read)
			}
			_, answer := af6PointCommit(t, engine, state, ctx, trace.Answer)
			var added []types.ToolCall
			for _, step := range state.RoundSteps[start:] {
				added = append(added, step.ToolCalls...)
			}
			require.Zero(t, model.callCount)
			require.Contains(t, strings.ReplaceAll(answer, ",", ""), "5160")
			require.Contains(t, answer, "B3340")
			if test.recall != "true" {
				require.Empty(t, added)
				require.Zero(t, index.calls)
				require.NotContains(t, answer, "60.6")
				require.NotContains(t, answer, "62.3")
				return
			}
			require.Equal(t, 1, index.calls)
			require.Len(t, added, 4)
			require.Equal(t, agenttools.ToolSearchKnowledge, added[0].Name)
			require.Equal(t, agenttools.ToolBindDocumentScope, added[1].Name)
			for _, read := range added[2:] {
				require.Equal(t, agenttools.ToolReadDocument, read.Name)
				require.True(t, read.Result.Success, read.Result.Error)
				id := sourceNavigationString(read.Args, "id")
				require.Contains(t, archive.PointReadChunks, id)
				require.ElementsMatch(t, archive.PointReadChunks[id], reviewAsSlice(read.Result.Data["chunk_ids"]))
			}
			if test.scoped != "false" {
				require.Contains(t, archive.PointPublished, "60.6万吨")
				require.Contains(t, archive.PointPublished, "62.3万吨")
				require.Contains(t, answer, "60.6万吨")
				require.Contains(t, answer, "62.3万吨")
			}
		})
	}
}

func TestQA4Af7ModelSelectedDocumentToolsWithRecallOff(t *testing.T) {
	configureAf5Publication(t, true)
	t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "true")
	t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "false")
	t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
	archive := loadAf7RecallArchive(t)
	trace := archive.Point
	data := loadAf5SupplementFixture(t)
	engine, state, index, model, ctx := af6PointSetup(t, data, trace, false)
	messages := slices.Clone(engine.turnReview.Conversation)
	guards := &loopGuards{}
	execute := func(name string, args json.RawMessage) types.ToolCall {
		t.Helper()
		id := fmt.Sprintf("model-%d", len(model.responses))
		model.responses = append(model.responses, chatToolCall(id, name, string(args)))
		state.CurrentRound++
		roundCtx := ctx
		if name == agenttools.ToolSearchKnowledge {
			// Use the local keyword index to keep the fixture offline.
			roundCtx = agenttools.WithLocalDocumentRecall(ctx)
		}
		outcome, err := engine.runReActIteration(roundCtx, state, &messages, engine.buildToolsForLLM(), "session", "turn", trace.Question, guards)
		require.NoError(t, err)
		require.Equal(t, iterOutcomeNext, outcome)
		calls := state.RoundSteps[len(state.RoundSteps)-1].ToolCalls
		require.Len(t, calls, 1)
		call := calls[0]
		require.Equal(t, id, call.ID)
		require.Equal(t, name, call.Name)
		require.True(t, call.Result.Success, call.Result.Error)
		return call
	}
	searchArgs, err := json.Marshal(agenttools.SearchKnowledgeInput{
		Query: data.Query, Mode: agenttools.SearchModeKeyword, KnowledgeBaseIDs: trace.Libraries, Limit: 12,
	})
	require.NoError(t, err)
	execute(agenttools.ToolSearchKnowledge, searchArgs)
	execute(agenttools.ToolBindDocumentScope, archive.PointBinding)
	for _, id := range slices.Sorted(maps.Keys(archive.PointReadChunks)) {
		pages := agenttools.CurrentDocumentReadPages(engine.toolRegistry, id)
		require.Len(t, pages, 1)
		args, err := json.Marshal(agenttools.ReadDocumentInput{ID: id, Page: pages[0]})
		require.NoError(t, err)
		read := execute(agenttools.ToolReadDocument, args)
		require.ElementsMatch(t, archive.PointReadChunks[id], reviewAsSlice(read.Result.Data["chunk_ids"]))
	}
	require.Equal(t, 1, index.calls)
	require.Equal(t, 4, model.callCount)
	require.False(t, engine.turnReview.FinalCoverageReadAttempted)
	af6PointAccept(t, engine, state, ctx, trace.Answer)
	added, answer := af6PointCommit(t, engine, state, ctx, trace.Answer)
	require.Empty(t, added)
	require.Contains(t, answer, "60.6万吨")
	require.Contains(t, answer, "62.3万吨")
}

func TestQA4Af7Af6LastValidReplayWithRecallOff(t *testing.T) {
	for _, test := range []struct {
		name, scoped string
		missing      bool
		wantReads    int
	}{
		{name: "recorded endpoint"},
		{name: "endpoint omitted", missing: true, wantReads: 1},
		{name: "scoped recovery disabled", scoped: "false", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setAf5StructuredReplayFlags(t, true)
			t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", "true")
			t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "false")
			t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", test.scoped)
			trace := loadAf7RecallArchive(t).LastValid
			good := trace.Steps[2].ToolCalls[0]
			var considered []string
			for _, field := range sourceNavigationRows(trace.Steps[1].ToolCalls[0].Result.Data["series"]) {
				considered = append(considered, sourceNavigationString(field, "series_id"))
			}
			if test.missing {
				// The recorded tool already asked for the endpoint. Exercise the
				// same series and window with only the known endpoint date.
				trace.Steps[2] = loadAf7RecallArchive(t).LastValid.Steps[2]
				withoutEndpoint := trace.Steps[2].ToolCalls[0]
				delete(withoutEndpoint.Args, "include_last_valid")
				withoutEndpoint.Result.Data["coverage"] = nil
				rehashLightFixture(t, withoutEndpoint.Result.Data)
			}
			library := af5ReplayLibrary(t, trace.Steps)
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
			var reads int
			registry := af6LastValidRegistry(t, targets, trace.Question, good, considered, nil, &reads, nil)
			engine, state, model, ctx, messages := prepareAf5StructuredTurn(t, af5StructuredTrace{
				Question: trace.Question, Targets: trace.Plan.CoverageTargets, Steps: trace.Steps,
			}, targets, registry, false)
			engine.taskPlan = &trace.Plan
			engine.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
			candidate := trace.Final
			if test.missing {
				candidate = "棉花数据库.xlsx 的郑棉期货收盘价在 2024 年 3 月没有记录。"
			}
			af6PointAccept(t, engine, state, ctx, candidate)
			start := len(state.RoundSteps)
			answer, _ := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				engine.commitTerminalResult(ctx, state, "recorded", candidate, "", "answer", false)
			})
			require.Equal(t, test.wantReads, reads)
			require.Len(t, state.RoundSteps, start+test.wantReads)
			require.Zero(t, model.callCount)
			require.NotEmpty(t, answer)
			if test.missing && test.wantReads == 0 {
				require.NotContains(t, answer, "17110")
				return
			}
			require.Contains(t, answer, "17110")
			require.Contains(t, answer, "B4705")
			require.Regexp(t, `2023(?:-09-22|年9月22日)`, answer)
		})
	}
}
