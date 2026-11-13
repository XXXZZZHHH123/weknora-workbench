package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func originalCatalogCodecFixture() *types.ToolResult {
	selector := (types.OriginalMediaReference{KnowledgeID: "actual-original-doc", KnowledgeBaseID: "actual-kb", SourceSHA256: strings.Repeat("a", 64), Page: 2, Part: "ppt/media/actual.emf", MediaSHA256: strings.Repeat("b", 64)}).Reference()
	return &types.ToolResult{Success: true, Output: "RAW_OUTPUT_MUST_NOT_SUPPLY_CATALOG_COMPLETENESS", Data: map[string]any{
		"display_type": "knowledge_chunks_list", "knowledge_id": "actual-original-doc", "knowledge_title": "Source",
		"document":       map[string]any{"knowledge_id": "actual-original-doc", "title": "Source"},
		"document_scope": map[string]any{"matched": true, "knowledge_id": "actual-original-doc", "page": 2},
		"chunks":         []map[string]any{{"knowledge_id": "actual-original-doc", "chunk_id": "actual-owned-chunk", "content": "A body fragment, not image values.", "page": 2}},
		"fetched_chunks": 1, "page_chunks": 3, "offset": 0, "next_offset": 1, "page_complete": false,
		"image_catalog": []map[string]any{{"reference": selector, "knowledge_id": "actual-original-doc", "chunk_id": "actual-owned-chunk", "page": 2, "source_part": "ppt/media/actual.emf", "read_args": map[string]any{"id": "actual-original-doc", "page": 2, "images": []string{selector}, "image_mode": "pixels"}}},
	}}
}

func TestOriginalImageCatalogCompletenessSurvivesFinalModelProjection(t *testing.T) {
	result := originalCatalogCodecFixture()
	result.Data["original_image_catalog_status"] = "verified_page_relationships"
	result.Data["original_image_catalog_pages"] = []int{2}
	result.Data["original_image_catalog_complete"] = true
	before, err := json.Marshal(result)
	require.NoError(t, err)
	text := NewRegistry(true).ModelToolResultForTool("read_document", result)
	require.NotContains(t, text, result.Output, "the structured source codec must really rebuild this result")
	require.Contains(t, text, `"original_image_catalog_status":"verified_page_relationships"`)
	require.Contains(t, text, `"original_image_catalog_pages":[2]`)
	require.Contains(t, text, `"original_image_catalog_complete":true`)
	require.Contains(t, text, "This physical page is not fully read")
	require.Contains(t, text, "further body offsets are not required")
	require.Contains(t, text, "only the physical pages listed")
	require.Contains(t, text, "neither pixel-read success nor numeric geometry evidence")
	require.Contains(t, text, `"image_mode":"pixels"`)
	require.Contains(t, text, "res://", "an actual selectable request-local image alias survives")
	require.NotContains(t, text, "original-media://")
	require.NotContains(t, text, `"read_success":true`)
	require.NotContains(t, text, `"geometry_read_success":true`)
	require.NotContains(t, text, "<original_figure_geometry")
	require.Empty(t, result.Images)
	after, err := json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestOriginalImageCatalogFailureAndBudgetRemainIncompleteInModelProjection(t *testing.T) {
	for _, variant := range []string{"verification-failure", "budget-limit", "verified-empty"} {
		t.Run(variant, func(t *testing.T) {
			result := originalCatalogCodecFixture()
			if variant == "verified-empty" {
				result.Data["image_catalog"] = []any{}
				result.Data["original_image_catalog_status"] = "verified_page_relationships"
				result.Data["original_image_catalog_pages"] = []int{2}
				result.Data["original_image_catalog_complete"] = true
			} else {
				result.Data["original_image_catalog_error"] = "Current original relationship verification unavailable; existing catalog is not complete."
				result.Data["original_image_catalog_complete"] = false
				if variant == "budget-limit" {
					result.Data["original_image_catalog_total"] = 48
					result.Data["original_image_catalog_limit"] = 32
					result.Data["original_image_catalog_truncated"] = true
				}
			}
			text := NewRegistry(true).ModelToolResultForTool("read_document", result)
			require.NotContains(t, text, `"read_success":true`)
			require.NotContains(t, text, "<original_figure_geometry")
			if variant == "verified-empty" {
				require.Contains(t, text, `"original_image_catalog_complete":true`)
				require.Contains(t, text, `"original_image_catalog_pages":[2]`)
				require.NotContains(t, text, `"image_catalog":`)
			} else {
				require.Contains(t, text, `"original_image_catalog_complete":false`)
				require.NotContains(t, text, `"original_image_catalog_complete":true`)
				require.Contains(t, text, `"original_image_catalog_error":`)
				require.NotContains(t, text, `"original_image_catalog_pages":`)
				require.Contains(t, text, "never proves that missing figures do not exist")
				if variant == "budget-limit" {
					require.Contains(t, text, `"original_image_catalog_total":48`)
					require.Contains(t, text, `"original_image_catalog_limit":32`)
					require.Contains(t, text, `"original_image_catalog_truncated":true`)
				}
			}
		})
	}
}

func TestOptionalGeometryNavigationDoesNotErasePixelsInModelProjection(t *testing.T) {
	result := originalCatalogCodecFixture()
	result.Data["image_evidence"] = []map[string]any{{"reference": "resource://AbCdEfGhIjKlMnOpQrStUv", "read_success": true, "geometry_read_success": false, "geometry_status": "selection_required", "geometry_selection_options": map[string]any{"available_series": []string{"Alpha", "Beta"}, "notice": "Original pixels returned; optional geometry not read."}}}
	result.Data["image_geometry_evidence"] = []any{}
	text := NewRegistry(true).ModelToolResultForTool("read_document", result)
	require.Contains(t, text, `"read_success":true`)
	require.Contains(t, text, `"geometry_read_success":false`)
	require.Contains(t, text, `"geometry_status":"selection_required"`)
	require.Contains(t, text, `"available_series":["Alpha","Beta"]`)
	require.NotContains(t, text, "<original_figure_geometry")
	require.NotContains(t, text, `"geometry_read_success":true`)
}
