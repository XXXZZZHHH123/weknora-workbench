package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func progressChartCall(result *types.ToolResult) types.ToolCall {
	return types.ToolCall{Name: "render_chart", Result: result}
}

func TestToolProgressBoundsChartAndStructuredFailuresByActualOutcome(t *testing.T) {
	for _, name := range []string{"render_chart", "structured_analyze_table", "structured_query_series", "structured_query_pdf_table"} {
		t.Run(name, func(t *testing.T) {
			var guard toolProgressGuard
			for round := 1; round <= toolProgressStopRounds; round++ {
				// Changing validation/transient diagnostics and parameters does
				// not establish progress. The existing grace window permits a
				// repaired operation or an unchanged transient retry to succeed.
				call := types.ToolCall{Name: name, ID: fmt.Sprint(round), Args: map[string]any{"title": fmt.Sprint(round)}, Result: &types.ToolResult{
					Success: round%2 == 0, Error: fmt.Sprintf("diagnostic %d", round),
					Data: map[string]any{"status": "error", "retryable": true, "reason_code": fmt.Sprint(round)},
				}}
				result := guard.observe([]types.ToolCall{call})
				require.Zero(t, result.NewEvidence)
				require.Equal(t, round, result.StaleRounds)
				require.Equal(t, round == toolProgressReplanRounds, result.Replan)
				require.Equal(t, round == toolProgressStopRounds, result.Stop)
				require.True(t, result.ResultWorkflow)
			}
		})
	}
}

func TestToolProgressActualChartInspectAndEquivalentMapCannotHideFailureLoop(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{v2ChartFixture(t)}}}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	sources, _ := chartSources(steps)
	require.Len(t, sources, 1)
	inspect := chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	mapping := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": sources[0].ID,
		"mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{0, 1}}})
	require.True(t, mapping.Success, mapping.Error)
	var guard toolProgressGuard
	require.Positive(t, guard.observe([]types.ToolCall{progressChartCall(inspect)}).NewEvidence)
	require.Positive(t, guard.observe([]types.ToolCall{progressChartCall(mapping)}).NewEvidence)
	// This full-width map exposes exactly the original cells in the directory;
	// the new mapped handle must not make that catalog entry new again.
	inspect = chartRun(t, tool, ctx, map[string]any{"operation": "inspect"})
	repeatedCatalog := guard.observe([]types.ToolCall{progressChartCall(inspect)})
	require.Zero(t, repeatedCatalog.NewEvidence)
	require.Equal(t, 1, repeatedCatalog.StaleRounds)
	before := reviewDigest([]any{inspect.Data, mapping.Data})
	for round := 2; round <= toolProgressStopRounds; round++ {
		failed := progressChartCall(&types.ToolResult{Error: "The corrected plan has not succeeded", Data: map[string]any{"status": "error"}})
		result := guard.observe([]types.ToolCall{failed, progressChartCall(inspect), progressChartCall(mapping)})
		require.Zero(t, result.NewEvidence)
		require.Equal(t, round, result.StaleRounds)
		require.Equal(t, round == toolProgressStopRounds, result.Stop)
	}
	require.Equal(t, before, reviewDigest([]any{inspect.Data, mapping.Data}), "progress must preserve chart receipts and source identities")
	changed := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": sources[0].ID,
		"mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{1}}})
	require.True(t, changed.Success, changed.Error)
	recovered := guard.observe([]types.ToolCall{progressChartCall(changed)})
	require.Positive(t, recovered.NewEvidence, "a genuinely new executed source selection remains progress")
	require.Zero(t, recovered.StaleRounds)
	require.False(t, recovered.Stop)
}

