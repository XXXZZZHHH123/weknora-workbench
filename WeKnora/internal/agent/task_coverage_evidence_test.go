package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func coverageSourceFixture(t *testing.T, operation, arguments, result string) types.ToolCall {
	t.Helper()
	args, err := decodeCitationJSON(arguments)
	require.NoError(t, err)
	data, err := decodeCitationJSON(result)
	require.NoError(t, err)
	data["source_context"] = map[string]any{"knowledge_base_id": "authorized-library"}
	data["_structured_execution"] = agenttools.StructuredOperationDescriptor{
		Operation: operation, ToolName: "structured_" + operation, EffectClass: "read", ScopeMode: "knowledge_base",
	}
	return types.ToolCall{ID: operation, Name: "structured_" + operation, Args: args, Result: &types.ToolResult{Success: true, Data: data}}
}

func (e *AgentEngine) taskCoverageEvidenceBatches(state *types.AgentState) []coverageEvidenceBatch {
	var batches []coverageEvidenceBatch
	seen := map[string]bool{}
	for _, step := range state.RoundSteps {
		batch := e.taskCoverageBatch(step, state, seen)
		if len(batch.Attempts) != 0 {
			batches = append(batches, batch)
		}
	}
	return batches
}

func TestTaskCoverageEvidenceBatchNativeReplay(t *testing.T) {
	registry := qa4ReplayRegistry()
	engine := &AgentEngine{toolRegistry: registry}
	const args = `{"dataset_id":"dataset","series_id":"source-field","start_date":"2025-02-01","end_date":"2025-02-01"}`
	const data = `{
		"dataset_id":"dataset","status":"no_data","valid_count":0,"truncated":false,
		"source":{"file":"source.xlsx","knowledge_id":"document","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sheet":"sheet"},
		"series":{"series_id":"source-field","metric":"measure","last_valid_date":"2025-01-31","unit_status":"needs_review"},
		"filters":{"series_id":"source-field","operation":"rows","start_date":"2025-02-01","end_date":"2025-02-01","value_filters":[]}
	}`
	call := coverageSourceFixture(t, "query_series", args, data)
	rendered, err := json.Marshal(call.Result.Data)
	require.NoError(t, err)
	call.Result.Output = string(rendered)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	batches := engine.taskCoverageEvidenceBatches(state)
	require.Len(t, batches, 1)
	require.Len(t, batches[0].Evidence, 1)
	gap := batches[0].Evidence[0]
	require.Equal(t, "gap", gap.Kind)
	require.Equal(t, "dataset", gap.SourceID)
	require.Equal(t, "document", gap.KnowledgeID)
	require.Equal(t, "2025-01-31", gap.LastValidDate)
	require.Empty(t, gap.Unit)
	require.Equal(t, structuredCitationReaderID(call), gap.EvidenceID)

	saved, err := json.Marshal(RunSnapshot{Version: RunSnapshotVersion, CompletedSteps: state.RoundSteps})
	require.NoError(t, err)
	snapshot, err := DecodeRunSnapshot(saved)
	require.NoError(t, err)
	state.RoundSteps = append(snapshot.CompletedSteps, snapshot.CompletedSteps[0])
	replayed := engine.taskCoverageEvidenceBatches(state)
	require.Len(t, replayed, 1)
	require.Len(t, replayed[0].Evidence, 1)
	require.Equal(t, structuredCitationReaderID(state.RoundSteps[0].ToolCalls[0]), replayed[0].Evidence[0].EvidenceID)
	require.Equal(t, gap.JSONPointer, replayed[0].Evidence[0].JSONPointer)
	require.Equal(t, gap.Kind, replayed[0].Evidence[0].Kind)
	require.Equal(t, replayed, engine.taskCoverageEvidenceBatches(state))
	rehydrated := state.RoundSteps[0].ToolCalls[0]
	require.NotEqual(t, structuredCitationReaderID(call), structuredCitationReaderID(rehydrated))
	for _, calls := range [][]types.ToolCall{{call, rehydrated}, {rehydrated, call}} {
		seen := map[string]bool{}
		mixed := engine.taskCoverageBatch(types.AgentStep{ToolCalls: calls}, state, seen)
		require.Len(t, mixed.Attempts, 1)
		require.Len(t, mixed.Evidence, 1)
		require.Equal(t, structuredCitationReaderID(calls[0]), mixed.Attempts[0].EvidenceID)
		require.Equal(t, mixed.Attempts[0].EvidenceID, mixed.Evidence[0].EvidenceID)
		later := engine.taskCoverageBatch(types.AgentStep{ToolCalls: calls}, state, seen)
		require.Empty(t, later.Attempts)
		require.Empty(t, later.Evidence)
		retry := call
		retry.ID = "a-new-invocation"
		later = engine.taskCoverageBatch(types.AgentStep{ToolCalls: []types.ToolCall{retry}}, state, seen)
		require.Len(t, later.Attempts, 1)
		require.Len(t, later.Evidence, 1)
	}

	for _, change := range []func(*types.ToolCall){
		func(call *types.ToolCall) { call.Result.Success = false },
		func(call *types.ToolCall) { call.Result.PreflightRejected = true },
		func(call *types.ToolCall) { call.Result.Data["execution_started"] = false },
		func(call *types.ToolCall) { delete(call.Result.Data, "_structured_execution") },
		func(call *types.ToolCall) { call.Args["series_id"] = "another-field" },
		func(call *types.ToolCall) { call.Args["end_date"] = "2025-02-02" },
		func(call *types.ToolCall) { call.Result.Data["truncated"] = true },
		func(call *types.ToolCall) { coverageObject(call.Result.Data["source"])["sha256"] = "" },
		func(call *types.ToolCall) {
			coverageObject(call.Result.Data["filters"])["value_filters"] = []any{map[string]any{"op": "eq", "value": 1}}
		},
	} {
		call := coverageSourceFixture(t, "query_series", args, data)
		change(&call)
		batch := engine.taskCoverageBatch(types.AgentStep{ToolCalls: []types.ToolCall{call}}, state, map[string]bool{})
		require.Len(t, batch.Attempts, 1)
		require.Empty(t, batch.Evidence)
	}
}

