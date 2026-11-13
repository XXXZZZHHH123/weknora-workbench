package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func documentFigureFixture(t *testing.T) (*ReadDocumentTool, string, *[]string) {
	t.Helper()
	tool, repo := newReadDocumentFixture(3)
	ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
	repo.ordered[0].Content = "<!-- Slide number: 1 -->\nPrevious page"
	repo.ordered[1].Content = "<!-- Slide number: 2 -->\n![Chart label](" + ref + ")\nSource chart title and axis labels."
	repo.ordered[2].Content = "<!-- Slide number: 3 -->\n![Other](resource://ZyXwVuTsRqPoNmLkJiHgFe)"
	reads := []string{}
	tool.WithImageReader(func(_ context.Context, _ *types.Knowledge, reference string) ([]byte, error) {
		reads = append(reads, reference)
		var imageBytes bytes.Buffer
		err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 3, 3)))
		return imageBytes.Bytes(), err
	})
	return tool, ref, &reads
}

func TestReadDocumentListsStoredOriginalsWithoutImageInfoOrPixelReads(t *testing.T) {
	tool, ref, reads := documentFigureFixture(t)
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":2}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Empty(t, *reads)
	require.Empty(t, result.Images)
	catalog := result.Data["image_catalog"].([]documentImage)
	require.Len(t, catalog, 1)
	require.Equal(t, ref, catalog[0].Reference)
	require.Equal(t, 2, catalog[0].Page)
	require.Equal(t, "chunk-1", catalog[0].ChunkID)
	require.Contains(t, catalog[0].Context, "Source chart title")
	require.Contains(t, result.Output, "not yet viewed")
	require.True(t, catalog[0].PageVerified)
	require.Equal(t, ReadDocumentInput{ID: "chunk-1", Page: 2, Images: []string{ref}, ImageMode: "pixels", Detail: types.ImageDetailFull}, catalog[0].ReadArgs)
	request, err := json.Marshal(catalog[0].ReadArgs)
	require.NoError(t, err)
	read, err := tool.Execute(t.Context(), request)
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	require.Equal(t, []string{ref}, *reads, "catalog navigation keeps the actual owning chunk and verified page")
}

func TestDocumentImageCatalogKeepsUnverifiedPageLabelSeparateFromReadNavigation(t *testing.T) {
	tool, repo := newReadDocumentFixture(3)
	ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
	repo.ordered[1].Content = "![第5页 · 对象 7](" + ref + ")"
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1"}`))
	require.NoError(t, err)
	item := result.Data["image_catalog"].([]documentImage)[0]
	require.Zero(t, item.Page, "a Markdown label does not establish a physical page")
	require.Equal(t, 5, item.PageHint)
	require.False(t, item.PageVerified)
	require.Equal(t, "chunk-1", item.ReadArgs.ID)
	require.Zero(t, item.ReadArgs.Page, "navigation must not promote a label or guess a page")
	require.Equal(t, []string{ref}, item.ReadArgs.Images)
}

