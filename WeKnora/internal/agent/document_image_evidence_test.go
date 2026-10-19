package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func boundDocumentImageState() *types.AgentState {
	state := sourceConsistencyState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	pixels := []byte("actual selected image")
	hash := sha256.Sum256(pixels)
	result.Images = []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(pixels)}
	result.ImageDescriptions = []string{"Plot Alpha, widgets: visible trough followed by a rebound."}
	result.Data["knowledge_id"] = "actual-document"
	source := map[string]any{"reference": "resource://AbCdEfGhIjKlMnOpQrStUv", "knowledge_id": "actual-document",
		"chunk_id": "actual-chunk", "page": 7, "label": "Plot Alpha", "source_context": "Original selected figure."}
	result.Data["image_catalog"] = []map[string]any{source}
	read := map[string]any{}
	for k, v := range source {
		read[k] = v
	}
	read["sha256"], read["read_success"], read["returned_image_index"] = hex.EncodeToString(hash[:]), true, 0
	read["version"] = "document-original-image-v1"
	result.Data["image_evidence"] = []map[string]any{read}
	return state
}

func TestDocumentImageReviewKeepsBindingAndObservationIndependentOfLongPage(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	result.Data["chunks"].([]map[string]any)[0]["content"] = strings.Repeat("Neighboring figure Beta has another axis. ", 10000)
	model := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, model)
	repair, err := engine.answerNeedsRepair(t.Context(), "Describe the requested curve.", "A trough followed by a rebound, estimated from the original.", "", state)
	require.NoError(t, err)
	require.False(t, repair)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	observations := input["document_image_observations"].([]any)
	require.Len(t, observations, 1)
	view := observations[0].(map[string]any)
	require.Equal(t, true, view["view_complete"])
	bound := view["content"].(map[string]any)
	require.Equal(t, "actual-chunk", bound["chunk_id"])
	require.Equal(t, "Plot Alpha", bound["label"])
	require.Equal(t, result.ImageDescriptions[0], bound["vision_observation"])
	require.Equal(t, true, bound["original_pixels_verified"])
	require.NotContains(t, model.calls[0][1].Content, "base64,")
}

func TestDocumentImageReviewRefusesUnboundOrMismatchedObservations(t *testing.T) {
	for _, change := range []string{"failed", "chunk", "document", "page", "hash", "index", "missing-pixels", "other-tool"} {
		t.Run(change, func(t *testing.T) {
			state := boundDocumentImageState()
			call := &state.RoundSteps[0].ToolCalls[0]
			result := call.Result
			read := result.Data["image_evidence"].([]map[string]any)[0]
			switch change {
			case "failed":
				read["read_success"] = false
			case "chunk":
				read["chunk_id"] = "not-fetched"
			case "document":
				read["knowledge_id"] = "another-document"
			case "page":
				read["page"] = 8
			case "hash":
				read["sha256"] = strings.Repeat("0", 64)
			case "index":
				read["returned_image_index"] = 1
			case "missing-pixels":
				result.Images = nil
			case "other-tool":
				call.Name = "untrusted_tool"
			}
			observations, _ := documentImageConsistencySummaries(state)
			require.Empty(t, observations)
		})
	}
}

