package agent

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Af1 KB-31 r1/r2 obtained multi-field SQL endpoints. Af1 KB-31 r3
// read the original worksheet. Af1 KB-16 r1 got a seven-field source
// clarification. Fixture transport IDs are aliased; original locators and
// numerical evidence survive that transformation. Catalog and inspection
// payloads retain the observed fields and cells used by this test.
func loadAf2LastValidCalls(t *testing.T) (fixture struct {
	MultiSQL         []qa4Stage2Call `json:"multi_sql"`
	WarehouseCatalog qa4Stage2Call   `json:"warehouse_catalog"`
	WarehouseInspect qa4Stage2Call   `json:"warehouse_inspect"`
	ClosingCatalog   qa4Stage2Call   `json:"closing_catalog"`
	ClosingUnbound   qa4Stage2Call   `json:"closing_unbound"`
}) {
	t.Helper()
	raw, err := os.ReadFile("testdata/qa4/replay/af2-last-valid.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	for i, recorded := range fixture.MultiSQL {
		payload, err := decodeCitationJSON(string(recorded.Response))
		require.NoError(t, err)
		require.NoError(t, rehashCoverageSQLFixture(payload))
		fixture.MultiSQL[i].Response, err = json.Marshal(payload)
		require.NoError(t, err)
	}
	return fixture
}

func (r *af1SourceReplay) bindAf2Endpoint(field coverageEvidence, from, to string) {
	r.t.Helper()
	r.engine.evidenceCompletion.setCoverageGoal(taskCoverageTarget{GoalIndex: 0, RequestBasis: r.question, StructuredTaskGoal: &StructuredTaskGoal{
		SourceMode: "named_first", SourceTime: "observed_period", OnVerifiedGap: []string{"last_valid_same_field"},
		Period: map[string]any{"from": from, "to": to, "grain": "day"},
	}})
	r.engine.evidenceCompletion.record.Bindings = append(r.engine.evidenceCompletion.record.Bindings, coverageBinding{
		GoalIndex: 0, Role: "primary", KnowledgeBaseID: field.KnowledgeBaseID, SourceID: field.SourceID,
		SeriesID: field.SeriesID, Sheet: field.Sheet, Column: field.Column, SnapshotHash: field.SnapshotHash,
		EvidenceID: field.EvidenceID, JSONPointer: field.JSONPointer, PeriodFrom: from, PeriodTo: to,
	})
}

