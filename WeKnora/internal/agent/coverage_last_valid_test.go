package agent

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// The store and SQL producers generated these v2 receipts from synthetic cells.
func lastValidFixture(t *testing.T, name string) types.ToolCall {
	t.Helper()
	raw, err := os.ReadFile("testdata/qa4/a2-last-valid.json")
	require.NoError(t, err)
	fixture, err := decodeCitationJSON(string(raw))
	require.NoError(t, err)
	receipt := coverageObject(fixture[name])
	data, args := coverageObject(receipt["data"]), coverageObject(receipt["args"])
	contract, err := structuredresult.ValidatePayload(data)
	require.NoError(t, err)
	require.NotNil(t, contract)
	operation := receipt["operation"].(string)
	tool := "structured_" + operation
	data["_structured_execution"] = agenttools.StructuredOperationDescriptor{Operation: operation, ToolName: tool,
		EffectClass: "read", ScopeMode: "knowledge_base"}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	return types.ToolCall{ID: name, Name: tool, Args: args, Result: &types.ToolResult{Success: true, Data: data, Output: string(encoded)}}
}

func TestA2LastValidNativeProjection(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	ctx := context.Background()
	engine := &AgentEngine{toolRegistry: qa4ReplayRegistry()}
	for _, name := range []string{"missing", "sql_missing", "rows", "sql_direct"} {
		t.Run(name, func(t *testing.T) {
			call := lastValidFixture(t, name)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call, call}}}}
			record, attempts := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
			require.Len(t, attempts, 1, "a copied native call should not create a second witness")
			var endpoint coverageEvidence
			count := 0
			for _, item := range record.Evidence {
				if item.SeriesID == "first" && coverageProtocolLastValid(item) {
					endpoint, count = item, count+1
				}
			}
			require.Equal(t, 1, count)
			require.True(t, coverageCurrentEvidence(record, endpoint))
			require.Equal(t, "2025-02-01", endpoint.Date)
			require.Equal(t, "B2", endpoint.Cell)
			require.Equal(t, "A2", endpoint.DateCell)
			require.Equal(t, json.Number("137.25"), endpoint.Raw)
			require.Equal(t, "cached_numeric", endpoint.FormulaCacheStatus)
			require.Equal(t, "kg", endpoint.Unit)
			require.Contains(t, endpoint.JSONPointer, "/coverage/last_valid/rows/0/value")
			if strings.Contains(name, "missing") {
				require.Equal(t, "before_range", endpoint.LastValidDirection)
				var gap coverageEvidence
				for _, item := range record.Evidence {
					if item.Kind == "gap" && item.SeriesID == "first" {
						gap = item
					}
					if item.SeriesID == "second" && coverageProtocolLastValid(item) {
						value, ok := coverageFiniteNumber(item.Raw)
						require.True(t, ok)
						require.Zero(t, value)
						require.Equal(t, "C2", item.Cell)
					}
				}
				require.True(t, gap.Complete)
				require.True(t, sameCoverageLastValid(record, gap, endpoint))
				answer := engine.terminalCoverageFailure(ctx, state)
				require.Contains(t, answer, "2025-02-01")
				require.Equal(t, 1, strings.Count(answer, "137.25"))
				require.NotContains(t, answer, "925.5")
				return
			}
			require.Equal(t, "in_range", endpoint.LastValidDirection)
			require.Equal(t, "cell", endpoint.Kind)
			require.NotEmpty(t, endpoint.ResultJSONPointer)
			for _, item := range record.Evidence {
				require.NotEqual(t, endpoint.ResultJSONPointer, item.JSONPointer, "a matching direct result should reuse the original cell witness")
			}
		})
	}
	for _, names := range [][]string{{"missing", "sql_missing"}, {"sql_missing", "missing"}} {
		calls := []types.ToolCall{lastValidFixture(t, names[0]), lastValidFixture(t, names[1])}
		state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: calls}}}
		answer := engine.terminalCoverageFailure(ctx, state)
		require.Equal(t, 1, strings.Count(answer, "137.25"), "native query and SQL read the same original source cell")
	}
}

