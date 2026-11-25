package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStructuredResultImageReviewChecksBoundOCRAgainstVision(t *testing.T) {
	const candidate = "The printed reading is 84.8 widgets."
	const corrected = "The transcription shows 84.6 widgets and the visual reading was about 84.8 widgets. The difference remains unverified."
	e, _, model := lightGeometryEngine(t, lightReviewEdits(structuredResultEdit{candidate, corrected}))
	state := boundDocumentImageState()
	call := state.RoundSteps[0].ToolCalls[0]
	result := call.Result
	result.Data["display_type"] = "knowledge_chunks_list"
	result.Data["knowledge_base_id"] = "granted-base"
	result.Data["document_scope"] = map[string]any{"matched": true, "plan_validated": true, "knowledge_id": "actual-document", "page": 7}
	row := result.Data["chunks"].([]map[string]any)[0]
	row["content"], row["content_revision"] = "A scanned register with a widget count.", 0
	result.ImageDescriptions = []string{"Delta visually appears to be about 84.8 widgets."}
	result.Data["image_descriptions"] = result.ImageDescriptions
	annotation := map[string]any{"reference": "resource://AbCdEfGhIjKlMnOpQrStUv", "stored_reference": "resource://AbCdEfGhIjKlMnOpQrStUv",
		"knowledge_id": "actual-document", "chunk_id": "actual-chunk", "parent_chunk_id": "actual-chunk", "page": 7}
	annotation["child_chunk_id"], annotation["kind"], annotation["text"] = "ocr-register", "image_ocr", "Group\tReading (widgets)\nDelta\t84.6"
	caption := make(map[string]any)
	otherImage := make(map[string]any)
	for key, value := range annotation {
		caption[key], otherImage[key] = value, value
	}
	caption["child_chunk_id"], caption["kind"], caption["text"] = "caption-register", "image_caption", "Printed counts for the groups."
	otherImage["reference"], otherImage["stored_reference"], otherImage["text"] = "resource://UvTsRqPoNmLkJiHgFeDcBa", "resource://UvTsRqPoNmLkJiHgFeDcBa", "Another figure records 93.1."
	result.Data["image_text_evidence"] = []map[string]any{annotation, caption, otherImage}
	e.appendToolResults(nil, state.RoundSteps[0])

	values, _ := currentReadDocumentNumbers(state.RoundSteps)
	require.Contains(t, values, 84.6, "OCR from the fetched image must pass numeric traceability")
	require.NotContains(t, values, 93.1, "a neighboring reference cannot lend numbers to the fetched image")
	before := reviewDigest(state.RoundSteps)
	d := e.checkStructuredResultDelivery(t.Context(), "Read the printed Delta count.", candidateDecision{Disposition: candidateAccepted, Candidate: candidate}, state, nil)
	require.Equal(t, "corrected", d.StructuredResultReviewStatus)
	require.Equal(t, corrected, d.Candidate)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Len(t, model.calls, 1)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	images := reviewAsSlice(input["actual_current_document_images"])
	require.Len(t, images, 3)
	visual := reviewAsMap(reviewAsMap(images[0])["content"])
	require.Equal(t, 7, reviewInt(visual["page"]))
	require.Equal(t, result.ImageDescriptions[0], visual["vision_observation"])
	ocrView := reviewAsMap(images[1])
	ocr := reviewAsMap(ocrView["content"])
	require.Equal(t, annotation["text"], ocr["text"])
	require.Equal(t, visual["reference"], ocr["reference"])
	require.Equal(t, caption["text"], reviewAsMap(reviewAsMap(images[2])["content"])["text"])
	require.Contains(t, model.calls[0][0].Content, "without silently choosing a reading")

	sources := sourceResultMessagesFromSteps(state.RoundSteps)
	canonicalImages, _ := documentImageConsistencySummariesWithSources(state, sources)
	require.NoError(t, reviewWitnessVisible(map[string]any{"document_image_observations": canonicalImages}, newReviewEvidenceReader(sources),
		reviewSourceWitness{EvidenceID: ocrView["evidence_id"].(string), JSONPointer: "/data/image_text_evidence/0/text", Quote: "84.6", Kind: "source"}))
}

