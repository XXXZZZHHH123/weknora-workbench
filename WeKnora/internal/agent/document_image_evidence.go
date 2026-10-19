package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Keep per-image identity beside the actual observation, independently of a
// potentially long multi-figure page. This projection never creates evidence
// from assistant prose or from an image URL without returned source pixels.
func documentImageConsistencySummaries(state *types.AgentState) ([]reviewToolEvidence, reviewEvidenceScope) {
	return documentImageConsistencySummariesWithSources(state, nil)
}

func documentImageConsistencySummariesWithSources(state *types.AgentState, sourceMessages []chat.Message) ([]reviewToolEvidence, reviewEvidenceScope) {
	messages := []chat.Message{}
	if state == nil {
		return boundedReviewEvidence(messages, 131072)
	}
	type source struct {
		Reference   string `json:"reference"`
		KnowledgeID string `json:"knowledge_id"`
		ChunkID     string `json:"chunk_id"`
		Page        int    `json:"page"`
		Label       string `json:"label"`
		Context     string `json:"source_context"`
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Name != tools.ToolReadDocument || call.Result == nil || !call.Result.Success {
				continue
			}
			result := call.Result
			var data struct {
				KnowledgeID string   `json:"knowledge_id"`
				Catalog     []source `json:"image_catalog"`
				Geometry    []struct {
					source
					Evidence map[string]any `json:"evidence"`
				} `json:"image_geometry_evidence"`
				Reads []struct {
					source
					Success bool   `json:"read_success"`
					Index   *int   `json:"returned_image_index"`
					SHA256  string `json:"sha256"`
					Version string `json:"version"`
				} `json:"image_evidence"`
			}
			encoded, err := json.Marshal(result.Data)
			if err != nil || json.Unmarshal(encoded, &data) != nil {
				continue
			}
			chunks := fetchedDocumentChunks(call)
			for _, read := range data.Reads {
				if _, valid := types.ParseResourcePath(read.Reference); !valid || !read.Success || read.Index == nil ||
					*read.Index < 0 || *read.Index >= len(result.Images) || data.KnowledgeID == "" || read.KnowledgeID != data.KnowledgeID {
					continue
				}
				fetched := false
				for _, chunk := range chunks {
					fetched = fetched || (chunk.ID == read.ChunkID && chunk.KnowledgeID == read.KnowledgeID)
				}
				pixels, err := decodeDataURIBytes(result.Images[*read.Index])
				hash := sha256.Sum256(pixels)
				if !fetched || err != nil || len(pixels) == 0 || hex.EncodeToString(hash[:]) != read.SHA256 {
					continue
				}
				for _, image := range data.Catalog {
					if image.Reference != read.Reference || image.ChunkID != read.ChunkID || image.KnowledgeID != read.KnowledgeID || image.Page != read.Page {
						continue
					}
					observation := ""
					if *read.Index < len(result.ImageDescriptions) {
						observation = strings.TrimSpace(result.ImageDescriptions[*read.Index])
					}
					context := []rune(image.Context)
					if len(context) > 300 {
						context = context[:300]
					}
					view := map[string]any{"reference": read.Reference, "knowledge_id": read.KnowledgeID,
						"chunk_id": read.ChunkID, "page": read.Page, "label": image.Label, "source_context": string(context),
						"sha256": read.SHA256, "evidence_version": read.Version, "original_pixels_verified": true,
						"returned_image_index": *read.Index, "vision_observation": observation}
					if observation != "" {
						pointer := fmt.Sprintf("/data/image_descriptions/%d", *read.Index)
						if rawID, ok := exactImageDescriptionSourceID(sourceMessages, call, pointer, observation); ok {
							view["source_evidence_id"] = rawID
							view["source_json_pointers"] = map[string]string{"vision_observation": pointer}
						}
					}
					for geometryIndex, figure := range data.Geometry {
						if figure.Reference != read.Reference || figure.ChunkID != read.ChunkID || figure.KnowledgeID != read.KnowledgeID || figure.Page != read.Page {
							continue
						}
						compacted := compactOriginalFigureEvidence(figure.Evidence)
						if compacted == nil {
							continue
						}
						view["original_geometry_evidence"] = compacted
						pointer := fmt.Sprintf("/data/image_geometry_evidence/%d/evidence", geometryIndex)
						if rawID, ok := exactImageGeometrySourceID(sourceMessages, call, pointer, view, compacted); ok {
							view["source_evidence_id"] = rawID
							pointers, _ := view["source_json_pointers"].(map[string]string)
							if pointers == nil {
								pointers = map[string]string{}
							}
							pointers["original_geometry_evidence"] = pointer
							view["source_json_pointers"] = pointers
							view["geometry_source_path_guide"] = map[string]string{
								"source_samples": "Use " + pointer + "/geometry/series/<i>/source_samples/<j>/<field>; this is the complete source path template for the bounded projection of actual samples.",
								"key_points":     "Use " + pointer + "/geometry/series/<i>/key_points/<j>/<field>; this is the complete source path template. Calibrated x positions use axis_position fields in this source projection.",
							}
						}
					}
					if geometry, ok := view["original_geometry_evidence"].(map[string]any); ok && geometry != nil {
						view["vision_observation"] = ""
						view["vision_observation_recorded"] = observation != ""
						view["vision_observation_scope"] = "Model-generated observation retained in the audit/UI, not source measurements. Use this exact original figure's verified coordinates for factual review."
					}
					encoded, _ := json.Marshal(view)
					messages = append(messages, chat.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(encoded)})
					break
				}
			}
		}
	}
	views, scope := boundedReviewEvidence(messages, 131072)
	for i := range views {
		content := reviewAsMap(views[i].Content)
		if content == nil {
			continue
		}
		if rawID, ok := content["source_evidence_id"].(string); ok && rawID != "" {
			// Replace the synthetic summary ID with the immutable source payload ID.
			views[i].EvidenceID = rawID
			for _, sourceMessage := range sourceMessages {
				if sourceMessage.Role == "tool" && reviewEvidenceID(sourceMessage) == rawID {
					views[i].OriginalBytes = len(sourceMessage.Content)
					break
				}
			}
			content["source_projection_note"] = "This is a bounded projection, not raw result content. evidence_id identifies the immutable source result; source_json_pointers locate exact values within it."
		} else {
			// A derived summary ID is never a valid source result locator.
			views[i].EvidenceID = ""
		}
		delete(content, "source_evidence_id")
		views[i].Content = content
	}
	return views, scope
}