func TestA2LastValidStatusesAndLegacyRecovery(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	for _, mode := range []string{"observed", "reordered", "conflict", "truncated", "no_valid", "wrong_address", "future", "tampered", "disabled", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
			engine := newTestEngine(t, &mockChat{})
			engine.toolRegistry = qa4ReplayRegistry()
			engine.config.WorkbenchDefault = true
			call := lastValidFixture(t, "missing")
			data := call.Result.Data
			coverage := coverageObject(data["coverage"])
			last := coverageObject(coverage["last_valid"])
			row := coverageObject(last["rows"].([]any)[0])
			coverageObject(data["series"])["last_valid_date"] = "2025-02-01"
			switch mode {
			case "observed", "reordered":
				blank := map[string]any{"date": row["date"], "source_cell": "B10", "date_cell": "A10",
					"value_status": "formula_no_cache", "formula_cache_status": "missing", "value": nil}
				last["rows"], last["row_count"] = []any{row, blank}, json.Number("2")
				if mode == "reordered" {
					slices.Reverse(last["rows"].([]any))
				}
			case "conflict":
				other := map[string]any{"date": row["date"], "source_cell": "B10", "date_cell": "A10",
					"value_status": "valid", "formula_cache_status": "not_formula", "value": json.Number("517.25"), "raw_value": "517.25"}
				last["status"], last["row_count"], last["conflict"], last["rows"] = "conflict", json.Number("2"), true, []any{row, other}
			case "truncated":
				last["status"], last["row_count"] = "truncated", json.Number("17")
			case "no_valid":
				last["status"], last["direction"], last["date"], last["row_count"], last["rows"] = "no_valid", nil, nil, json.Number("0"), []any{}
				coverage["valid_first_date"], coverage["valid_last_date"] = nil, nil
			case "wrong_address":
				row["date_cell"] = "A3"
			case "future":
				last["direction"], last["date"], row["date"] = "in_range", "2025-02-05", "2025-02-05"
				coverage["record_last_date"], coverage["valid_last_date"] = "2025-02-05", "2025-02-05"
			case "tampered":
				row["value"] = json.Number("912.25")
			case "disabled":
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
			case "legacy":
				delete(data, "coverage")
				delete(data, "requested_range")
				contract := coverageObject(data["result_contract"])
				contract["hash_input_keys"] = slices.DeleteFunc(contract["hash_input_keys"].([]any), func(key any) bool {
					return key == "coverage" || key == "requested_range"
				})
			}
			if mode != "tampered" {
				require.NoError(t, rehashCoverageSQLFixture(data))
			}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			engine.refreshEvidenceCompletion(context.Background(), "Inspect this field", state, nil)
			record := engine.evidenceCompletion.record
			_, name, _ := engine.coverageLatestRead(state, false)
			if mode == "legacy" || mode == "disabled" {
				require.Equal(t, "structured_query_series", name)
				return
			}
			require.Empty(t, name, "a new endpoint must not open a second bound read")
			require.Equal(t, mode == "observed" || mode == "reordered", slices.ContainsFunc(record.Evidence, coverageProtocolLastValid))
			if mode == "tampered" {
				require.Empty(t, record.Evidence)
			}
		})
	}
}

