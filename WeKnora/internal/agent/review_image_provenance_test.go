package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Candidate tests use the real source projection, immutable reader, image
// adapter and receipt visibility gate. They verify provenance/exposure only.
func TestDocumentImageProjectionUsesImmutableVisionLocator(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	result.Data["image_descriptions"] = append([]string(nil), result.ImageDescriptions...)
	engine := sourceConsistencyEngine(t, &mockChat{})
	raw := engine.sourceConsistencyMessages(state, nil)
	reader := newReviewEvidenceReader(raw)
	views, _ := documentImageConsistencySummariesWithSources(state, raw)
	require.Len(t, views, 1)
	entry := views[0]
	content := reviewAsMap(entry.Content)
	rawID := entry.EvidenceID
	require.NotEmpty(t, rawID)
	require.NotContains(t, content, "source_evidence_id")
	require.Contains(t, content["source_projection_note"], "bounded projection")
	pointers := reviewAsMap(content["source_json_pointers"])
	pointer := pointers["vision_observation"].(string)
	require.Equal(t, "/data/image_descriptions/0", pointer)
	input := map[string]any{"document_image_observations": views}
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: rawID, JSONPointer: pointer, Quote: result.ImageDescriptions[0], Kind: "source",
	}))
	for name, witness := range map[string]reviewSourceWitness{
		"synthetic-id":      {EvidenceID: "derived-summary-id", JSONPointer: pointer, Quote: result.ImageDescriptions[0], Kind: "source"},
		"summary-path":      {EvidenceID: rawID, JSONPointer: "/content/vision_observation", Quote: result.ImageDescriptions[0], Kind: "source"},
		"wrong-image-index": {EvidenceID: rawID, JSONPointer: "/data/image_descriptions/1", Quote: result.ImageDescriptions[0], Kind: "source"},
		"wrong-quote":       {EvidenceID: rawID, JSONPointer: pointer, Quote: "different observation", Kind: "source"},
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, reviewWitnessVisible(input, reader, witness)) })
	}
	result.Success = false
	failedRaw := engine.sourceConsistencyMessages(state, nil)
	failedViews, _ := documentImageConsistencySummariesWithSources(state, failedRaw)
	require.Empty(t, failedViews)
}

func TestDocumentImageProjectionUsesExactGeometryRootAndLeafPaths(t *testing.T) {
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	source := result.Data["image_evidence"].([]map[string]any)[0]
	figure := map[string]any{}
	for key, value := range source {
		figure[key] = value
	}
	point := map[string]any{"approximate_value": 12, "estimate_low": 11, "estimate_high": 13, "x_fraction": 0.5,
		"x_axis_interval": map[string]any{"left_label": "tick A", "right_label": "tick B", "fraction_between_labels": 0.25}}
	figure["evidence"] = map[string]any{"version": "source_figure_geometry_v1", "source_file_sha256": strings.Repeat("a", 64),
		"source_media_sha256": strings.Repeat("b", 64), "geometry": map[string]any{"method": "original_emf_trace_geometry_v1",
			"series": []map[string]any{{"legend": "Series A", "key_points": []map[string]any{point}, "samples": []map[string]any{point}}}}}
	// Add a second distinct returned image and geometry record with the same
	// leaf text so path binding, rather than quote coincidence, decides support.
	result.Images = append(result.Images, result.Images[0])
	result.ImageDescriptions = append(result.ImageDescriptions, result.ImageDescriptions[0])
	result.Data["image_descriptions"] = append([]string(nil), result.ImageDescriptions...)
	readRows := result.Data["image_evidence"].([]map[string]any)
	secondRead := map[string]any{}
	for key, value := range readRows[0] {
		secondRead[key] = value
	}
	secondRead["reference"] = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	secondRead["chunk_id"], secondRead["page"], secondRead["returned_image_index"] = "second-chunk", 8, 1
	result.Data["image_evidence"] = append(readRows, secondRead)
	catalogRows := result.Data["image_catalog"].([]map[string]any)
	secondCatalog := map[string]any{}
	for key, value := range catalogRows[0] {
		secondCatalog[key] = value
	}
	secondCatalog["reference"], secondCatalog["chunk_id"], secondCatalog["page"] = secondRead["reference"], "second-chunk", 8
	result.Data["image_catalog"] = append(catalogRows, secondCatalog)
	chunkRows := result.Data["chunks"].([]map[string]any)
	result.Data["chunks"] = append(chunkRows, map[string]any{"chunk_id": "second-chunk", "knowledge_id": "actual-document", "page": 8, "content": "Second image source."})
	secondFigure := map[string]any{}
	for key, value := range figure {
		secondFigure[key] = value
	}
	secondFigure["reference"], secondFigure["chunk_id"], secondFigure["page"] = secondRead["reference"], "second-chunk", 8
	result.Data["image_geometry_evidence"] = []map[string]any{figure, secondFigure}
	engine := sourceConsistencyEngine(t, &mockChat{})
	raw := engine.sourceConsistencyMessages(state, nil)
	reader := newReviewEvidenceReader(raw)
	views, _ := documentImageConsistencySummariesWithSources(state, raw)
	require.Len(t, views, 2)
	entry := views[0]
	content := reviewAsMap(entry.Content)
	rawID := entry.EvidenceID
	require.NotEmpty(t, rawID)
	require.NotContains(t, content, "source_evidence_id")
	require.Contains(t, content["source_projection_note"], "bounded projection")
	pointers := reviewAsMap(content["source_json_pointers"])
	root := pointers["original_geometry_evidence"].(string)
	require.Equal(t, "/data/image_geometry_evidence/0/evidence", root)
	secondContent := reviewAsMap(views[1].Content)
	secondPointers := reviewAsMap(secondContent["source_json_pointers"])
	require.Equal(t, "/data/image_geometry_evidence/1/evidence", secondPointers["original_geometry_evidence"])
	input := map[string]any{"document_image_observations": views}
	witness := func(pointer, quote string) reviewSourceWitness {
		return reviewSourceWitness{EvidenceID: rawID, JSONPointer: pointer, Quote: quote, Kind: "source"}
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness(root+"/geometry/series/0/source_samples/0/approximate_value", "12")))
	require.NoError(t, reviewWitnessVisible(input, reader, witness(root+"/geometry/series/0/key_points/0/axis_position/left_source_tick", "tick A")))
	for name, bad := range map[string]reviewSourceWitness{
		"hidden-field":           witness(root+"/geometry/series/0/source_samples/0/secret", "hidden"),
		"transformed-away-field": witness(root+"/geometry/series/0/key_points/0/x_fraction", "0.5"),
		"container-quote":        witness(root, fmt.Sprint(content["original_geometry_evidence"])),
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, reviewWitnessVisible(input, reader, bad)) })
	}
	wrongIndex := witness("/data/image_geometry_evidence/1/evidence/geometry/series/0/source_samples/0/approximate_value", "12")
	require.Error(t, reviewWitnessVisible(map[string]any{"document_image_observations": views[:1]}, reader, wrongIndex),
		"a valid same-quote leaf from another image cannot be borrowed by the first image projection")
	require.NoError(t, reviewWitnessVisible(map[string]any{"document_image_observations": views[1:]}, reader,
		witness("/data/image_geometry_evidence/1/evidence/geometry/series/0/source_samples/0/approximate_value", "12")))
}