func TestStructuredResultImageReviewKeepsPixelObservationsAlongsideGeometry(t *testing.T) {
	const candidate = "Alpha falls and then rebounds, ending at approximately 6% within the drawn interval."
	e, _, model := lightGeometryEngine(t, discoveryReply("keep", ""), lightReviewReply("keep", ""))
	state := lightGeometryFixture()
	for i, description := range []string{"Alpha visually falls to about −5% and rebounds to around 6%.", "Alpha looks like it ends near the trough at about −4%."} {
		pixels := boundDocumentImageState().RoundSteps[0]
		call := &pixels.ToolCalls[0]
		call.ID = []string{"first-pixels", "repeated-pixels"}[i]
		call.Result.Data["document_scope"] = state.RoundSteps[0].ToolCalls[0].Result.Data["document_scope"]
		call.Result.ImageDescriptions = []string{description}
		call.Result.Data["image_descriptions"] = call.Result.ImageDescriptions
		state.RoundSteps = append(state.RoundSteps, pixels)
	}
	before := reviewDigest(state.RoundSteps)
	d := e.checkCandidate(t.Context(), "Describe Alpha's direction within the drawn interval.", candidate, "", state, nil, false)
	require.Equal(t, candidateAccepted, d.Disposition)
	require.Equal(t, candidate, d.Candidate)
	require.Equal(t, "kept", d.StructuredResultReviewStatus)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Len(t, model.calls, 2)
	var input map[string]any
	editor := model.calls[1]
	require.NoError(t, json.Unmarshal([]byte(editor[1].Content), &input))
	require.Contains(t, input, "actual_original_geometry")
	images := reviewAsSlice(input["actual_current_document_images"])
	require.Len(t, images, 2)
	first := reviewAsMap(reviewAsMap(images[0])["content"])
	second := reviewAsMap(reviewAsMap(images[1])["content"])
	require.Equal(t, first["sha256"], second["sha256"])
	require.NotEmpty(t, first["sha256"])
	require.Contains(t, first["vision_observation"], "rebounds")
	require.Contains(t, second["vision_observation"], "near the trough")
	require.Contains(t, editor[0].Content, "Retain approximate language")
	require.Contains(t, editor[0].Content, "Never replace supported image findings")
}

func TestStructuredResultImageReviewRetainsQualitativeReadingWithoutGeometry(t *testing.T) {
	const candidate = "Alpha appears to dip and then recover part of the decline."
	e, _, model := lightGeometryEngine(t, lightReviewReply("keep", ""))
	state := boundDocumentImageState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	result.Data["display_type"] = "knowledge_chunks_list"
	result.Data["knowledge_base_id"] = "granted-base"
	result.Data["document_scope"] = map[string]any{"matched": true, "plan_validated": true, "knowledge_id": "actual-document", "page": 7}
	row := result.Data["chunks"].([]map[string]any)[0]
	row["content"], row["content_revision"] = "The plot shows the Alpha label and tick marks.", 0
	result.Data["image_descriptions"] = result.ImageDescriptions
	e.appendToolResults(nil, state.RoundSteps[0])

	d := e.checkStructuredResultDelivery(t.Context(), "Describe the visible direction.", candidateDecision{Disposition: candidateAccepted, Candidate: candidate}, state, nil)
	require.Equal(t, "kept", d.StructuredResultReviewStatus)
	require.Equal(t, candidate, d.Candidate)
	require.Len(t, model.calls, 1)
	require.Contains(t, model.calls[0][1].Content, "visible trough followed by a rebound")
	require.NotContains(t, model.calls[0][1].Content, "actual_original_geometry")
}