func TestDocumentImageReviewRetainsEveryOriginalTraceRangeAndSourceSample(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	source := result.Data["image_evidence"].([]map[string]any)[0]
	entry := map[string]any{}
	for key, value := range source {
		entry[key] = value
	}
	entry["evidence"] = map[string]any{"version": "source_figure_geometry_v1", "source_part": "ppt/media/plot.emf", "source_file_sha256": strings.Repeat("a", 64), "source_media_sha256": strings.Repeat("b", 64),
		"geometry": map[string]any{"method": "original_emf_trace_geometry_v1", "series_count": 2,
			"series": []map[string]any{{"legend": "Alpha", "first": map[string]any{"approximate_value": 40}, "last": map[string]any{"approximate_value": 30}, "samples": []map[string]any{{"approximate_value": 40}, {"approximate_value": 30}}},
				{"legend": "Beta", "first": map[string]any{"approximate_value": 70}, "last": map[string]any{"approximate_value": 60}, "samples": []map[string]any{{"approximate_value": 70}, {"approximate_value": 60}}}}}}
	result.Data["image_geometry_evidence"] = []map[string]any{entry}
	views, scope := documentImageConsistencySummaries(state)
	require.True(t, scope.ViewComplete)
	view := views[0].Content.(map[string]any)["original_geometry_evidence"].(map[string]any)["geometry"].(map[string]any)
	traces := view["series"].([]any)
	require.Len(t, traces, 2)
	require.Equal(t, "Alpha", traces[0].(map[string]any)["legend"])
	require.Equal(t, "Beta", traces[1].(map[string]any)["legend"])
	require.NotContains(t, traces[0].(map[string]any), "samples")
	require.Len(t, traces[0].(map[string]any)["source_samples"], 2)
	require.Equal(t, true, traces[0].(map[string]any)["sample_projection_complete"])
	require.Len(t, entry["evidence"].(map[string]any)["geometry"].(map[string]any)["series"], 2)
}

func TestOriginalGeometryFactViewsDoNotPromoteConflictingModelObservations(t *testing.T) {
	state := boundDocumentImageState()
	call := &state.RoundSteps[0].ToolCalls[0]
	result := call.Result
	result.ImageDescriptions = []string{"MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES"}
	result.Data["image_descriptions"] = append([]string(nil), result.ImageDescriptions...)
	source := result.Data["image_evidence"].([]map[string]any)[0]
	figure := map[string]any{}
	for key, value := range source {
		figure[key] = value
	}
	figure["evidence"] = map[string]any{"version": "source_figure_geometry_v1", "source_file_sha256": strings.Repeat("a", 64), "source_media_sha256": strings.Repeat("b", 64), "geometry": map[string]any{"method": "original_emf_trace_geometry_v1", "series": []map[string]any{{"legend": "Alpha", "last": map[string]any{"approximate_value": 25}}}}}
	result.Data["image_geometry_evidence"] = []map[string]any{figure}
	engine := sourceConsistencyEngine(t, &mockChat{})
	messages := engine.appendToolImages(t.Context(), []chat.Message{{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: "verified drawing data"}}, state.RoundSteps[0])
	require.NotContains(t, messages[0].Content, "MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES")
	require.Contains(t, messages[0].Content, "Verified original drawing coordinates")
	evidence, _ := engine.sourceConsistencyEvidence(state, nil)
	encoded, _ := json.Marshal(evidence)
	require.NotContains(t, string(encoded), "MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES")
	summaries, _ := documentImageConsistencySummaries(state)
	encoded, _ = json.Marshal(summaries)
	require.NotContains(t, string(encoded), "MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES")
	require.Contains(t, string(encoded), "approximate_value")
	require.Equal(t, "MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES", result.ImageDescriptions[0])
	require.Equal(t, "MODEL_GUESS_OUTSIDE_ORIGINAL_COORDINATES", result.Data["image_descriptions"].([]string)[0], "audit/UI original must remain unchanged")
	// A wrong pixel binding cannot suppress the only available observation.
	result.Data["image_evidence"].([]map[string]any)[0]["sha256"] = strings.Repeat("0", 64)
	require.Empty(t, nativeGeometryImageIndexes(*call))
}

func TestCalibratedPointReviewSeparatesIntervalDistanceFromWholePlotPosition(t *testing.T) {
	point := map[string]any{"x_fraction": .46, "approximate_value": 12,
		"x_axis_interval": map[string]any{"left_label": "Alpha", "right_label": "Beta", "fraction_between_labels": .08}}
	view := reviewFigurePoint(point)
	require.NotContains(t, view, "x_fraction")
	position := view["axis_position"].(map[string]any)
	require.Equal(t, "Alpha", position["left_source_tick"])
	require.Equal(t, "Beta", position["right_source_tick"])
	require.Equal(t, 8.0, position["percent_of_THIS_interval_from_LEFT_tick"])
	require.Equal(t, .46, point["x_fraction"], "original coordinates remain in durable evidence")
	delete(point, "x_axis_interval")
	require.Equal(t, .46, reviewFigurePoint(point)["x_fraction"], "uncalibrated points retain their actual position")
}

