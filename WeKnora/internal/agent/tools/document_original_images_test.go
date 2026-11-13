package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOriginalImageCatalogCompletesPartialStoredPageAndKeepsScope(t *testing.T) {
	tool, repo := newReadDocumentFixture(3)
	tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].FileType = "pptx"
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\nFirst"
	repo.ordered[1].Content = "<!-- Slide number: 2 -->\n![Decoration](resource://AbCdEfGhIjKlMnOpQrStUv)\nOriginal figure missing in legacy import"
	repo.ordered[2].Content = "<!-- Slide number: 3 -->\nThird"
	calls := 0
	tool.WithOriginalImageCatalog(func(_ context.Context, k *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
		calls++
		require.Equal(t, []int{2}, pages)
		sel := types.OriginalMediaReference{KnowledgeID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, SourceSHA256: strings.Repeat("a", 64), Page: 2, Part: "ppt/media/figure.emf", MediaSHA256: strings.Repeat("b", 64)}
		return []types.OriginalDocumentImage{{Reference: sel.Reference(), Page: 2, Part: sel.Part, Label: "Original"}}, nil
	})
	result, e := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":2}`))
	require.NoError(t, e)
	require.True(t, result.Success, result.Error)
	images := result.Data["image_catalog"].([]documentImage)
	require.Len(t, images, 2)
	require.True(t, strings.HasPrefix(images[1].Reference, types.OriginalMediaPrefix))
	require.Equal(t, "doc-1", images[1].ReadArgs.ID)
	require.Equal(t, 2, images[1].ReadArgs.Page)
	require.Empty(t, result.Images)
	require.Equal(t, true, result.Data["original_image_catalog_complete"])
	require.Equal(t, []int{2}, result.Data["original_image_catalog_pages"])
	callsBefore := calls
	_, e = tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","query":"First"}`))
	require.NoError(t, e)
	require.Equal(t, callsBefore, calls, "unbounded text reads do not open the full original")
	tool.WithOriginalImageCatalog(func(_ context.Context, k *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
		require.Equal(t, []int{3}, pages)
		return nil, nil
	})
	request := images[1].ReadArgs
	request.Page = 3
	result, e = tool.Execute(t.Context(), mustReadDocumentJSON(t, request))
	require.NoError(t, e)
	require.False(t, result.Success)
	require.Equal(t, "image_not_in_read_scope", result.Data["reason_code"])
}
func TestOriginalImageCatalogRejectsForeignOrForgedEntries(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].FileType = "pptx"
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\nOriginal page"
	tool.WithOriginalImageCatalog(func(_ context.Context, k *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
		sel := types.OriginalMediaReference{KnowledgeID: k.ID, KnowledgeBaseID: "foreign-kb", SourceSHA256: strings.Repeat("a", 64), Page: 1, Part: "ppt/media/figure.emf", MediaSHA256: strings.Repeat("b", 64)}
		return []types.OriginalDocumentImage{{Reference: sel.Reference(), Page: 1, Part: sel.Part}, {Reference: "original-media://fake", Page: 1, Part: sel.Part}}, nil
	})
	result, e := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, e)
	require.Empty(t, result.Data["image_catalog"])
	require.NotEqual(t, true, result.Data["original_image_catalog_complete"])
}
func TestOriginalImageCatalogReportsBudgetWithoutClaimingNoImages(t *testing.T) {
	tool, repo := newReadDocumentFixture(1)
	tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].FileType = "pptx"
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\nActual page"
	tool.WithOriginalImageCatalog(func(context.Context, *types.Knowledge, []int) ([]types.OriginalDocumentImage, error) {
		return nil, &types.OriginalMediaCatalogLimitError{Total: 44, Limit: 32}
	})
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 44, result.Data["original_image_catalog_total"])
	require.Equal(t, true, result.Data["original_image_catalog_truncated"])
	require.Contains(t, result.Output, "cannot infer")
	require.NotEqual(t, true, result.Data["original_image_catalog_complete"])
}

func TestOriginalImageCatalogCompletenessOnlyCoversVerifiedOwnerPages(t *testing.T) {
	for _, variant := range []string{"partial-body", "empty-page-catalog", "verification-failed"} {
		t.Run(variant, func(t *testing.T) {
			tool, _ := newReadDocumentFixture(1)
			k := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
			k.FileType = "pptx"
			tool.WithOriginalImageCatalog(func(_ context.Context, _ *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
				require.Equal(t, []int{2}, pages, "requested page 3 had no returned authorized owner")
				if variant == "verification-failed" {
					return nil, errors.New("source changed")
				}
				if variant == "empty-page-catalog" {
					return nil, nil
				}
				sel := types.OriginalMediaReference{KnowledgeID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, SourceSHA256: strings.Repeat("a", 64), MediaSHA256: strings.Repeat("b", 64), Page: 2, Part: "ppt/media/actual.emf"}
				return []types.OriginalDocumentImage{{Reference: sel.Reference(), Page: 2, Part: sel.Part}}, nil
			})
			result := &types.ToolResult{Success: true, Data: map[string]interface{}{
				"page_complete": false, "next_offset": 20,
				"chunks": []map[string]interface{}{{"chunk_id": "owned-fragment", "page": 2, "content": "An actual returned fragment"}},
			}}
			catalog := tool.appendOriginalImageCatalog(t.Context(), k, ReadDocumentInput{ID: k.ID, Pages: []int{2, 3}}, result, nil)
			require.Equal(t, false, result.Data["page_complete"])
			require.Equal(t, 20, result.Data["next_offset"], "image completeness never promotes body completeness")
			require.Empty(t, result.Images)
			if variant == "verification-failed" {
				require.NotEqual(t, true, result.Data["original_image_catalog_complete"])
				require.NotContains(t, result.Data, "original_image_catalog_pages")
				require.Empty(t, catalog)
				return
			}
			require.Equal(t, true, result.Data["original_image_catalog_complete"])
			require.Equal(t, []int{2}, result.Data["original_image_catalog_pages"])
			require.Contains(t, result.Output, "body text may still be paginated")
			require.Contains(t, result.Output, "not numeric or visual evidence")
			if variant == "partial-body" {
				require.Len(t, catalog, 1)
				require.Equal(t, 2, catalog[0].ReadArgs.Page)
			} else {
				require.Empty(t, catalog)
			}
		})
	}
}
