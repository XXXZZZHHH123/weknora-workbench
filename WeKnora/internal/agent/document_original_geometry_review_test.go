package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func originalGeometryReviewState() *types.AgentState {
	state := boundGeometryOnlyDocumentState()
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	data["knowledge_base_id"] = "actual-kb"
	data["original_image_catalog_status"] = "verified_page_relationships"
	data["document_scope"] = map[string]any{"knowledge_id": "actual-document", "page": 7, "matched": true, "plan_validated": true}
	reference := types.OriginalMediaReference{KnowledgeID: "actual-document", KnowledgeBaseID: "actual-kb", SourceSHA256: strings.Repeat("a", 64), MediaSHA256: strings.Repeat("b", 64), Page: 7, Part: "ppt/media/plot.emf"}.Reference()
	for _, field := range []string{"image_catalog", "image_evidence", "image_geometry_evidence"} {
		data[field].([]map[string]any)[0]["reference"] = reference
	}
	data["image_catalog"].([]map[string]any)[0]["page_verified"] = true
	return state
}

func originalPixelsReviewState() *types.AgentState {
	state := originalGeometryReviewState()
	result := state.RoundSteps[0].ToolCalls[0].Result
	result.Data["image_mode"] = "pixels"
	delete(result.Data, "image_geometry_evidence")
	bytes := []byte("rendered PNG bytes distinct from original EMF bytes")
	digest := sha256.Sum256(bytes)
	result.Images = []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(bytes)}
	result.ImageDescriptions = []string{"The selected original chart has two visible curves."}
	read := result.Data["image_evidence"].([]map[string]any)[0]
	read["read_success"], read["returned_image_index"], read["sha256"] = true, 0, hex.EncodeToString(digest[:])
	delete(read, "pixel_status")
	read["geometry_read_success"] = false
	return state
}

func TestOriginalMediaPixelsReviewPreservesPixelsWithoutGrantingGeometry(t *testing.T) {
	state := originalPixelsReviewState()
	views, scope := documentImageConsistencySummariesWithSources(state, sourceResultMessagesFromSteps(state.RoundSteps))
	require.True(t, scope.ViewComplete)
	require.Len(t, views, 1)
	view := reviewAsMap(views[0].Content)
	require.Equal(t, true, view["original_pixels_verified"])
	require.Equal(t, "The selected original chart has two visible curves.", view["vision_observation"])
	require.NotContains(t, view, "original_geometry_evidence")
	require.NotEqual(t, strings.Repeat("b", 64), view["sha256"], "PNG digest must not be confused with original EMF media digest")
}

func TestOriginalMediaPixelsDoNotLaunderStaleOptionalGeometry(t *testing.T) {
	state := originalPixelsReviewState()
	stale := originalGeometryReviewState().RoundSteps[0].ToolCalls[0].Result.Data["image_geometry_evidence"]
	stale.([]map[string]any)[0]["evidence"].(map[string]any)["source_file_sha256"] = strings.Repeat("c", 64)
	state.RoundSteps[0].ToolCalls[0].Result.Data["image_geometry_evidence"] = stale
	views, _ := documentImageConsistencySummariesWithSources(state, sourceResultMessagesFromSteps(state.RoundSteps))
	require.Len(t, views, 1)
	require.Equal(t, true, reviewAsMap(views[0].Content)["original_pixels_verified"])
	require.NotContains(t, reviewAsMap(views[0].Content), "original_geometry_evidence")
}

func TestOriginalMediaPixelsReviewRejectsUnboundOrChangedRaster(t *testing.T) {
	for _, variant := range []string{"fake-reference", "stale-read-reference", "cross-document", "cross-kb", "cross-page", "cross-part", "scope-unvalidated", "catalog-unverified", "page-unverified", "chunk-missing", "failed-read", "failed-pixels", "wrong-version", "wrong-png-hash", "no-pixels", "wrong-tool"} {
		t.Run(variant, func(t *testing.T) {
			state := originalPixelsReviewState()
			call := &state.RoundSteps[0].ToolCalls[0]
			data := call.Result.Data
			read := data["image_evidence"].([]map[string]any)[0]
			selector, err := types.ParseOriginalMediaReference(read["reference"].(string))
			require.NoError(t, err)
			switch variant {
			case "fake-reference":
				read["reference"] = "original-media://fake"
			case "stale-read-reference":
				selector.SourceSHA256 = strings.Repeat("c", 64)
				read["reference"] = selector.Reference()
			case "cross-document":
				data["knowledge_id"] = "other-document"
			case "cross-kb":
				data["knowledge_base_id"] = "other-kb"
			case "cross-page":
				read["page"] = 8
			case "cross-part":
				data["image_catalog"].([]map[string]any)[0]["source_part"] = "ppt/media/other.emf"
			case "scope-unvalidated":
				data["document_scope"].(map[string]any)["matched"] = false
			case "catalog-unverified":
				delete(data, "original_image_catalog_status")
			case "page-unverified":
				data["image_catalog"].([]map[string]any)[0]["page_verified"] = false
			case "chunk-missing":
				data["chunks"] = []map[string]any{}
			case "failed-read":
				call.Result.Success = false
			case "failed-pixels":
				read["read_success"] = false
			case "wrong-version":
				read["version"] = "untrusted"
			case "wrong-png-hash":
				read["sha256"] = selector.MediaSHA256
			case "no-pixels":
				call.Result.Images = nil
			case "wrong-tool":
				call.Name = "untrusted_tool"
			}
			views, _ := documentImageConsistencySummariesWithSources(state, sourceResultMessagesFromSteps(state.RoundSteps))
			require.Empty(t, views)
		})
	}
}