func exactImageDescriptionSourceID(sourceMessages []chat.Message, call types.ToolCall, pointer, expected string) (string, bool) {
	for _, message := range sourceMessages {
		if message.Role != "tool" || message.Name != call.Name || message.ToolCallID != call.ID {
			continue
		}
		var payload map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.Content))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil || payload["success"] != true {
			continue
		}
		value, err := reviewPointerValue(payload, pointer)
		text, isString := value.(string)
		if err == nil && isString && text == expected {
			return reviewEvidenceID(message), true
		}
	}
	return "", false
}

func exactImageGeometrySourceID(sourceMessages []chat.Message, call types.ToolCall, pointer string, projectedImage map[string]any, projectedGeometry any) (string, bool) {
	for _, message := range sourceMessages {
		if message.Role != "tool" || message.Name != call.Name || message.ToolCallID != call.ID {
			continue
		}
		var payload map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.Content))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil || payload["success"] != true {
			continue
		}
		value, err := reviewPointerValue(payload, pointer)
		if err != nil || !reviewJSONValuesEqual(value, projectedGeometry) {
			continue
		}
		data, _ := reviewPointerValue(payload, "/data")
		dataMap, _ := data.(map[string]any)
		if !reviewImageProjectionBoundToSource(dataMap["image_evidence"], projectedImage, projectedImage) {
			continue
		}
		return reviewEvidenceID(message), true
	}
	return "", false
}