func TestTaskCoverageEvidenceDocumentBodyNeedsCurrentBoundFullChunk(t *testing.T) {
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, nil, nil, nil, nil, nil))
	engine := &AgentEngine{toolRegistry: registry}
	data, err := decodeCitationJSON(`{
		"document_ids":["doc"],
		"results":[{"knowledge_id":"doc","knowledge_base_id":"library","chunk_id":"chunk",
		    "content":"Observed measure: 29 kg.","content_complete":true,"content_offset":0,
		    "content_revision":2,"content_offset_space":"chunk_unicode_runes",
		    "source_locators":[{"type":"pdf","mapping":"exact","source_hash":"file-hash","page":11}]}]
	}`)
	require.NoError(t, err)
	call := types.ToolCall{ID: "search", Name: agenttools.ToolSearchKnowledge,
		Args: map[string]any{"query": "measure", "document_ids": []any{"doc"}}, Result: &types.ToolResult{Success: true, Data: data}}
	step := types.AgentStep{ToolCalls: []types.ToolCall{call}}
	batch := func() coverageEvidenceBatch { return engine.taskCoverageBatch(step, nil, map[string]bool{}) }
	require.Len(t, batch().Evidence, 1)
	body := batch().Evidence[0]
	require.Equal(t, "body", body.Kind)
	require.Equal(t, 11, body.Page)
	require.Equal(t, reviewDigest([]any{"file-hash", json.Number("2")}), body.SnapshotHash)
	require.Equal(t, "/data/results/0/content", body.JSONPointer)
	require.Equal(t, "chunk", body.CitationID)

	row := sourceNavigationRows(data["results"])[0]
	row["page"] = 22 // search rank and a page label do not override actual parser coordinates
	row["content_complete"] = false
	require.Equal(t, "navigation", batch().Evidence[0].Kind)
	row["content_complete"] = true
	data["document_ids"] = []any{"neighbor"}
	require.Equal(t, "navigation", batch().Evidence[0].Kind)
}

