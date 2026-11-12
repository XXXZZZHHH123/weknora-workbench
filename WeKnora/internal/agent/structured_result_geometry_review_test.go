package agent

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func lightGeometryEngine(t *testing.T, replies ...mockResponse) (*AgentEngine, *types.AgentState, *mockChat) {
	t.Helper()
	e, state, model := lightStructuredFixture(t, replies...)
	document := &types.Knowledge{ID: "actual-document", TenantID: 7, KnowledgeBaseID: "granted-base", Title: "Source", FileName: "source.pdf"}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &plannedDocumentChunks{repo: &plannedDocumentRepo{}},
		types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-base", TenantID: 7}}))
	agenttools.PrepareDocumentScope(e.toolRegistry, "Only physical page 1.")
	binder, err := e.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	bound, err := binder.Execute(t.Context(), json.RawMessage(`{"selection_reason":"actual source plan","sources":[{"knowledge_id":"actual-document","expected_title":"Source"}]}`))
	require.NoError(t, err)
	require.True(t, bound.Success, bound.Error)
	return e, state, model
}

func lightGeometryFixture() *types.AgentState {
	state := boundGeometryOnlyDocumentState()
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	data["document_scope"] = map[string]any{"matched": true, "plan_validated": true, "knowledge_id": "actual-document", "page": 7}
	evidence := data["image_geometry_evidence"].([]map[string]any)[0]["evidence"].(map[string]any)
	point := func(value, fraction float64, left, right string, roles ...string) map[string]any {
		return map[string]any{"approximate_value": value, "estimate_low": value - 0.25, "estimate_high": value + 0.25,
			"roles": roles, "x_axis_interval": map[string]any{"left_label": left, "right_label": right,
				"fraction_between_labels": fraction, "relative_band": "middle_third"}}
	}
	series := []map[string]any{}
	for _, name := range []string{"Alpha", "Beta"} {
		left, right := "Start", "Middle"
		if name == "Beta" {
			left, right = "Middle", "End"
		}
		points := []map[string]any{point(10, 0.1, "Start", "Middle", "first", "maximum"),
			point(-5, 0.5, left, right, "minimum"), point(6, 0.9, "Middle", "End", "last")}
		series = append(series, map[string]any{"legend": name, "color": "#123456", "key_points": points, "samples": points,
			"key_point_changes": []map[string]any{{"from_roles": []string{"minimum"}, "to_roles": []string{"last"}, "approximate_change": 11}}})
	}
	evidence["geometry"] = map[string]any{"method": "original_emf_trace_geometry_v1", "unit": "%",
		"numeric_representation": "displayed_percent_values", "change_unit": "percentage_points", "value_interpretation": "Differences use percentage points.",
		"axis_tick_labels": []map[string]any{{"label": "10%", "value": 10}, {"label": "−10%", "value": -10}},
		"series_count":     2, "series": series, "x_axis_anchors": []map[string]any{{"label": "Start"}, {"label": "Middle"}, {"label": "End"}}}
	return state
}