func TestDocumentImageReviewKeepsEveryLegendWhenMultiCurveEvidenceExceedsOldBudget(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	rows := []map[string]any{}
	for i := range 24 {
		points := []map[string]any{}
		for j, role := range []string{"first", "maximum", "minimum", "last"} {
			value := []int{10, 30, 5, 20}[j]
			band := []string{"first_third", "first_third", "middle_third", "last_third"}[j]
			points = append(points, map[string]any{"roles": []string{role}, "approximate_value": value,
				"estimate_low": value - 1, "estimate_high": value + 1, "x_fraction": float64(j) / 4,
				"x_axis_interval": map[string]any{"left_label": "Source mark Alpha", "right_label": "Source mark Beta",
					"fraction_between_labels": float64(j) / 4, "relative_band": band}})
		}
		rows = append(rows, map[string]any{"legend": fmt.Sprintf("Curve-%d", i), "key_points": points, "samples": points})
	}
	source := result.Data["image_evidence"].([]map[string]any)[0]
	figure := map[string]any{}
	for key, value := range source {
		figure[key] = value
	}
	figure["evidence"] = map[string]any{"version": "source_figure_geometry_v1", "source_file_sha256": strings.Repeat("a", 64),
		"source_media_sha256": strings.Repeat("b", 64), "geometry": map[string]any{"method": "original_emf_trace_geometry_v1", "series": rows}}
	result.Data["image_geometry_evidence"] = []map[string]any{figure}
	views, scope := documentImageConsistencySummaries(state)
	require.True(t, scope.ViewComplete)
	encoded, err := json.Marshal(views)
	require.NoError(t, err)
	require.Greater(t, len(encoded), 16384, "the regression must exercise evidence beyond the old shared limit")
	geometry := views[0].Content.(map[string]any)["original_geometry_evidence"].(map[string]any)["geometry"].(map[string]any)
	traces := geometry["series"].([]any)
	require.Len(t, traces, 24)
	require.Equal(t, "Curve-23", traces[23].(map[string]any)["legend"])
}

func TestDocumentImageReviewUsesTheSameCompleteSampleSetAndUncertaintyAsGeneration(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	points := []map[string]any{}
	for i := range 26 {
		points = append(points, map[string]any{"approximate_value": i * 4, "estimate_low": i*4 - 1,
			"estimate_high": i*4 + 1, "x_fraction": float64(i) / 30})
	}
	source := result.Data["image_evidence"].([]map[string]any)[0]
	figure := map[string]any{}
	for key, value := range source {
		figure[key] = value
	}
	figure["evidence"] = map[string]any{"version": "source_figure_geometry_v1", "source_file_sha256": strings.Repeat("a", 64),
		"source_media_sha256": strings.Repeat("b", 64), "geometry": map[string]any{"method": "original_emf_trace_geometry_v1",
			"series": []map[string]any{{"legend": "Alpha", "samples": points}}}}
	result.Data["image_geometry_evidence"] = []map[string]any{figure}
	views, scope := documentImageConsistencySummaries(state)
	require.True(t, scope.ViewComplete)
	geometry := views[0].Content.(map[string]any)["original_geometry_evidence"].(map[string]any)["geometry"].(map[string]any)
	row := geometry["series"].([]any)[0].(map[string]any)
	samples := row["source_samples"].([]any)
	require.Len(t, samples, len(points))
	require.Equal(t, true, row["sample_projection_complete"])
	for i, raw := range samples {
		point := raw.(map[string]any)
		for _, key := range []string{"approximate_value", "estimate_low", "estimate_high", "x_fraction"} {
			require.Equal(t, fmt.Sprint(points[i][key]), fmt.Sprint(point[key]), "source sample and calibrated bounds must not disappear in review")
		}
	}
}