func TestToolProgressNewExecutionHandlesAndEquivalentMappingChainsAreNotNewCells(t *testing.T) {
	ctx := chartContext(t)
	call := v2ChartFixture(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	sources, _ := chartSources(steps)
	mapSource := func(id string) *types.ToolResult {
		result := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": id,
			"mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{0, 1}}})
		require.True(t, result.Success, result.Error)
		return result
	}
	first := mapSource(sources[0].ID)
	var guard toolProgressGuard
	require.Positive(t, guard.observe([]types.ToolCall{progressChartCall(first)}).NewEvidence)
	repeated := mapSource(first.Data["source_id"].(string))
	require.NotEqual(t, first.Data["source_id"], repeated.Data["source_id"])
	require.Zero(t, guard.observe([]types.ToolCall{progressChartCall(repeated)}).NewEvidence)
	steps[0].ToolCalls[0].ID = "another-read-of-the-same-source"
	newSources, _ := chartSources(steps)
	require.NotEqual(t, sources[0].ID, newSources[0].ID)
	again := mapSource(newSources[0].ID)
	require.Zero(t, guard.observe([]types.ToolCall{progressChartCall(again)}).NewEvidence)
	// An otherwise equal table from a new original file revision is distinct.
	changed := toolProgressCopy(again.Data)
	// Test the genuine root snapshot separately without assuming fixture hashes.
	changed["provenance"] = `{"source":{"sha256":"` + strings.Repeat("e", 64) + `","file":"source.pdf"}}`
	require.Positive(t, guard.observe([]types.ToolCall{progressChartCall(&types.ToolResult{Success: true, Data: changed})}).NewEvidence)
}

func TestToolProgressStructuredContractsIgnoreSQLSpellingButKeepRealValuesAndUnits(t *testing.T) {
	first := v2ChartFixture(t)
	first.Name = "structured_analyze_table"
	var guard toolProgressGuard
	before := reviewDigest(first.Result.Data)
	require.Positive(t, guard.observe([]types.ToolCall{first}).NewEvidence)
	require.Equal(t, before, reviewDigest(first.Result.Data))
	second := v2ChartFixture(t)
	second.Name, second.ID = first.Name, "another-execution"
	data := second.Result.Data
	data["sql"] = data["sql"].(string) + "\n "
	contract := data["result_contract"].(map[string]any)
	contract["result_id"] = strings.Repeat("b", 24)
	data["result_id"] = contract["result_id"]
	hash := sha256.Sum256([]byte(data["sql"].(string)))
	contract["execution"].(map[string]any)["sql_sha256"] = hex.EncodeToString(hash[:])
	rehashLightFixture(t, data)
	require.NotEqual(t, first.Result.Data["result_contract"], contract)
	require.Zero(t, guard.observe([]types.ToolCall{second}).NewEvidence, "reissued equivalent SQL is not a new source result")
	gateway := second
	gateway.Name, gateway.Args = "structured_data", map[string]any{"operation": "analyze_table"}
	require.Zero(t, guard.observe([]types.ToolCall{gateway}).NewEvidence, "native legacy and flat executions share accounting")
	for round := 1; round < toolProgressStopRounds; round++ {
		guard.observe([]types.ToolCall{{Name: "render_chart", Result: &types.ToolResult{Error: "Not yet rendered"}}})
	}
	data["rows"].([]any)[0].([]any)[1] = "17.250"
	// An altered payload without its witness is not successful new evidence.
	require.Zero(t, guard.observe([]types.ToolCall{second}).NewEvidence)
	rehashLightFixture(t, data)
	recovered := guard.observe([]types.ToolCall{second})
	require.Positive(t, recovered.NewEvidence)
	require.Zero(t, recovered.StaleRounds)
	contract["column_metadata"].([]any)[1].(map[string]any)["unit"].(map[string]any)["reason"] = "Actual revised unit limitation"
	rehashLightFixture(t, data)
	require.Positive(t, guard.observe([]types.ToolCall{second}).NewEvidence)
	for i := 0; i < toolProgressStopRounds+2; i++ {
		data["rows"].([]any)[0].([]any)[1] = fmt.Sprintf("%d.250", 20+i)
		rehashLightFixture(t, data)
		result := guard.observe([]types.ToolCall{second})
		require.Positive(t, result.NewEvidence)
		require.False(t, result.Stop, "a long valid analysis with new actual results remains allowed")
	}
}

func TestToolProgressEqualValuesFromDifferentOriginalMapCellsRemainNew(t *testing.T) {
	source := chart.Table{Columns: []chart.Column{{Name: "category"}, {Name: "amount", Unit: "kg"}},
		Rows: [][]string{{"A", "7"}, {"A", "7"}}, Provenance: `{"source":{"file":"source.csv","sha256":"` + strings.Repeat("a", 64) + `"}}`}
	require.NoError(t, source.Seal("original-read"))
	x := 0
	var guard toolProgressGuard
	for _, row := range []int{0, 1, 0} {
		mapped, err := mapChartSource(source, chartSourceMapping{Orientation: "columns", XIndex: &x, YIndexes: []int{1}, ValueIndexes: []int{row}})
		require.NoError(t, err)
		call := progressChartCall(&types.ToolResult{Success: true, Data: map[string]any{"kind": "chart_source_mapping", "source_id": mapped.ID,
			"columns": mapped.Columns, "rows": mapped.Rows, "source_truncated": mapped.Truncated, "provenance": mapped.Provenance}})
		observation := guard.observe([]types.ToolCall{call})
		if len(guard.seen) < 2 || row == 1 {
			require.Positive(t, observation.NewEvidence)
		} else {
			require.Zero(t, observation.NewEvidence, "revisiting the first equal-valued cell is still a repeat")
		}
	}
}

func TestToolProgressStructuredRowsDoNotLoseFieldsNamedLikeTransport(t *testing.T) {
	call := types.ToolCall{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"status": "ok", "columns": []string{"notice", "result_id"}, "rows": []any{map[string]any{"notice": "original", "result_id": "real-source-value"}},
	}}}
	var guard toolProgressGuard
	require.Positive(t, guard.observe([]types.ToolCall{call}).NewEvidence)
	call.Result.Data["notice"] = "Changed only runtime advice"
	require.Zero(t, guard.observe([]types.ToolCall{call}).NewEvidence)
	call.Result.Data["rows"].([]any)[0].(map[string]any)["notice"] = "changed actual cell"
	require.Positive(t, guard.observe([]types.ToolCall{call}).NewEvidence)
}