func TestA2LastValidCandidateAndCitationUseCurrentCanonicalResult(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	ctx := context.Background()
	model := &mockChat{}
	engine := newTestEngine(t, model, func(config *types.AgentConfig) {
		config.WorkbenchDefault, config.EvidenceReferencesEnabled = true, true
		config.AnswerReviewMode = "standard"
		config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "fixture-library"}}
	})
	engine.toolRegistry = qa4ReplayRegistry()
	call := lastValidFixture(t, "missing")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	view := engine.currentStructuredCitationView(call)
	require.Contains(t, view, "/data/coverage/last_valid/rows/0/value")
	require.Len(t, engine.turnReview.StructuredCitations, 1)
	var id string
	for key, item := range engine.turnReview.StructuredCitations {
		id = key
		require.Len(t, item.Reference.Locators, 2)
		require.Equal(t, 2, item.Reference.Locators[0].ColumnStart)
	}
	question := "Read the measurement in the requested interval and give the most recent measured value if the interval is empty."
	answer := "There was no valid measurement in that period. The most recent measurement before the period was 137.25 kg on 2025-02-01."
	withCitation := answer + ` <evidence evidence_id="` + id + `" />`
	decision := engine.checkCandidate(ctx, question, withCitation, "", state, nil, false)
	require.Empty(t, decision.Unsupported)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Len(t, engine.currentCandidateEvidenceReferences(withCitation, state, ctx), 1)
	require.Empty(t, engine.currentCandidateEvidenceReferences(withCitation, state), "a current request is required for A2 references")
	require.Zero(t, model.callCount)
	revoked := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"other-library"}})
	require.Empty(t, engine.currentCandidateEvidenceReferences(withCitation, state, revoked))
	require.Equal(t, "citation_identity_invalid", engine.checkCandidate(revoked, question, withCitation, "", state, nil, false).ReasonCode)
	require.Empty(t, engine.currentCandidateEvidenceReferences(withCitation, state, nil))

	allowedDirectory := engine.structuredCitationReviewDirectory(ctx, withCitation, state)
	allowedItem := allowedDirectory["items"].([]any)[0].(map[string]any)
	require.Contains(t, allowedItem, "manifest")
	require.Contains(t, allowedItem, "source")
	require.Equal(t, "/data/coverage/last_valid/rows/0/value", allowedItem["json_pointer"])
	require.Equal(t, 1, engine.sourceCitationReviewPolicy(ctx, state, nil)["available_evidence_count"])
	for _, restricted := range []context.Context{revoked, nil} {
		directory := engine.structuredCitationReviewDirectory(restricted, withCitation, state)
		require.Equal(t, []any{map[string]any{"id": id, "status": "unknown_or_changed"}}, directory["items"])
		require.Zero(t, engine.sourceCitationReviewPolicy(restricted, state, nil)["available_evidence_count"])
	}
	reviewView := engine.buildSourceReviewSnapshot(revoked, withCitation, state, nil)
	encoded, err := json.Marshal(reviewView.input)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"native-document"`)
	require.NotContains(t, string(encoded), `"B2"`)
	require.NotContains(t, string(encoded), `"last_valid"`)
	require.Len(t, state.RoundSteps[0].ToolCalls, 1, "the original receipt is still recorded")
	repair, err := engine.validateCitationCheck(revoked, &sourceCitationCheck{Requirement: "required"}, withCitation, state, nil, true)
	require.NoError(t, err)
	require.True(t, repair)
	_, counts, _ := engine.candidateCitationRecovery(revoked, withCitation, state, nil)
	require.Equal(t, 1, counts["structured_reference_invalid"])

	engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "other-library"}}
	decision = engine.checkCandidate(ctx, question, answer, "", state, nil, false)
	require.Contains(t, decision.Unsupported, "137.25")
	require.Empty(t, engine.currentCandidateEvidenceReferences(withCitation, state))
	engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "fixture-library"}}
	last := coverageObject(coverageObject(call.Result.Data["coverage"])["last_valid"])
	last["row_count"] = json.Number("327.27")
	conversation := []chat.Message{{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: call.Result.Output}}
	evidence := engine.answerNumericEvidenceInScope(ctx, answer, state.RoundSteps, conversation)
	bad := ungroundedStructuredNumbersWithEmptyEvidence("Values 137.25 and 327.27", evidence, true, state.RoundSteps)
	require.Equal(t, []string{"137.25", "327.27"}, bad)
	require.Empty(t, engine.currentCandidateEvidenceReferences(withCitation, state))
}

func TestA2LastValidReviewerPacketRechecksAuthorization(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"Use evidence that is still authorized."}`)}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.EvidenceReferencesEnabled = true
	engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "fixture-library"}}
	engine.toolRegistry = qa4ReplayRegistry()
	ctx := t.Context()
	call := lastValidFixture(t, "missing")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	require.NotEmpty(t, engine.currentStructuredCitationView(call))
	id := engine.sortedCurrentStructuredCitationIDs()[0]
	answer := `The field has a verified reading. <evidence evidence_id="` + id + `" />`
	old := engine.buildSourceReviewSnapshot(ctx, answer, state, nil)
	_, accessible := old.sourceRecords.byID[structuredCitationReaderID(call)]
	require.True(t, accessible, "an authorized current review can retrieve the exact original receipt")

	revoked := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"other-library"}})
	repair, err := engine.evaluateAnswerCompletenessWithSnapshot(revoked, "Check the field reading.", answer, state, 0, old)
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, 1, model.callCount)
	var packet map[string]any
	for _, message := range model.calls[0] {
		if message.Role == "user" {
			require.NoError(t, json.Unmarshal([]byte(message.Content), &packet))
			break
		}
	}
	require.NotNil(t, packet)
	directory := coverageObject(packet["structured_citation_evidence"])
	require.Equal(t, []any{map[string]any{"id": id, "status": "unknown_or_changed"}}, directory["items"])
	require.Empty(t, coverageObject(packet["source_citation_policy"])["available_evidence"])
	data, err := json.Marshal(packet)
	require.NoError(t, err)
	require.NotContains(t, string(data), "native-document")
	require.NotContains(t, string(data), `"B2"`)
	require.NotContains(t, string(data), "/data/coverage/last_valid/rows/0/value")
	require.NotContains(t, string(data), "137.25")
}