// Preserve every trace returned by the validated source plan and its actual
// extent. Dense sample projections and omitted source traces remain explicit.
func compactOriginalFigureEvidence(evidence map[string]any) map[string]any {
	geometry, _ := evidence["geometry"].(map[string]any)
	available, _ := geometry["series"].([]any)
	if evidence["version"] != "source_figure_geometry_v1" || geometry["method"] != "original_emf_trace_geometry_v1" ||
		!validFigureDigest(evidence["source_file_sha256"]) || !validFigureDigest(evidence["source_media_sha256"]) || len(available) == 0 {
		return nil
	}
	view := map[string]any{}
	for _, key := range []string{"version", "source_file_sha256", "source_media_sha256", "source_page", "source_part", "source_labels"} {
		view[key] = evidence[key]
	}
	compact := map[string]any{}
	for _, key := range []string{"method", "unit", "series_count", "source_series_count", "series_selection", "omitted_series", "comparison_basis"} {
		compact[key] = geometry[key]
	}
	labels := []any{}
	anchors, _ := geometry["x_axis_anchors"].([]any)
	for _, raw := range anchors {
		if anchor, ok := raw.(map[string]any); ok {
			labels = append(labels, anchor["label"])
		}
	}
	compact["x_axis_labels_in_order"] = labels
	series := []map[string]any{}
	rows, _ := geometry["series"].([]any)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item := map[string]any{}
		for _, key := range []string{"legend", "color", "key_point_changes"} {
			item[key] = row[key]
		}
		keyPoints := []any{}
		rawPoints, _ := row["key_points"].([]any)
		for _, point := range rawPoints {
			keyPoints = append(keyPoints, reviewFigurePoint(point))
		}
		item["key_points"] = keyPoints
		if len(keyPoints) == 0 {
			for _, key := range []string{"first", "last", "minimum", "maximum"} {
				item[key] = reviewFigurePoint(row[key])
			}
		}
		samples, _ := row["samples"].([]any)
		selected := []map[string]any{}
		for _, rawPoint := range samples {
			point, ok := rawPoint.(map[string]any)
			if !ok {
				continue
			}
			selected = append(selected, reviewFigurePoint(point))
		}
		item["source_samples"] = selected
		item["source_sample_count"] = len(samples)
		item["sample_projection_complete"] = len(samples) == len(selected)
		series = append(series, item)
	}
	compact["series"] = series
	// Common definitions belong once beside the complete data, rather than
	// consuming the review budget at every point and silently evicting curves.
	compact["coordinate_scope"] = "Each axis_position interval STARTS at left_source_tick and has not reached right_source_tick. Its last third still belongs to that LEFT-tick interval. Percentages are local to that interval, not the whole axis."
	compact["key_points_scope"] = "Ordered plotted levels with source roles first/last/minimum/maximum, not net changes. estimate_low/high are program-calibrated geometric uncertainty, not values printed by the author."
	compact["key_point_changes_scope"] = "Deterministic differences between named plotted endpoints, not plotted levels. A different magnitude for a change and its destination level is not a contradiction."
	compact["color_scope"] = "The original EMF pen RGB links each legend to its actual trace; a standard color name does not need to be printed as OCR text."
	compact["omitted_fields"] = []string{"series.actual_point_count", "axis_fit_details", "x_axis_anchors.x_fraction (original label order retained)", "point.x_fraction when a calibrated bounding-tick interval is present"}
	compact["point_scope"] = "First/last/minimum/maximum are represented once by their roles in ordered key_points when available. Qualified approximate values within estimate_low/high may be rounded differently."
	view["geometry"] = compact
	return view
}

// Keep the actual bounding labels and express distance from the LEFT tick as
// an interval percentage, independently of whole-plot coordinates. This is
// source geometry, without interpreting labels as dates or business periods.
func reviewFigurePoint(raw any) map[string]any {
	point, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	view := map[string]any{}
	for key, value := range point {
		view[key] = value
	}
	interval, _ := point["x_axis_interval"].(map[string]any)
	fraction, valid := interval["fraction_between_labels"].(float64)
	if valid && fraction >= 0 && fraction <= 1 && interval["left_label"] != nil && interval["right_label"] != nil {
		delete(view, "x_fraction")
		delete(view, "x_axis_interval")
		view["axis_position"] = map[string]any{"left_source_tick": interval["left_label"], "right_source_tick": interval["right_label"],
			"containing_interval_starts_at_source_label": interval["left_label"],
			"percent_of_THIS_interval_from_LEFT_tick":    fraction * 100,
			"relative_band_from_LEFT_tick":               interval["relative_band"]}
	}
	return view
}