func TestStructuredGeometryReviewCorrectsGroupMembershipOnce(t *testing.T) {
	const original = "Both Alpha and Beta have their minimum between Start and Middle. Their later endpoints rebound."
	const corrected = "Alpha has its minimum between Start and Middle; Beta between Middle and End. Their later endpoints rebound."
	e, _, model := lightGeometryEngine(t, lightReviewEdits(structuredResultEdit{
		"Both Alpha and Beta have their minimum between Start and Middle.",
		"Alpha has its minimum between Start and Middle; Beta between Middle and End.",
	}))
	state := lightGeometryFixture()
	before := reviewDigest(state.RoundSteps)
	views, scope := documentImageConsistencySummariesWithSources(state, e.sourceConsistencyMessages(state, nil))
	require.True(t, scope.ViewComplete, "%+v", scope)
	require.Len(t, views, 1)
	require.NotEmpty(t, views[0].EvidenceID)
	require.True(t, views[0].ViewComplete, "%+v", views[0])
	require.Empty(t, views[0].OmittedPaths)
	decision := e.checkCandidate(t.Context(), "Describe the grouped low points and later direction.", original, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, corrected, decision.Candidate)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.False(t, decision.SourceReviewAttempted)
	require.Len(t, model.calls, 1)
	require.True(t, e.candidateApprovalCurrent(corrected, state))
	require.Equal(t, before, reviewDigest(state.RoundSteps), "source geometry and its approval fingerprint are immutable")
	input := model.calls[0][1].Content
	for _, text := range []string{"actual_original_geometry", "Alpha", "Beta", "key_points", "minimum", "maximum", "first", "last", "axis_position", "percentage_points", "10%", "source_samples_omitted"} {
		require.Contains(t, input, text)
	}
	require.NotContains(t, input, `"source_samples":[`)
	require.LessOrEqual(t, len(input), structuredResultReviewInputBytes)
	e.checkCandidate(t.Context(), "Describe the grouped low points and later direction.", original, "", state, nil, true)
	require.Len(t, model.calls, 1, "geometry shares the existing once-per-turn allowance")
}