func TestToolProgressCatalogOrderAndTransientHandlesDoNotCreateProgress(t *testing.T) {
	call := types.ToolCall{Name: "structured_list_series", Result: &types.ToolResult{Success: true, Data: map[string]any{
		"dataset_id": "actual-source", "series": []any{map[string]any{"series_id": "A", "unit": "kg"}, map[string]any{"series_id": "B", "unit": "m"}},
		"total": 2, "offset": 0, "next_offset": nil,
	}}}
	var guard toolProgressGuard
	first := guard.observe([]types.ToolCall{call})
	require.Positive(t, first.NewEvidence)
	require.False(t, first.ResultWorkflow, "metadata alone is not a result workflow")
	rows := call.Result.Data["series"].([]any)
	rows[0], rows[1] = rows[1], rows[0]
	call.Result.Data["search"], call.Result.Data["notice"] = "different words", "another message"
	call.Result.Data["_structured_execution"] = map[string]any{"generation": 99}
	require.Zero(t, guard.observe([]types.ToolCall{call}).NewEvidence)
	rows[0].(map[string]any)["unit"] = "cm"
	require.Positive(t, guard.observe([]types.ToolCall{call}).NewEvidence)
}

func TestToolProgressDoesNotClaimAsyncWorkOrConfirmationIsAStalledSourceRead(t *testing.T) {
	for _, name := range []string{"external_job", "structured_confirm_fields", "structured_execute_field_plan", "structured_custom_job"} {
		var guard toolProgressGuard
		for i := 0; i < 2*toolProgressStopRounds; i++ {
			observation := guard.observe([]types.ToolCall{{Name: name, Result: &types.ToolResult{Success: true,
				Data: map[string]any{"status": "running", "job_id": "actual-job", "progress": i}}}})
			require.False(t, observation.Stop)
			require.False(t, observation.ResultWorkflow)
			require.Zero(t, observation.StaleRounds)
		}
	}
}

func TestToolProgressSuccessfulChartOutputCountsOnceWithoutMutatingReceipt(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{v2ChartFixture(t)}}}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	sources, _ := chartSources(steps)
	render := func(title string) *types.ToolResult {
		result := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": sources[0].ID,
			"chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}, "title": title})
		require.True(t, result.Success, result.Error)
		return result
	}
	first := render("First title")
	before := reviewDigest(first.Data)
	var guard toolProgressGuard
	require.Positive(t, guard.observe([]types.ToolCall{progressChartCall(first)}).NewEvidence)
	require.Equal(t, before, reviewDigest(first.Data))
	repeated := render("Different wording for exactly the same plotted data")
	require.Zero(t, guard.observe([]types.ToolCall{progressChartCall(repeated)}).NewEvidence)
	invalid := toolProgressCopy(first.Data)
	invalid["artifact"] = map[string]any{"handle": "not-a-persisted-resource"}
	require.Zero(t, guard.observe([]types.ToolCall{progressChartCall(&types.ToolResult{Success: true, Data: invalid})}).NewEvidence)
}

type failedProgressChartTool struct {
	agenttools.BaseTool
	calls int
}

func (tool *failedProgressChartTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return &types.ToolResult{Error: "The actual selected columns have different declared units."}, nil
}