func TestOriginalMediaGeometryReviewRequiresIndependentCurrentBindings(t *testing.T) {
	state := originalGeometryReviewState()
	messages := sourceResultMessagesFromSteps(state.RoundSteps)
	views, scope := documentImageConsistencySummariesWithSources(state, messages)
	require.True(t, scope.ViewComplete)
	require.Len(t, views, 1)
	require.Equal(t, reviewEvidenceID(messages[0]), views[0].EvidenceID)
	require.Equal(t, false, reviewAsMap(views[0].Content)["original_pixels_verified"])
	require.Contains(t, reviewAsMap(views[0].Content)["source_json_pointers"], "original_geometry_evidence")
}

func TestOriginalMediaGeometryReviewRejectsFakeStaleCrossScopeOrFailedReads(t *testing.T) {
	for _, variant := range []string{"fake-reference", "noncanonical", "source-hash", "media-hash", "cross-document", "cross-kb", "cross-page", "cross-part", "scope-page", "scope-unvalidated", "catalog-unverified", "catalog-missing", "page-unverified", "chunk-missing", "failed-read", "failed-geometry", "wrong-tool"} {
		t.Run(variant, func(t *testing.T) {
			state := originalGeometryReviewState()
			call := &state.RoundSteps[0].ToolCalls[0]
			data := call.Result.Data
			figure := data["image_geometry_evidence"].([]map[string]any)[0]
			selector, err := types.ParseOriginalMediaReference(figure["reference"].(string))
			require.NoError(t, err)
			switch variant {
			case "fake-reference":
				figure["reference"] = "original-media://fake"
			case "noncanonical":
				figure["reference"] = figure["reference"].(string) + "="
			case "source-hash":
				selector.SourceSHA256 = strings.Repeat("c", 64)
			case "media-hash":
				selector.MediaSHA256 = strings.Repeat("c", 64)
			case "cross-document":
				selector.KnowledgeID = "other-document"
			case "cross-kb":
				selector.KnowledgeBaseID = "other-kb"
			case "cross-page":
				selector.Page = 8
			case "cross-part":
				selector.Part = "ppt/media/other.emf"
			case "scope-page":
				data["document_scope"].(map[string]any)["page"] = 8
			case "scope-unvalidated":
				data["document_scope"].(map[string]any)["plan_validated"] = false
			case "catalog-unverified":
				delete(data, "original_image_catalog_status")
			case "catalog-missing":
				data["image_catalog"] = []map[string]any{}
			case "page-unverified":
				data["image_catalog"].([]map[string]any)[0]["page_verified"] = false
			case "chunk-missing":
				data["chunks"] = []map[string]any{}
			case "failed-read":
				call.Result.Success = false
			case "failed-geometry":
				data["image_evidence"].([]map[string]any)[0]["geometry_read_success"] = false
			case "wrong-tool":
				call.Name = "untrusted_tool"
			}
			// Change every locator copy together: internally matching fake catalog
			// entries still cannot override independently verified geometry metadata.
			if strings.HasPrefix(variant, "cross-") || variant == "source-hash" || variant == "media-hash" {
				for _, field := range []string{"image_catalog", "image_evidence", "image_geometry_evidence"} {
					data[field].([]map[string]any)[0]["reference"] = selector.Reference()
				}
			}
			views, _ := documentImageConsistencySummariesWithSources(state, sourceResultMessagesFromSteps(state.RoundSteps))
			require.Empty(t, views)
		})
	}
}