func TestQA4Af2EndpointReadUsesCurrentBoundField(t *testing.T) {
	trace := loadAf1SourceFixture(t).Endpoints[0]
	for _, scenario := range []string{"read", "missing_response_endpoint", "already_read", "observed_conflict", "explicit_false", "feature_off", "legacy_schema", "stale_snapshot", "no_permission", "expired", "window_only", "different_window"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
			if scenario == "feature_off" {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
			}
			read := trace.Observed
			read.Arguments["source_binding"] = nil // Advertise the actual negotiated source binding option.
			read.Arguments["limit"] = float64(1)
			r := newAf1SourceReplay(t, trace.Question, read)
			r.engine.turnReview.Query = trace.Question
			r.engine.reviewConversationReady = true // executeLoop pins the human-turn transcript before entering ReAct.
			if scenario == "legacy_schema" {
				manifest := structuredCodecManifest()
				for _, tool := range sourceNavigationRows(manifest["tools"]) {
					if tool["name"] != "query_series" {
						continue
					}
					properties := coverageObject(coverageObject(tool["inputSchema"])["properties"])
					for argument := range read.Arguments {
						if argument != "knowledge_base_id" && argument != "include_last_valid" {
							properties[argument] = map[string]any{}
						}
					}
					delete(properties, "include_last_valid")
				}
				_, err := r.engine.toolRegistry.InstallStructuredManifest(manifest)
				require.NoError(t, err)
			}
			var flag any
			switch scenario {
			case "explicit_false":
				flag = false
			case "already_read":
				flag, read.Response = true, trace.OtherProtocol
			case "observed_conflict":
				flag, read.Response = true, af2EndpointConflict(t, trace.OtherProtocol)
			}
			var options map[string]any
			if flag != nil {
				options = map[string]any{"include_last_valid": flag}
			}
			original := r.execute("historical-query", read, options, flag)
			require.True(t, original.Result.Success, original.Result.Error)
			if scenario == "legacy_schema" {
				_, ok := r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
				require.False(t, ok)
				return
			}
			record := r.engine.evidenceCompletion.record
			index := slices.IndexFunc(record.Evidence, func(item coverageEvidence) bool { return item.Kind == "gap" })
			require.GreaterOrEqual(t, index, 0)
			field := record.Evidence[index]
			from, to := field.PeriodFrom, field.PeriodTo
			if scenario == "different_window" {
				from = "2024-03-09"
			}
			if scenario == "stale_snapshot" {
				field.SnapshotHash = "0000000000000000000000000000000000000000000000000000000000000000"
			}
			r.bindAf2Endpoint(field, from, to)
			if scenario == "window_only" {
				r.engine.evidenceCompletion.declared[0].OnVerifiedGap = []string{"report_gap"}
			}
			ctx := r.ctx
			if scenario == "no_permission" {
				ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"elsewhere"}})
			}
			if scenario == "expired" {
				expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = expired
			}
			call, ok := r.engine.nextCoverageLastValidRead(ctx, &r.state)
			require.Equal(t, scenario == "read" || scenario == "missing_response_endpoint", ok)
			if scenario == "observed_conflict" {
				feedback := r.engine.lastValidCoverageFeedback(r.ctx)
				require.Len(t, feedback, 1)
				require.Equal(t, "conflict", feedback[0].Endpoint)
				require.Nil(t, feedback[0].Raw)
			}
			if !ok {
				return
			}
			require.Equal(t, "structured_query_series", call.Function.Name)
			args, err := decodeCitationJSON(call.Function.Arguments)
			require.NoError(t, err)
			require.Equal(t, field.SeriesID, args["series_id"])
			require.Equal(t, from, args["start_date"])
			require.Equal(t, to, args["end_date"])
			require.Equal(t, true, args["include_last_valid"])
			require.Equal(t, []any{}, args["source_binding"], "explicit empty groups cannot start a service-side model")
			if scenario == "read" {
				read.Response = trace.OtherProtocol
				r.response, r.wantFlag = read, true
				r.state.CurrentRound = 1
				var messages []chat.Message
				_, err := r.engine.runReActIteration(ctx, &r.state, &messages, r.engine.buildToolsForLLM(), "session", "turn", trace.Question, &loopGuards{})
				require.NoError(t, err)
				steps := r.state.RoundSteps
				require.Len(t, steps[len(steps)-1].ToolCalls, 1)
				registered := steps[len(steps)-1].ToolCalls[0]
				require.Equal(t, call.ID, registered.ID)
				require.True(t, registered.Result.Success, registered.Result.Error)
				require.Zero(t, r.engine.chatModel.(*qa4Stage2CModel).callCount, "endpoint round performed no model call")
			} else {
				result := r.execute(call.ID, read, args, true)
				require.True(t, result.Result.Success, result.Result.Error)
			}
			require.Equal(t, 2, r.transport)
			feedback := r.engine.lastValidCoverageFeedback(r.ctx)
			require.Len(t, feedback, 1)
			if scenario == "read" {
				assertAf1WarehouseEndpoint(t, feedback[0])
			} else {
				require.Equal(t, "unavailable", feedback[0].Endpoint)
			}
			_, ok = r.engine.nextCoverageLastValidRead(ctx, &r.state)
			require.False(t, ok)
		})
	}
}

func TestQA4Af2MultipleExecutedFieldsRequireAnIndependentSelection(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	traces := loadAf1SourceFixture(t).Endpoints
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(traces[order[0]].Run, func(t *testing.T) {
			calls := []qa4Stage2Call{traces[order[0]].Observed, traces[order[1]].Observed}
			calls[0].Arguments["source_binding"] = nil
			calls[0].Arguments["limit"] = float64(1)
			r := newAf1SourceReplay(t, traces[0].Question, calls...)
			r.engine.turnReview.Query = traces[0].Question
			for _, read := range calls {
				result := r.execute(read.id(), read, nil, nil)
				require.True(t, result.Result.Success, result.Result.Error)
			}
			_, auto := r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
			require.False(t, auto)
			record := r.engine.evidenceCompletion.record
			selectedIndex := slices.IndexFunc(record.Evidence, func(item coverageEvidence) bool { return item.Kind == "gap" && item.SeriesID == "warehouse.B" })
			require.GreaterOrEqual(t, selectedIndex, 0)
			selected := record.Evidence[selectedIndex]
			r.bindAf2Endpoint(selected, selected.PeriodFrom, selected.PeriodTo)
			call, auto := r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
			require.True(t, auto)
			args, err := decodeCitationJSON(call.Function.Arguments)
			require.NoError(t, err)
			require.Equal(t, selected.SeriesID, args["series_id"])
			otherIndex := slices.IndexFunc(record.Evidence, func(item coverageEvidence) bool { return item.Kind == "gap" && item.SeriesID == "warehouse.AF" })
			require.GreaterOrEqual(t, otherIndex, 0)
			other := record.Evidence[otherIndex]
			r.bindAf2Endpoint(other, other.PeriodFrom, other.PeriodTo)
			_, auto = r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
			require.False(t, auto)
		})
	}
}