func TestA2LastValidDisabledReviewsStoredIntervalData(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	jsonText := func(value any) string {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		return string(encoded)
	}
	model := &mockChat{responses: []mockResponse{acceptedCitationVerdict("required", "The returned interval readings support the answer.")}}
	engine := sourceConsistencyEngine(t, model)
	engine.config.EvidenceReferencesEnabled = true
	engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "fixture-library"}}
	engine.toolRegistry = qa4ReplayRegistry()
	ctx := t.Context()
	rows, missing, analysis := lastValidFixture(t, "rows"), lastValidFixture(t, "missing"), lastValidFixture(t, "sql_direct")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{rows, missing, analysis}}}}
	require.NotEmpty(t, engine.currentStructuredCitationView(rows))
	require.NotEmpty(t, engine.currentStructuredCitationView(missing))
	var intervalID, endpointID string
	for id, observation := range engine.turnReview.StructuredCitations {
		selector := coverageObject(observation.Directory["selector"])
		if selector["scope"] == "returned_result" && observation.CallID == rows.ID {
			intervalID = id
		}
		if selector["scope"] == "last_valid" && observation.CallID == missing.ID {
			endpointID = id
		}
	}
	require.NotEmpty(t, intervalID)
	require.NotEmpty(t, endpointID)
	answer := `The requested range has a reading. <evidence evidence_id="` + intervalID + `" />`
	endpointAnswer := `<evidence evidence_id="` + endpointID + `" />`
	on := engine.buildSourceReviewSnapshot(ctx, answer, state, nil)
	require.Contains(t, on.sourceRecords.byID, structuredCitationReaderID(missing))
	require.Len(t, engine.currentCandidateEvidenceReferences(endpointAnswer, state, ctx), 1)
	before := reviewDigest(missing.Result.Data)

	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
	off := engine.buildSourceReviewSnapshot(ctx, answer, state, nil)
	for _, sample := range []struct {
		call      types.ToolCall
		pointer   string
		wantValue string
	}{
		{rows, "/data/rows/0/value", "137.25"},
		{missing, "/data/valid_count", "0"},
		{analysis, "/data/rows/0/2", "137.25"},
	} {
		id := structuredCitationReaderID(lastValidIntervalReviewCall(sample.call))
		require.Contains(t, off.sourceRecords.byID, id)
		read := func(pointer string) map[string]any {
			return off.sourceRecords.read(jsonText(map[string]string{"operation": "read", "evidence_id": id, "json_pointer": pointer}))
		}
		found := read(sample.pointer)
		require.Equal(t, "ok", found["status"])
		require.Equal(t, sample.wantValue, jsonText(found["value"]))
		root := coverageObject(off.sourceRecords.records[off.sourceRecords.byID[id]].Value)
		projected := coverageObject(root["data"])
		require.NotContains(t, projected, "result_contract")
		require.False(t, coverageHasLastValid(projected))
	}
	require.NotContains(t, jsonText(off.input), "/coverage/last_valid")
	require.Equal(t, before, reviewDigest(missing.Result.Data))
	contract, err := structuredresult.ValidatePayload(missing.Result.Data)
	require.NoError(t, err)
	require.NotNil(t, contract)
	require.Empty(t, engine.currentCandidateEvidenceReferences(endpointAnswer, state, ctx))
	require.Empty(t, engine.currentStructuredCitationView(missing))
	refs := engine.currentCandidateEvidenceReferences(answer, state, ctx)
	require.Len(t, refs, 1)
	require.NotContains(t, string(refs[0].Manifest), `"last_valid"`)
	require.Equal(t, contract.SchemaVersion, 2)

	missingSteps := []types.AgentStep{{ToolCalls: []types.ToolCall{missing}}}
	claim := "The latest reading was 137.25."
	numeric := engine.answerNumericEvidenceInScope(ctx, claim, missingSteps)
	require.Equal(t, []string{"137.25"}, ungroundedStructuredNumbersWithEmptyEvidence(claim, numeric, true, missingSteps))

	// The already-built authorized A2 packet cannot be reused after disabling.
	repair, err := engine.evaluateAnswerCompletenessWithSnapshot(ctx, "Check the interval reading.", answer, state, 0, on)
	require.NoError(t, err)
	require.False(t, repair)
	require.Equal(t, 1, model.callCount)
	var packet map[string]any
	for _, message := range model.calls[0] {
		if message.Role == "user" {
			require.NoError(t, json.Unmarshal([]byte(message.Content), &packet))
			break
		}
	}
	require.NotEmpty(t, packet["source_tool_results"])
	require.NotContains(t, jsonText(packet), "/coverage/last_valid")
	intervalView, current := engine.currentStructuredObservation(intervalID, state, ctx)
	require.True(t, current)
	projectedID := structuredCitationReaderID(lastValidIntervalReviewCall(rows))
	require.Equal(t, projectedID, intervalView.Directory["evidence_id"])
	require.Contains(t, jsonText(packet["structured_query_row_companion"]), projectedID)

	revoked := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"other-library"}})
	require.Empty(t, engine.lastValidSourceReviewState(revoked, state, 0).RoundSteps[0].ToolCalls)
	require.Empty(t, engine.currentCandidateEvidenceReferences(answer, state, revoked))
	legacy := coverageSourceFixture(t, "query_series", `{}`, `{"status":"no_data","valid_count":0}`)
	legacyState := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{legacy}}}}
	require.Same(t, legacyState, engine.lastValidSourceReviewState(ctx, legacyState, 0))

	newest := lastValidFixture(t, "rows")
	newest.ID = "new-manifest"
	newManifest := strings.Repeat("e", 64)
	snapshot := coverageObject(coverageObject(newest.Result.Data["result_contract"])["source_snapshot"])
	coverageObject(snapshot["versions"])["manifest_sha256"] = newManifest
	newCoverage := coverageObject(newest.Result.Data["coverage"])
	coverageObject(newCoverage["source"])["manifest_sha256"] = newManifest
	newEndpoint := coverageObject(newCoverage["last_valid"])
	newEndpoint["manifest_sha256"], newEndpoint["source_manifest_sha256"] = newManifest, newManifest
	require.NoError(t, rehashCoverageSQLFixture(newest.Result.Data))
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{newest}})
	updated := engine.lastValidSourceReviewState(ctx, state, 0)
	require.Empty(t, updated.RoundSteps[0].ToolCalls)
	require.Len(t, updated.RoundSteps[1].ToolCalls, 1)
	require.Empty(t, engine.currentCandidateEvidenceReferences(answer, state, ctx))
	newest.Args["series_id"] = "other-series"
	invalid := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{newest}}}}
	require.Empty(t, engine.lastValidSourceReviewState(ctx, invalid, 0).RoundSteps[0].ToolCalls)
	newest.Args["series_id"] = "first"
	newest.Result.Data["rows"] = []any{}
	require.Empty(t, engine.lastValidSourceReviewState(ctx, invalid, 0).RoundSteps[0].ToolCalls)
}