func TestTaskCoverageEvidenceKeepsPerPageWitness(t *testing.T) {
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewReadDocumentTool(nil, nil, nil))
	engine := &AgentEngine{toolRegistry: registry}
	data, err := decodeCitationJSON(`{
		"knowledge_id":"document","knowledge_base_id":"library","all_pages_succeeded":false,
		"page_results":[
			{"page":2,"success":true,"page_complete":true,"offset_space":"physical_page_chunks"},
			{"page":3,"success":false}],
		"chunks":[{"knowledge_id":"document","chunk_id":"chunk-2","page":2,"content":"Original body.",
			"content_complete":true,"content_offset":0,"content_revision":0,"content_offset_space":"physical_page_chunk_unicode_runes"}]
	}`)
	require.NoError(t, err)
	call := types.ToolCall{ID: "read", Name: agenttools.ToolReadDocument,
		Result: &types.ToolResult{Success: true, Data: data}}
	step := types.AgentStep{ToolCalls: []types.ToolCall{call}}
	batch := engine.taskCoverageBatch(step, nil, map[string]bool{})
	require.Len(t, batch.Evidence, 1)
	require.Equal(t, "body", batch.Evidence[0].Kind)
	require.Equal(t, 2, batch.Evidence[0].Page)
	sourceNavigationRows(data["chunks"])[0]["content_rewritten"] = true
	batch = engine.taskCoverageBatch(step, nil, map[string]bool{})
	require.Equal(t, "navigation", batch.Evidence[0].Kind)
	sourceNavigationRows(data["chunks"])[0]["content_rewritten"] = false
	sourceNavigationRows(data["chunks"])[0]["page"] = json.Number("3")
	batch = engine.taskCoverageBatch(step, nil, map[string]bool{})
	require.Equal(t, "navigation", batch.Evidence[0].Kind)
	require.Zero(t, batch.Evidence[0].Page)
}

func TestTaskCoverageEvidenceSQLPublishedInputAndRawInspection(t *testing.T) {
	var trace struct{ Steps []types.AgentStep }
	fixture, err := os.ReadFile("testdata/qa4/replay/r7-2-kb31.json")
	require.NoError(t, err)
	decoder := json.NewDecoder(strings.NewReader(string(fixture)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&trace))
	engine := &AgentEngine{toolRegistry: qa4ReplayRegistry(trace.Steps...)}
	var sql types.ToolCall
	for _, step := range trace.Steps {
		for _, call := range step.ToolCalls {
			if call.Name == "structured_analyze_table" {
				sql = call
			}
		}
	}
	require.NotNil(t, sql.Result)
	data := sql.Result.Data
	contract, err := structuredresult.ValidatePayload(data)
	require.NoError(t, err)
	require.NotNil(t, contract)
	for _, batch := range engine.taskCoverageEvidenceBatches(&types.AgentState{RoundSteps: trace.Steps}) {
		for _, item := range batch.Evidence {
			require.NotContains(t, []string{"gap", "last_valid", "cell"}, item.Kind,
				"SQL result dependencies do not attest absence or original cell lineage")
		}
	}

	// Simulate the same Python result shape for a completed transport window
	// with a zero-valid input. SQL output and retained-file identity stay separate.
	inputs := coverageObject(data["inputs"])
	var seriesID string
	for id, input := range inputs {
		seriesID = id
		coverageObject(input)["valid_count"] = json.Number("0")
	}
	const requested = "2025-02-01"
	data["requested_range"] = map[string]any{"start_date": requested, "end_date": requested}
	sql.Args["start_date"], sql.Args["end_date"] = requested, requested
	data["rows"] = []any{}
	require.NoError(t, rehashCoverageSQLFixture(data))
	batch := engine.taskCoverageBatch(types.AgentStep{ToolCalls: []types.ToolCall{sql}}, nil, map[string]bool{})
	require.Len(t, batch.Evidence, 1)
	require.Equal(t, "gap", batch.Evidence[0].Kind)
	require.Equal(t, seriesID, batch.Evidence[0].SeriesID)
	require.Contains(t, batch.Evidence[0].JSONPointer, "/valid_count")
	require.NotEqual(t, coverageObject(data["source"])["sha256"], batch.Evidence[0].SnapshotHash,
		"loaded SQL dataset inputs may retain series from an older workbook")

	call := coverageSourceFixture(t, "inspect_source", `{"dataset_id":"dataset","sheet":"sheet"}`,
		`{"dataset_id":"dataset","status":"ok","hash_verified":true,"ranges":[{"sheet":"sheet","cells":[
		{"cell":"A7","sheet":"sheet","value":"2025-01-05T00:00:00","value_status":"original"},
		{"cell":"B7","sheet":"sheet","value":42,"raw_value":42,"value_status":"original"}]}]}`)
	batch = engine.taskCoverageBatch(types.AgentStep{ToolCalls: []types.ToolCall{call}}, nil, map[string]bool{})
	require.Len(t, batch.Evidence, 2)
	require.False(t, batch.Evidence[1].Bound, "an omitted source hash is not a new snapshot")
	require.Equal(t, json.Number("42"), batch.Evidence[1].Raw)
	require.Equal(t, "/data/ranges/0/cells/1/raw_value", batch.Evidence[1].JSONPointer)
}