func TestReadDocumentImageScopeFailureReturnsOnlyCurrentBoundedNavigation(t *testing.T) {
	tool, ref, reads := documentFigureFixture(t)
	// The selected image belongs to page 2; page 3 has a different real image.
	result, err := tool.Execute(t.Context(), mustReadDocumentJSON(t, ReadDocumentInput{ID: "doc-1", Page: 3, Images: []string{ref}, ImageMode: "geometry"}))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Empty(t, *reads)
	require.Empty(t, result.Images)
	require.Equal(t, "image_not_in_read_scope", result.Data["reason_code"])
	require.Equal(t, false, result.Data["pixels_read"])
	require.Equal(t, false, result.Data["geometry_read"])
	current := result.Data["image_catalog"].([]documentImage)
	require.Len(t, current, 1)
	require.NotEqual(t, ref, current[0].Reference, "a prior image must not be silently searched outside the selected page")
	require.Equal(t, 3, current[0].ReadArgs.Page)
	require.Equal(t, "chunk-2", current[0].ReadArgs.ID)
	require.NotContains(t, result.Data, "chunks", "failed image selection does not issue successful body evidence")
	require.Contains(t, result.Output, "image_not_in_read_scope")

	rows := []map[string]interface{}{}
	for i := 0; i < 23; i++ {
		rows = append(rows, map[string]interface{}{"chunk_id": fmt.Sprintf("source-%d", i), "content": fmt.Sprintf("![Source](resource://%022d)", i)})
	}
	large := &types.ToolResult{Success: true, Data: map[string]interface{}{"chunks": rows}}
	result, err = tool.finishImageRead(t.Context(), &types.Knowledge{ID: "other-document"}, ReadDocumentInput{ID: "other-document", Images: []string{ref}}, large, nil)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Empty(t, *reads)
	require.Len(t, result.Data["image_catalog"], 16)
	require.Equal(t, 23, result.Data["image_catalog_total"])
	require.Equal(t, true, result.Data["image_catalog_truncated"])
	for _, item := range result.Data["image_catalog"].([]documentImage) {
		require.Equal(t, "other-document", item.KnowledgeID)
		require.NotEqual(t, ref, item.Reference)
	}
}

func mustReadDocumentJSON(t *testing.T, input ReadDocumentInput) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	return encoded
}

func TestDocumentImageCatalogUsesPDFPhysicalPageForWholeDocument(t *testing.T) {
	ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
	result := &types.ToolResult{Data: map[string]interface{}{"chunks": []map[string]interface{}{{
		"chunk_id": "source-chunk", "content": "![原页](" + ref + ")",
		"image_sources": []types.DocumentImageSource{{Reference: ref, Page: 13, SourceHash: "file-hash"}},
	}}}}
	catalog := documentImageCatalog(result, "doc-1", "file-hash")
	require.Len(t, catalog, 1)
	require.Equal(t, 13, catalog[0].Page)
	require.Empty(t, catalog[0].SourcePart)
	stale := documentImageCatalog(result, "doc-1", "replacement-hash")
	require.Zero(t, stale[0].Page)
}

func TestReadDocumentSelectedPixelsKeepActualSourceAndVersion(t *testing.T) {
	tool, ref, reads := documentFigureFixture(t)
	input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 2, Images: []string{ref}, Focus: "Describe the requested trace using the visible axes"})
	result, err := tool.Execute(t.Context(), input)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []string{ref}, *reads)
	require.Len(t, result.Images, 1)
	require.True(t, strings.HasPrefix(result.Images[0], "data:image/png;base64,"))
	evidence := result.Data["image_evidence"].([]map[string]interface{})
	require.Equal(t, "doc-1", evidence[0]["knowledge_id"])
	require.Equal(t, "chunk-1", evidence[0]["chunk_id"])
	require.Equal(t, true, evidence[0]["read_success"])
	require.Len(t, evidence[0]["sha256"].(string), 64)
	require.Equal(t, documentImageEvidenceVersion, evidence[0]["version"])
	require.Equal(t, types.ImageDetailFull, result.ImageDetail)
	stored := SanitizeAgentStepsForStorage([]types.AgentStep{{ToolCalls: []types.ToolCall{{Name: ToolReadDocument, Result: result}}}})
	require.Empty(t, stored[0].ToolCalls[0].Result.Images)
	require.NotEmpty(t, stored[0].ToolCalls[0].Result.Data["image_evidence"])
}

func TestReadDocumentRejectsOtherPageAndInventedImageBeforeReadingBytes(t *testing.T) {
	for _, ref := range []string{"resource://ZyXwVuTsRqPoNmLkJiHgFe", "https://example.invalid/other.png", "resource://invented"} {
		t.Run(ref, func(t *testing.T) {
			tool, _, reads := documentFigureFixture(t)
			input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 2, Images: []string{ref}})
			result, err := tool.Execute(t.Context(), input)
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Empty(t, *reads)
			require.Empty(t, result.Images)
		})
	}
}