func TestA2LastValidRetainedWorkbookAndLaterManifest(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	engine := newTestEngine(t, &mockChat{}, func(config *types.AgentConfig) { config.EvidenceReferencesEnabled = true })
	engine.toolRegistry = qa4ReplayRegistry()
	call := lastValidFixture(t, "sql_missing")
	data := call.Result.Data
	first := coverageObject(coverageObject(data["inputs"])["first"])
	coverage := coverageObject(first["coverage"])
	workbook := coverageObject(coverage["source"])
	workbook["dataset_id"], workbook["sha256"], workbook["manifest_sha256"] = "retained", strings.Repeat("b", 64), strings.Repeat("d", 64)
	first["source"] = workbook
	last := coverageObject(coverage["last_valid"])
	last["source_dataset_id"], last["source_sha256"], last["source_manifest_sha256"] = workbook["dataset_id"], workbook["sha256"], workbook["manifest_sha256"]
	unit := coverageObject(last["unit"])
	coverageObject(unit["evidence"].([]any)[0])["source_sha256"] = workbook["sha256"]
	require.NoError(t, rehashCoverageSQLFixture(data))
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	record, _ := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
	var earlier coverageEvidence
	for _, item := range record.Evidence {
		if item.SeriesID == "first" && coverageProtocolLastValid(item) {
			earlier = item
		}
	}
	require.True(t, coverageCurrentEvidence(record, earlier))
	require.Equal(t, workbook["sha256"], earlier.WorkbookSnapshotHash)
	require.NotEqual(t, earlier.WorkbookSnapshotHash, earlier.SnapshotHash)
	require.NotContains(t, engine.currentStructuredCitationView(call), "/data/inputs/first/coverage/last_valid/rows/0/value", "retained data may not cite the publisher's other workbook")

	latest := lastValidFixture(t, "missing")
	newManifest := strings.Repeat("e", 64)
	snapshot := coverageObject(coverageObject(latest.Result.Data["result_contract"])["source_snapshot"])
	coverageObject(snapshot["versions"])["manifest_sha256"] = newManifest
	newCoverage := coverageObject(latest.Result.Data["coverage"])
	coverageObject(newCoverage["source"])["manifest_sha256"] = newManifest
	newLast := coverageObject(newCoverage["last_valid"])
	newLast["manifest_sha256"], newLast["source_manifest_sha256"] = newManifest, newManifest
	require.NoError(t, rehashCoverageSQLFixture(latest.Result.Data))
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{latest}})
	updated, _ := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
	require.False(t, coverageCurrentEvidence(updated, earlier))
	require.True(t, slices.ContainsFunc(updated.Evidence, func(item coverageEvidence) bool {
		return coverageProtocolLastValid(item) && item.ToolCallID == latest.ID && coverageCurrentEvidence(updated, item)
	}))
}