func validFigureDigest(value any) bool {
	text, ok := value.(string)
	if !ok || len(text) != 64 {
		return false
	}
	_, err := hex.DecodeString(text)
	return err == nil
}

// Only the same source-bound pixels used by the document evidence can enter
// visual review. Captions, failed reads and arbitrary image URLs grant nothing.
func documentReviewImageInputs(state *types.AgentState) ([]string, map[string]any) {
	images := []string{}
	bindings := []map[string]any{}
	seen := map[string]int{}
	totalBytes, omitted := 0, 0
	if state != nil {
		for i := len(state.RoundSteps) - 1; i >= 0; i-- {
			calls := state.RoundSteps[i].ToolCalls
			for j := len(calls) - 1; j >= 0; j-- {
				call := calls[j]
				views, _ := documentImageConsistencySummaries(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}})
				for _, entry := range views {
					view, ok := entry.Content.(map[string]any)
					if !ok || view["original_pixels_verified"] != true || call.Result == nil {
						continue
					}
					index, err := strconv.Atoi(fmt.Sprint(view["returned_image_index"]))
					if err != nil || index < 0 || index >= len(call.Result.Images) {
						continue
					}
					key := fmt.Sprint(view["reference"]) + ":" + fmt.Sprint(view["sha256"])
					number, exists := seen[key]
					if !exists {
						pixels, err := decodeDataURIBytes(call.Result.Images[index])
						if err != nil || len(images) >= 4 || totalBytes+len(pixels) > 32<<20 {
							seen[key] = 0
							omitted++
							continue
						}
						images = append(images, call.Result.Images[index])
						totalBytes += len(pixels)
						number = len(images)
						seen[key] = number
					}
					if number == 0 {
						continue
					}
					binding := map[string]any{"attached_image_number": number}
					for _, key := range []string{"reference", "knowledge_id", "chunk_id", "page", "sha256"} {
						binding[key] = view[key]
					}
					bindings = append(bindings, binding)
				}
			}
		}
	}
	return images, map[string]any{"image_count": len(images), "image_bytes": totalBytes, "bindings": bindings,
		"omitted_image_count": omitted, "scope": "Only attached, source-verified pixels are visually available. Omitted images are unknown, not absent; retain original source scopes and coordinate evidence."}
}

// Coordinate evidence must match fetched document/chunk/page and returned
// pixels before it can replace fallible model guesses in a fact-only view.
// The original observations are never modified; UI/history/trace retain them.
func nativeGeometryImageIndexes(call types.ToolCall) map[int]bool {
	indexes := map[int]bool{}
	views, _ := documentImageConsistencySummaries(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}})
	for _, entry := range views {
		view, ok := entry.Content.(map[string]any)
		if !ok {
			continue
		}
		geometry, ok := view["original_geometry_evidence"].(map[string]any)
		if !ok || geometry == nil || view["original_pixels_verified"] != true {
			continue
		}
		index, err := strconv.Atoi(fmt.Sprint(view["returned_image_index"]))
		if err == nil {
			indexes[index] = true
		}
	}
	return indexes
}

func sourceReviewToolData(call types.ToolCall, native map[int]bool) map[string]any {
	if call.Result == nil {
		return nil
	}
	if len(native) == 0 {
		return call.Result.Data
	}
	data := map[string]any{}
	for key, value := range call.Result.Data {
		data[key] = value
	}
	encoded, _ := json.Marshal(data["image_descriptions"])
	var descriptions []string
	if json.Unmarshal(encoded, &descriptions) == nil {
		for index := range native {
			if index < len(descriptions) {
				descriptions[index] = ""
			}
		}
		data["image_descriptions"] = descriptions
	}
	data["vision_description_scope"] = "Observations for exact images with verified original geometry are recorded for audit, not promoted to source facts."
	encoded, _ = json.Marshal(data["image_geometry_evidence"])
	var figures []map[string]any
	if json.Unmarshal(encoded, &figures) == nil {
		for _, figure := range figures {
			if original, ok := figure["evidence"].(map[string]any); ok {
				if compact := compactOriginalFigureEvidence(original); compact != nil {
					figure["evidence"] = compact
				}
			}
		}
		data["image_geometry_evidence"] = figures
	}
	return data
}