func TestStructuredGeometryReviewRejectsUnboundAndNonGeometrySources(t *testing.T) {
	for _, kind := range []string{"ordinary", "document", "web", "directory", "failed", "forged_output", "hash", "page", "part", "chunk", "catalog", "read_failed", "typed_other_execution", "unvalidated_scope", "wrong_scope", "rebound_page"} {
		t.Run(kind, func(t *testing.T) {
			e, _, model := lightGeometryEngine(t)
			state := lightGeometryFixture()
			call := &state.RoundSteps[0].ToolCalls[0]
			data := call.Result.Data
			figure := data["image_geometry_evidence"].([]map[string]any)[0]
			switch kind {
			case "ordinary":
				state.RoundSteps = nil
			case "document":
				delete(data, "image_geometry_evidence")
			case "web":
				call.Name = "web_fetch"
			case "directory":
				call.Name = "search_knowledge"
				data["display_type"], data["requires_source_scope"] = "document_info", true
			case "failed":
				call.Result.Success = false
			case "forged_output":
				raw, err := json.Marshal(data)
				require.NoError(t, err)
				call.Result.Output, call.Result.Data = string(raw), nil
			case "hash":
				figure["evidence"].(map[string]any)["source_media_sha256"] = "invalid"
			case "page":
				figure["page"] = 9
			case "part":
				figure["source_part"] = "ppt/media/other.emf"
			case "chunk":
				data["chunks"] = nil
			case "catalog":
				data["image_catalog"] = nil
			case "read_failed":
				data["image_evidence"].([]map[string]any)[0]["geometry_read_success"] = false
			case "typed_other_execution":
				call.Target = &types.ToolCallTarget{ServiceName: "another_source"}
			case "unvalidated_scope":
				data["document_scope"].(map[string]any)["plan_validated"] = false
			case "wrong_scope":
				data["document_scope"].(map[string]any)["knowledge_id"] = "another-document"
			case "rebound_page":
				binder, err := e.toolRegistry.GetTool(agenttools.ToolBindDocumentScope)
				require.NoError(t, err)
				bound, err := binder.Execute(t.Context(), json.RawMessage(`{"selection_reason":"restricted source","sources":[{"knowledge_id":"actual-document","expected_title":"Source","user_page_constraint":{"pages":[1],"user_quote":"Only physical page 1."}}]}`))
				require.NoError(t, err)
				require.True(t, bound.Success, bound.Error)
			}
			decision := e.checkCandidate(t.Context(), "Explain the available result.", "The core explanation is retained.", "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.Empty(t, model.calls, "metadata, prose, pixels and rejected bindings must not trigger the optional geometry check")
		})
	}
}

func TestStructuredGeometryAndSQLShareSingleReview(t *testing.T) {
	e, state, model := lightGeometryEngine(t, lightReviewReply("keep", ""))
	state.RoundSteps = append(state.RoundSteps, lightGeometryFixture().RoundSteps...)
	decision := e.checkCandidate(t.Context(), "Describe both sources.", "Both current sources were read.", "", state, nil, false)
	require.Equal(t, "kept", decision.StructuredResultReviewStatus)
	require.Len(t, model.calls, 1)
	require.Contains(t, model.calls[0][1].Content, "actual_structured_results")
	require.Contains(t, model.calls[0][1].Content, "actual_original_geometry")
	require.Contains(t, model.calls[0][1].Content, "Alpha")
	require.Contains(t, model.calls[0][1].Content, "12.500")
	e.checkCandidate(t.Context(), "Describe both sources.", "A revised answer.", "", state, nil, true)
	require.Len(t, model.calls, 1)
}

func TestStructuredGeometryReviewSkipsIncompleteOrOversizedProjection(t *testing.T) {
	for _, variant := range []string{"missing_role", "missing_series", "invalid_sample", "too_large"} {
		t.Run(variant, func(t *testing.T) {
			e, _, model := lightGeometryEngine(t)
			state := lightGeometryFixture()
			geometry := state.RoundSteps[0].ToolCalls[0].Result.Data["image_geometry_evidence"].([]map[string]any)[0]["evidence"].(map[string]any)["geometry"].(map[string]any)
			series := geometry["series"].([]map[string]any)
			switch variant {
			case "missing_role":
				series[0]["key_points"].([]map[string]any)[0]["roles"] = []string{"first"}
			case "missing_series":
				geometry["series_count"] = 3
			case "invalid_sample":
				series[0]["samples"] = []any{nil}
			case "too_large":
				series[0]["legend"] = strings.Repeat("long legend ", 4000)
			}
			before := reviewDigest(state.RoundSteps)
			decision := e.checkCandidate(t.Context(), "Describe source facts.", "The core explanation is retained.", "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.Empty(t, model.calls)
			require.Contains(t, decision.StructuredResultReviewStatus, "not_checked_")
			require.Equal(t, before, reviewDigest(state.RoundSteps))
		})
	}
}

func TestStructuredGeometryReviewKeepsSelectedAndShortCurves(t *testing.T) {
	e, _, model := lightGeometryEngine(t, lightReviewReply("keep", ""))
	state := lightGeometryFixture()
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	geometry := data["image_geometry_evidence"].([]map[string]any)[0]["evidence"].(map[string]any)["geometry"].(map[string]any)
	geometry["source_series_count"], geometry["series_selection"], geometry["omitted_series"] = 3, []string{"Alpha", "Beta"}, []string{"Gamma"}
	points := geometry["series"].([]map[string]any)[1]["key_points"].([]map[string]any)
	for i, point := range points {
		point["x_axis_interval"] = map[string]any{"left_label": "Start", "right_label": "Middle", "fraction_between_labels": 0.1 + float64(i)*0.2, "relative_band": "middle_third"}
	}
	before := reviewDigest(state.RoundSteps)
	decision := e.checkCandidate(t.Context(), "Describe the selected curves.", "The short Beta curve covers only part of the plotted range.", "", state, nil, false)
	require.Equal(t, "kept", decision.StructuredResultReviewStatus)
	require.Len(t, model.calls, 1)
	input := model.calls[0][1].Content
	for _, field := range []string{"source_series_count", "series_selection", "omitted_series", "Gamma", "Alpha", "Beta", "source_samples_omitted"} {
		require.Contains(t, input, field)
	}
	require.NotContains(t, input, `"source_samples":[`)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
}

func TestStructuredGeometryReviewRejectsConflictingReadVersions(t *testing.T) {
	e, _, model := lightGeometryEngine(t)
	state, changed := lightGeometryFixture(), lightGeometryFixture()
	changed.RoundSteps[0].ToolCalls[0].ID = "later-file-version"
	changed.RoundSteps[0].ToolCalls[0].Result.Data["image_geometry_evidence"].([]map[string]any)[0]["evidence"].(map[string]any)["source_file_sha256"] = strings.Repeat("c", 64)
	state.RoundSteps = append(state.RoundSteps, changed.RoundSteps...)
	decision := e.checkCandidate(t.Context(), "Explain the source.", "The supported core result remains.", "", state, nil, false)
	require.Equal(t, "not_checked_geometry_omission", decision.StructuredResultReviewStatus)
	require.Empty(t, model.calls)
}

func TestGeometryReviewAxisPositionIgnoresJSONDecoderRepresentation(t *testing.T) {
	point := func(fraction any) map[string]any {
		return map[string]any{"approximate_value": 2.5, "x_fraction": 0.25,
			"x_axis_interval": map[string]any{"left_label": "Left", "right_label": "Right", "fraction_between_labels": fraction, "relative_band": "last_third"}}
	}
	floatPoint, numberPoint := point(0.87654), point(json.Number("0.87654"))
	before := reviewDigest(numberPoint)
	require.Equal(t, reviewFigurePoint(floatPoint), reviewFigurePoint(numberPoint))
	require.Equal(t, 87.654, reviewFigurePoint(numberPoint)["axis_position"].(map[string]any)["percent_of_THIS_interval_from_LEFT_tick"])
	require.Equal(t, before, reviewDigest(numberPoint))
	for _, fraction := range []any{"0.5", json.Number("invalid"), json.Number("NaN"), json.Number("Infinity"), math.NaN(), math.Inf(1), -0.1, 1.1} {
		require.NotContains(t, reviewFigurePoint(point(fraction)), "axis_position")
	}
}

func TestStructuredGeometryColumnarReviewPreservesValuesAndLegacyObjects(t *testing.T) {
	e, _, _ := lightGeometryEngine(t)
	state := lightGeometryFixture()
	geometry := state.RoundSteps[0].ToolCalls[0].Result.Data["image_geometry_evidence"].([]map[string]any)[0]["evidence"].(map[string]any)["geometry"].(map[string]any)
	rows := geometry["series"].([]map[string]any)
	change := map[string]any{"from_roles": []any{"minimum"}, "to_roles": []any{"last"}, "approximate_change": 11,
		"estimate_low": 10.5, "estimate_high": 11.5, "x_span_fraction": 0.2519,
		"horizontal_fraction_of_this_trace": 0.376, "absolute_change_fraction_of_this_trace_value_range": 0.7333}
	rows[0]["key_point_changes"] = []any{change}
	extendedChange := reviewAsMap(change)
	extendedChange["future_annotation"] = map[string]any{"preserve": nil}
	rows[1]["key_point_changes"] = append(rows[1]["key_point_changes"].([]map[string]any), extendedChange)
	point := rows[0]["key_points"].([]map[string]any)[0]
	delete(point, "x_axis_interval")
	point["axis_position"] = map[string]any{"left_source_tick": "Start", "right_source_tick": "Middle",
		"percent_of_THIS_interval_from_LEFT_tick": 10.25, "relative_band_from_LEFT_tick": "first_third",
		"containing_interval_starts_at_source_label": "Start", "future_annotation": "preserve"}
	before := reviewDigest(state.RoundSteps)
	sources, _ := documentImageConsistencySummariesWithSources(state, e.sourceConsistencyMessages(state, nil))
	require.Len(t, sources, 1)
	canonicalBefore := reviewDigest(sources)
	expected := reviewAsMap(reviewAsMap(sources[0].Content)["original_geometry_evidence"])["geometry"]
	view, complete := e.currentOriginalGeometryReviewEvidence(state)
	require.True(t, complete)
	figures := view["figures"].([]reviewToolEvidence)
	projected := reviewAsMap(reviewAsMap(reviewAsMap(figures[0].Content)["original_geometry_evidence"])["geometry"])
	axisColumns := reviewAsSlice(projected["axis_position_columns"])
	changeColumns := reviewAsSlice(projected["key_point_change_columns"])
	require.Len(t, axisColumns, 4)
	require.Len(t, changeColumns, 8)
	decode := func(value any, columns []any) map[string]any {
		if object, ok := value.(map[string]any); ok {
			return object
		}
		values, ok := value.([]any)
		require.True(t, ok)
		require.Len(t, values, len(columns))
		object := map[string]any{}
		for i, column := range columns {
			object[column.(string)] = values[i]
		}
		return object
	}
	wantRows := reviewAsSlice(reviewAsMap(expected)["series"])
	gotRows := reviewAsSlice(projected["series"])
	require.Len(t, gotRows, len(wantRows))
	for i, raw := range gotRows {
		got, want := reviewAsMap(raw), reviewAsMap(wantRows[i])
		for _, field := range []string{"legend", "color", "source_sample_count"} {
			require.Equal(t, want[field], got[field])
		}
		wantChanges := reviewAsSlice(want["key_point_changes"])
		gotChanges := reviewAsSlice(got["key_point_changes"])
		require.Len(t, gotChanges, len(wantChanges))
		for j, rawChange := range gotChanges {
			require.Equal(t, wantChanges[j], decode(rawChange, changeColumns))
		}
		wantPoints, gotPoints := reviewAsSlice(want["key_points"]), reviewAsSlice(got["key_points"])
		require.Len(t, gotPoints, len(wantPoints))
		for j, rawPoint := range gotPoints {
			gotPoint, wantPoint := reviewAsMap(rawPoint), reviewAsMap(wantPoints[j])
			if _, columnar := gotPoint["axis_position"].([]any); columnar {
				position := decode(gotPoint["axis_position"], axisColumns)
				position["containing_interval_starts_at_source_label"] = position["left_source_tick"]
				gotPoint["axis_position"] = position
			}
			require.Equal(t, wantPoint, gotPoint, "every point's roles, estimates and exact axis values survive")
		}
	}
	first := reviewAsMap(gotRows[0])
	second := reviewAsMap(gotRows[1])
	require.IsType(t, []any{}, reviewAsSlice(first["key_point_changes"])[0])
	require.IsType(t, map[string]any{}, reviewAsSlice(second["key_point_changes"])[0], "legacy fields remain intact")
	require.IsType(t, map[string]any{}, reviewAsSlice(second["key_point_changes"])[1], "unknown fields remain intact")
	require.IsType(t, map[string]any{}, reviewAsMap(reviewAsSlice(first["key_points"])[0])["axis_position"], "extended axis retains all named fields")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	sourcesAfter, _ := documentImageConsistencySummariesWithSources(state, e.sourceConsistencyMessages(state, nil))
	require.Equal(t, canonicalBefore, reviewDigest(sourcesAfter), "canonical/strict source projection is unchanged")
}

func TestStructuredGeometryColumnValuesPreserveNullEmptyAndExactNumbers(t *testing.T) {
	columns := []string{"number", "null", "empty_array", "empty_text"}
	object := map[string]any{"number": json.Number("0.123456789012345678901234567890"), "null": nil, "empty_array": []any{}, "empty_text": ""}
	before := reviewDigest(object)
	values, ok := structuredGeometryColumnValues(object, columns)
	require.True(t, ok)
	decoded := map[string]any{}
	for i, column := range columns {
		decoded[column] = values[i]
	}
	require.Equal(t, object, decoded)
	require.Equal(t, before, reviewDigest(object))
	delete(object, "null")
	_, ok = structuredGeometryColumnValues(object, columns)
	require.False(t, ok, "missing is not null")
	object["unknown"] = nil
	_, ok = structuredGeometryColumnValues(object, columns)
	require.False(t, ok, "a same-size object with different keys must remain intact")
}