func TestDocumentSourceReviewUsesConfiguredVisionModelAndOriginalPixels(t *testing.T) {
	primary := &mockChat{}
	vision := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	engine := sourceConsistencyEngine(t, primary)
	engine.config.ReasoningEffort = "off"
	engine.SetDocumentImageReviewModel(vision, "off")
	state := boundDocumentImageState()
	repair, err := engine.answerNeedsRepair(t.Context(), "Describe the source curve.", "A decline followed by a recovery, estimated from the figure.", "", state)
	require.NoError(t, err)
	require.False(t, repair)
	require.Empty(t, primary.calls, "the selected answer model must not silently review unseen pixels")
	require.Len(t, vision.calls, 1)
	require.Equal(t, state.RoundSteps[0].ToolCalls[0].Result.Images, vision.calls[0][1].Images)
	require.Equal(t, "off", engine.config.ReasoningEffort)
	require.GreaterOrEqual(t, vision.opts[0].MaxCompletionTokens, 2048)
	require.LessOrEqual(t, vision.opts[0].MaxCompletionTokens, 6144)
	require.JSONEq(t, string(sourceConsistencySchema), string(vision.opts[0].Format))
	require.False(t, *vision.opts[0].Thinking)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(vision.calls[0][1].Content), &input))
	delivery := input["source_review_image_delivery"].(map[string]any)
	require.Equal(t, float64(1), delivery["image_count"])
	require.Len(t, delivery["bindings"], 1)
}

func TestDocumentSourceReviewNeverDeliversUnboundPixels(t *testing.T) {
	for _, variant := range []string{"hash", "failed", "chunk"} {
		t.Run(variant, func(t *testing.T) {
			primary := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
			vision := &mockChat{}
			engine := sourceConsistencyEngine(t, primary)
			engine.SetDocumentImageReviewModel(vision, "off")
			state := boundDocumentImageState()
			read := state.RoundSteps[0].ToolCalls[0].Result.Data["image_evidence"].([]map[string]any)[0]
			switch variant {
			case "hash":
				read["sha256"] = strings.Repeat("0", 64)
			case "failed":
				read["read_success"] = false
			case "chunk":
				read["chunk_id"] = "not-fetched"
			}
			_, err := engine.answerNeedsRepair(t.Context(), "Explain the fetched text.", "The text names the unit.", "", state)
			require.NoError(t, err)
			require.Empty(t, vision.calls)
			require.Len(t, primary.calls, 1)
			require.Empty(t, primary.calls[0][1].Images)
		})
	}
}

func TestDocumentVisualReviewKeepsTruncationAndVerdictProtocolChecks(t *testing.T) {
	truncated := sourceConsistencyVerdict(`{"complete":true}`)
	truncated.chunks[0].FinishReason = "length"
	vision := &mockChat{responses: []mockResponse{truncated, sourceConsistencyVerdict(`{"complete":false,"feedback":"核对曲线与实际来源。"}`)}}
	primary := &mockChat{}
	engine := sourceConsistencyEngine(t, primary)
	engine.SetDocumentImageReviewModel(vision, "off")
	repair, err := engine.answerNeedsRepair(t.Context(), "Describe the source.", "A candidate observation.", "", boundDocumentImageState())
	require.NoError(t, err)
	require.True(t, repair)
	require.Equal(t, "核对曲线与实际来源。", engine.answerReviewFeedback)
	require.Empty(t, primary.calls)
	require.Len(t, vision.calls, 2, "a complete-looking JSON object with a truncated finish must never pass")
	require.Equal(t, vision.calls[0][1], vision.calls[1][1], "retry preserves the exact candidate, evidence and pixels")
}