func TestReadDocumentFailedOriginalReadNeverCreatesVisualEvidence(t *testing.T) {
	tool, ref, _ := documentFigureFixture(t)
	tool.WithImageReader(func(context.Context, *types.Knowledge, string) ([]byte, error) {
		return nil, errors.New("private storage failure")
	})
	input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 2, Images: []string{ref}})
	result, err := tool.Execute(t.Context(), input)
	require.NoError(t, err)
	require.True(t, result.Success, "Document text remains readable")
	require.Empty(t, result.Images)
	require.Equal(t, false, result.Data["image_evidence"].([]map[string]interface{})[0]["read_success"])
	require.NotContains(t, result.Output, "private storage failure")
}

func TestReadDocumentGeometryUsesOnlyUnambiguousParserSourceLocator(t *testing.T) {
	for _, variant := range []string{"valid", "other-page", "ambiguous", "invalid-selection"} {
		t.Run(variant, func(t *testing.T) {
			tool, repo := newReadDocumentFixture(1)
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			content := "<!-- Slide number: 1 -->\n![Plot](" + ref + ")\n来源：第1页 · ppt/media/synthetic.emf（原文件中的图像或缓存预览）。"
			if variant == "other-page" {
				content = strings.Replace(content, "来源：第1页", "来源：第2页", 1)
			}
			if variant == "ambiguous" {
				content += "\n来源：第1页 · ppt/media/other.emf（原文件中的图像或缓存预览）。"
			}
			repo.ordered[0].Content = content
			tool.WithImageReader(func(context.Context, *types.Knowledge, string) ([]byte, error) {
				var data bytes.Buffer
				err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 3)))
				return data.Bytes(), err
			})
			calls := 0
			tool.WithFigureReader(func(_ context.Context, k *types.Knowledge, reference string, page int, part string, series []string) (map[string]any, error) {
				calls++
				require.Equal(t, ref, reference)
				require.Equal(t, 1, page)
				require.Equal(t, "ppt/media/synthetic.emf", part)
				if variant == "invalid-selection" {
					return map[string]any{"version": "source_figure_selection_options_v1", "available_series": []string{"Alpha", "Beta"}}, nil
				}
				return map[string]any{"markdown": "Independent drawn points", "version": "source_figure_geometry_v1"}, nil
			})
			selection := []string(nil)
			if variant == "invalid-selection" {
				selection = []string{"Plot"}
			}
			input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 1, Images: []string{ref}, ImageSeries: selection})
			result, err := tool.Execute(t.Context(), input)
			require.NoError(t, err)
			if variant == "invalid-selection" {
				require.True(t, result.Success)
				require.Len(t, result.Images, 1)
				require.Contains(t, result.Output, `["Alpha","Beta"]`)
				require.Empty(t, result.Data["image_geometry_evidence"])
				require.Equal(t, "selection_required", result.Data["image_evidence"].([]map[string]interface{})[0]["geometry_status"])
				return
			}
			require.True(t, result.Success)
			if variant == "valid" {
				require.Equal(t, 1, calls)
				require.Contains(t, result.Output, "Independent drawn points")
				require.Len(t, result.Data["image_geometry_evidence"], 1)
			} else {
				require.Zero(t, calls)
				require.Empty(t, result.Data["image_geometry_evidence"])
			}
		})
	}
}