func TestChartRenderLimitEntersCheckedTextSynthesisForEveryEntry(t *testing.T) {
	for _, workbench := range []bool{true, false} {
		t.Run(fmt.Sprint(workbench), func(t *testing.T) {
			var replies []mockResponse
			for round := 0; round < maxChartRenderFailures; round++ {
				replies = append(replies, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{
					ID: fmt.Sprintf("chart-%d", round), Type: "function", Function: types.FunctionCall{Name: renderChartToolName, Arguments: `{"operation":"render"}`},
				}}}}})
			}
			const answer = "The observation is 12.500 kg. The chart could not be generated because the selected columns have different declared units."
			verdict := sourceAcceptedVerdict()
			if !workbench {
				verdict = completenessVerdict(true)
			}
			replies = append(replies, chatAnswer(answer), verdict)
			e, state, model := lightStructuredFixture(t, replies...)
			e.config.WorkbenchDefault, e.config.MaxIterations = workbench, 50
			tool := &failedProgressChartTool{BaseTool: agenttools.NewBaseTool(renderChartToolName, "isolated render failure", json.RawMessage(`{"type":"object"}`))}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(tool)
			_, err := e.executeLoop(t.Context(), state, "Report the observation and chart outcome.", emptyMessages(), nil, "session", "message")
			require.NoError(t, err)
			require.Equal(t, maxChartRenderFailures, tool.calls)
			require.Equal(t, answer, state.FinalAnswer)
			require.Equal(t, "none", model.opts[maxChartRenderFailures].ToolChoice)
			require.Empty(t, model.opts[maxChartRenderFailures].Tools)
			require.Contains(t, model.opts[maxChartRenderFailures].RuntimeNotice, "chart render failure limit")
			require.True(t, e.candidateApprovalCurrent(answer, state))
		})
	}
}

// A provider is allowed to close the channel asynchronously. The engine must
// still respect the final phase deadline without waiting for that close.
type openFinalStreamChat struct{ mockChat }

func (model *openFinalStreamChat) ChatStream(context.Context, []chat.Message, *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	return make(chan types.StreamResponse), nil
}

func TestToolStopSynthesisDeadlineCommitsBodyOnLiveParent(t *testing.T) {
	e := newTestEngine(t, &openFinalStreamChat{})
	state := &types.AgentState{}
	ctx := context.WithValue(t.Context(), toolStopSynthesisDeadlineKey{}, time.Now().Add(40*time.Millisecond))
	err := e.handleMaxIterations(ctx, "Summarize successful observations.", state, "session", emptyMessages())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, ctx.Err())
	require.True(t, state.IsComplete)
	require.NotEmpty(t, state.FinalAnswer)
	requireTerminalEvidenceGap(t, state.FinalAnswer)
}

type continuousFinalStreamChat struct {
	mockChat
	stop chan struct{}
}

func (model *continuousFinalStreamChat) ChatStream(_ context.Context, _ []chat.Message, _ *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	stream := make(chan types.StreamResponse, 256)
	go func() {
		defer close(stream)
		for {
			select {
			case stream <- types.StreamResponse{ResponseType: types.ResponseTypeThinking, Content: "tick"}:
			case <-model.stop:
				return
			}
		}
	}()
	return stream, nil // Intentionally ignores context until test cleanup.
}

func TestFinalSynthesisDeadlineDoesNotDrainUnboundedProviderOutput(t *testing.T) {
	model := &continuousFinalStreamChat{stop: make(chan struct{})}
	t.Cleanup(func() { close(model.stop) })
	e := newTestEngine(t, model)
	state := &types.AgentState{}
	ctx := context.WithValue(t.Context(), toolStopSynthesisDeadlineKey{}, time.Now().Add(20*time.Millisecond))
	start := time.Now()
	err := e.handleMaxIterations(ctx, "Deliver the actual findings.", state, "session", emptyMessages())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
	require.NotEmpty(t, state.FinalAnswer)
}

func TestToolProgressRenderFailureLimitSurvivesEvidenceSaturation(t *testing.T) {
	guard := toolProgressGuard{saturated: true}
	for i := 0; i < maxChartRenderFailures; i++ {
		result := guard.observe([]types.ToolCall{{Name: renderChartToolName, Args: map[string]any{"operation": "render"}, Result: &types.ToolResult{Error: "invalid actual columns"}}})
		require.False(t, result.Stop, "accounting saturation is not a stall")
		require.Equal(t, i+1 >= maxChartRenderFailures, result.RenderLimit)
	}
}

func TestToolProgressSuccessfulRenderDoesNotConsumeFailureAllowance(t *testing.T) {
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{v2ChartFixture(t)}}}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, &chartWriterFixture{}, "session", "message")
	sources, _ := chartSources(steps)
	saved := chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": sources[0].ID, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}})
	require.True(t, saved.Success, saved.Error)
	guard := toolProgressGuard{renderFailures: maxChartRenderFailures - 1}
	call := progressChartCall(saved)
	call.Args = map[string]any{"operation": "render"}
	require.False(t, guard.observe([]types.ToolCall{call}).RenderLimit)
	failed := progressChartCall(&types.ToolResult{Error: "different invalid selection"})
	failed.Args = call.Args
	require.True(t, guard.observe([]types.ToolCall{failed}).RenderLimit, "a saved chart does not replenish the cumulative failure allowance")
}