func TestA2LastValidFilteredScopeAndAmbiguousLatestCitability(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	engine := newTestEngine(t, &mockChat{}, func(config *types.AgentConfig) { config.EvidenceReferencesEnabled = true })
	engine.toolRegistry = qa4ReplayRegistry()
	call := lastValidFixture(t, "missing")
	root := call.Result.Data
	filters := coverageObject(root["filters"])
	filters["last_n"], call.Args["last_n"] = json.Number("2"), json.Number("2")
	require.NoError(t, rehashCoverageSQLFixture(root))
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	record, _ := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
	require.Len(t, record.Evidence, 1)
	endpoint := record.Evidence[0]
	require.True(t, coverageProtocolLastValid(endpoint))
	require.NotEmpty(t, endpoint.LastValidFilterHash)
	gap := endpoint
	gap.Kind, gap.LastValidFilterHash = "gap", ""
	_, matched := coverageEndpointForGap(record, gap)
	require.False(t, matched, "the filtered endpoint must not discharge an unfiltered gap")
	delete(call.Args, "last_n")
	invalid, _ := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
	require.False(t, slices.ContainsFunc(invalid.Evidence, coverageProtocolLastValid))

	for _, status := range []string{"conflict", "truncated"} {
		t.Run(status, func(t *testing.T) {
			call := lastValidFixture(t, "rows")
			root := call.Result.Data
			call.Args["operation"], coverageObject(root["filters"])["operation"] = "latest", "latest"
			root["rows_are"] = "latest_valid_observation"
			contract := coverageObject(root["result_contract"])
			coverageObject(contract["execution"])["operation"] = "latest"
			last := coverageObject(coverageObject(root["coverage"])["last_valid"])
			last["status"] = status
			if status == "conflict" {
				last["conflict"] = true
				last["row_count"] = json.Number("2")
				row := map[string]any{"date": "2025-02-01", "source_cell": "B10", "date_cell": "A10",
					"value_status": "valid", "formula_cache_status": "not_formula", "value": json.Number("317.25"), "raw_value": "317.25"}
				last["rows"] = append(last["rows"].([]any), row)
			} else {
				last["row_count"] = json.Number("17")
			}
			require.NoError(t, rehashCoverageSQLFixture(root))
			contractV2, err := structuredresult.ValidatePayload(root)
			require.NoError(t, err)
			require.NotNil(t, contractV2)
			steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
			require.Empty(t, engine.currentStructuredCitationView(call))
			answer := "The unique latest value is 137.25 kg."
			evidence := engine.answerNumericEvidenceInScope(context.Background(), answer, steps)
			require.Equal(t, []string{"137.25"}, ungroundedStructuredNumbersWithEmptyEvidence(answer, evidence, true, steps))
			ctx := context.WithValue(context.Background(), types.LanguageContextKey, "en-US")
			fallback := engine.terminalCoverageFailure(ctx, &types.AgentState{RoundSteps: steps})
			require.NotContains(t, fallback, "137.25")
			if status == "conflict" {
				require.Contains(t, fallback, "conflicting values")
			} else {
				require.Contains(t, fallback, "more original rows")
			}
		})
	}
}