func af2EndpointConflict(t *testing.T, receipt json.RawMessage) json.RawMessage {
	t.Helper()
	payload, err := decodeCitationJSON(string(receipt))
	require.NoError(t, err)
	endpoint := coverageObject(coverageObject(payload["coverage"])["last_valid"])
	rows := endpoint["rows"].([]any)
	competing := map[string]any{}
	for key, value := range rows[0].(map[string]any) {
		competing[key] = value
	}
	competing["value"], competing["raw_value"], competing["source_cell"], competing["date_cell"] = json.Number("4890"), "4890", "B3341", "A3341"
	endpoint["status"], endpoint["conflict"], endpoint["row_count"], endpoint["rows"] = "conflict", true, json.Number("2"), append(rows, competing)
	require.NoError(t, rehashCoverageSQLFixture(payload))
	changed, err := json.Marshal(payload)
	require.NoError(t, err)
	return changed
}

func TestQA4Af2PreservesSQLAndOriginalEndValues(t *testing.T) {
	fixture := loadAf2LastValidCalls(t)
	for _, flag := range []string{"true", "false"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", flag)
			for _, read := range fixture.MultiSQL {
				question, _ := read.Arguments["source_question"].(string)
				r := newAf1SourceReplay(t, question, read)
				r.engine.turnReview.Query = question
				var sent any
				if flag == "true" {
					sent = true
				}
				actual := r.execute(read.id(), read, nil, sent)
				require.True(t, actual.Result.Success, actual.Result.Error)
				feedback := r.engine.lastValidCoverageFeedback(r.ctx)
				if flag == "true" {
					require.Len(t, feedback, len(read.Arguments["series_ids"].([]any)))
					index := slices.IndexFunc(feedback, func(item coverageLastValidFeedback) bool { return item.Series == "warehouse.B" })
					require.GreaterOrEqual(t, index, 0)
					assertAf1WarehouseEndpoint(t, feedback[index])
				} else {
					require.Empty(t, feedback)
				}
				_, auto := r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
				require.False(t, auto, "multi-field analysis did not bind a primary field for the question")
			}
			const question = "棉花数据库里2024年3月10日的郑棉仓单是多少？"
			r := newAf1SourceReplay(t, question, fixture.WarehouseCatalog, fixture.WarehouseInspect)
			actual := r.execute(fixture.WarehouseCatalog.id(), fixture.WarehouseCatalog, nil, nil)
			require.True(t, actual.Result.Success, actual.Result.Error)
			actual = r.execute(fixture.WarehouseInspect.id(), fixture.WarehouseInspect, nil, nil)
			require.True(t, actual.Result.Success, actual.Result.Error)
			record := r.engine.evidenceCompletion.record
			original := inspectedPublishedLastValid(record)
			require.Len(t, original, 1)
			require.Equal(t, "warehouse.B", original[0].SeriesID)
			require.Equal(t, "A3340", original[0].DateCell)
			require.Equal(t, "B3340", original[0].Cell)
			require.Equal(t, "2023-09-21", original[0].Date)
			require.True(t, coverageSameOriginalValue(original[0].Raw, json.Number("5160")))
		})
	}
}

func TestQA4Af2UnresolvedSelectionUsesNativeCandidateGroup(t *testing.T) {
	fixture := loadAf2LastValidCalls(t)
	for _, flag := range []string{"true", "false"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", flag)
			question := fixture.ClosingUnbound.Arguments["source_question"].(string)
			r := newAf1SourceReplay(t, question, fixture.ClosingCatalog, fixture.ClosingUnbound)
			r.engine.turnReview.Query = question
			catalog := r.execute(fixture.ClosingCatalog.id(), fixture.ClosingCatalog, nil, nil)
			require.True(t, catalog.Result.Success, catalog.Result.Error)
			var sent any
			if flag == "true" {
				sent = true
			}
			unbound := r.execute(fixture.ClosingUnbound.id(), fixture.ClosingUnbound, nil, sent)
			require.True(t, unbound.Result.Success, unbound.Result.Error)
			require.Equal(t, "needs_clarification", unbound.Result.Data["status"])
			require.Empty(t, r.engine.lastValidCoverageFeedback(r.ctx), "the catalog and clarification supplied no numerical endpoint")
			if flag == "true" {
				issues := r.engine.lastValidSelectionFeedback(r.ctx, &r.state)
				require.Len(t, issues, 1)
				require.Empty(t, issues[0].RequestedFrom, "the unbounded attempted read omitted the human's window")
				require.Equal(t, "futures.close.CF.CZC", issues[0].Series)
				require.Len(t, issues[0].CandidateSets, 1)
				require.Len(t, issues[0].CandidateSets[0], 7)
			} else {
				require.Empty(t, r.engine.lastValidSelectionFeedback(r.ctx, &r.state))
			}
			_, auto := r.engine.nextCoverageLastValidRead(r.ctx, &r.state)
			require.False(t, auto)
			require.Equal(t, 2, r.transport)
		})
	}
}