func rehashCoverageSQLFixture(data map[string]any) error {
	contract := coverageObject(data["result_contract"])
	keys := contract["hash_input_keys"].([]any)
	payload := map[string]any{}
	for _, key := range keys {
		name := key.(string)
		payload[name] = data[name]
	}
	delete(contract, "result_hash")
	digest, err := structuredresult.TypedSHA256(map[string]any{"payload": payload, "contract": contract})
	contract["result_hash"] = digest
	return err
}

func TestTaskCoverageEvidenceInspectedLatestSameField(t *testing.T) {
	field := coverageSeriesIdentity(coverageEvidence{Kind: "directory", Operation: "list_series", SourceDomain: "structured",
		SourceID: "dataset", KnowledgeBaseID: "library", EvidenceID: "directory", JSONPointer: "/data/series/0", Bound: true},
		map[string]any{"series_id": "selected", "sheet": "sheet", "last_date": "2025-01-31", "last_valid_date": "2025-01-30",
			"header_cells": []any{"A1", "B3"}, "unit_status": "needs_review"})
	date := coverageEvidence{Kind: "cell", Operation: "inspect_source", SourceID: "dataset", KnowledgeBaseID: "library",
		Sheet: "sheet", EvidenceID: "inspection", JSONPointer: "/data/ranges/0/cells/0/raw_value", Bound: true,
		Cell: "A10", SnapshotHash: "original", Raw: "2025-01-30T00:00:00"}
	value := date
	value.Cell, value.Raw, value.JSONPointer = "B10", json.Number("17"), "/data/ranges/0/cells/1/raw_value"
	last, ok := coveragePinInspectedLastValid(field, date, value)
	require.True(t, ok)
	require.Equal(t, "A10", last.DateCell)
	require.Equal(t, date.JSONPointer, last.DateJSONPointer)
	require.Equal(t, value.Raw, last.Raw)
	require.Empty(t, last.Unit)
	boundary, ok := coverageInspectedBoundary(field, last, "2025-02-01", "2025-02-28", "month")
	require.True(t, ok)
	require.Equal(t, "outside_published_observations", boundary.Status)
	require.Equal(t, "/data/series/0/last_valid_date", boundary.JSONPointer)
	require.Equal(t, value.EvidenceID, boundary.LastValidRef)
	_, ok = coverageInspectedBoundary(field, last, "2025-01-29", "2025-02-28", "month")
	require.False(t, ok)
	value.Cell = "C10" // A same-valued adjacent field is an independent observation.
	_, ok = coveragePinInspectedLastValid(field, date, value)
	require.False(t, ok)
	value.Cell, value.SnapshotHash = "B10", "retained-previous-version"
	_, ok = coveragePinInspectedLastValid(field, date, value)
	require.False(t, ok)
	value.SnapshotHash, value.DatasetID = date.SnapshotHash, "dataset"
	field.Raw = map[string]any{"source_dataset_id": "retained-previous-version"}
	_, ok = coveragePinInspectedLastValid(field, date, value)
	require.False(t, ok, "a retained field's boundary does not certify original cells in the new workbook")
	field.Raw = map[string]any{"source_context": map[string]any{"formula_source": map[string]any{"date_cell": "A2"}}}
	date.Cell = "D10"
	_, ok = coveragePinInspectedLastValid(field, date, value)
	require.False(t, ok, "a matching text from a different date column cannot certify the selected field")
}