func TestReadDocumentExplicitGeometryDoesNotRequireOrAttemptPixels(t *testing.T) {
	for _, variant := range []string{"pixel-timeout", "no-pixel-reader", "denied", "no-figure-reader", "no-part", "invalid-selection", "unsupported"} {
		t.Run(variant, func(t *testing.T) {
			tool, repo := newReadDocumentFixture(1)
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			repo.ordered[0].Content = "<!-- Slide number: 1 -->\n![Plot](" + ref + ")\n来源：第1页 · ppt/media/synthetic.emf（原图）。"
			if variant == "no-part" {
				repo.ordered[0].Content = "<!-- Slide number: 1 -->\n![Plot](" + ref + ")"
			}
			pixelReads, figureReads := 0, 0
			tool.WithSourceImageReader(func(context.Context, *types.Knowledge, string, int, string) ([]byte, error) {
				pixelReads++
				return nil, context.DeadlineExceeded
			})
			if variant == "no-pixel-reader" {
				tool.sourceImageReader = nil
			}
			tool.WithFigureReader(func(_ context.Context, knowledge *types.Knowledge, reference string, page int, part string, series []string) (map[string]any, error) {
				figureReads++
				require.Equal(t, "doc-1", knowledge.ID)
				require.Equal(t, ref, reference)
				require.Equal(t, 1, page)
				require.Equal(t, "ppt/media/synthetic.emf", part)
				require.Equal(t, []string{"Alpha"}, series)
				if variant == "denied" {
					return nil, errors.New("private authorization failure")
				}
				if variant == "invalid-selection" {
					return map[string]any{"version": "source_figure_selection_options_v1", "available_series": []string{"Beta"}}, nil
				}
				if variant == "unsupported" {
					return nil, nil
				}
				return map[string]any{"version": "source_figure_geometry_v1", "markdown": "Verified original trace; approximate value with original units."}, nil
			})
			if variant == "no-figure-reader" {
				tool.figureReader = nil
			}
			input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 1, Images: []string{ref}, ImageMode: "geometry", ImageSeries: []string{"Alpha"}})
			result, err := tool.Execute(t.Context(), input)
			require.NoError(t, err)
			require.Zero(t, pixelReads, "explicit geometry must not spend a PNG conversion timeout or fall back to pixels")
			require.Empty(t, result.Images)
			require.Equal(t, "geometry", result.Data["image_mode"])
			reads := result.Data["image_evidence"].([]map[string]interface{})
			require.Equal(t, false, reads[0]["read_success"])
			require.Equal(t, "not_requested", reads[0]["pixel_status"])
			require.NotContains(t, reads[0], "returned_image_index")
			if variant == "pixel-timeout" || variant == "no-pixel-reader" {
				require.True(t, result.Success, result.Error)
				require.Equal(t, true, reads[0]["geometry_read_success"])
				require.Len(t, result.Data["image_geometry_evidence"], 1)
				require.Contains(t, result.Output, "no visual inspection")
			} else {
				require.False(t, result.Success)
				require.Equal(t, false, reads[0]["geometry_read_success"])
				require.Empty(t, result.Data["image_geometry_evidence"])
				require.NotContains(t, result.Output, "private authorization failure")
			}
			if variant == "no-part" || variant == "no-figure-reader" {
				require.Zero(t, figureReads)
			} else {
				require.Equal(t, 1, figureReads)
			}
		})
	}
}

func TestReadDocumentGeometryModeRejectsUnselectedOrForeignImages(t *testing.T) {
	for _, input := range []string{
		`{"id":"doc-1","page":2,"image_mode":"geometry"}`,
		`{"id":"doc-1","page":2,"image_mode":"unknown","images":["resource://AbCdEfGhIjKlMnOpQrStUv"]}`,
		`{"id":"doc-1","page":2,"image_mode":"geometry","images":["resource://ZyXwVuTsRqPoNmLkJiHgFe"]}`,
		`{"id":"doc-1","page":2,"image_mode":"geometry","images":["https://example.invalid/private.emf"]}`,
	} {
		t.Run(input, func(t *testing.T) {
			tool, _, reads := documentFigureFixture(t)
			tool.WithFigureReader(func(context.Context, *types.Knowledge, string, int, string, []string) (map[string]any, error) {
				t.Fatal("invalid image selection reached source geometry extraction")
				return nil, nil
			})
			result, err := tool.Execute(t.Context(), json.RawMessage(input))
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Empty(t, *reads)
			require.Empty(t, result.Images)
		})
	}
}
