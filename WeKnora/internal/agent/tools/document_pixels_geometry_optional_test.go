package tools

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReadDocumentPixelsKeepOptionalGeometrySelectionSeparate(t *testing.T) {
	for _, source := range []string{"stored", "original"} {
		for _, mode := range []string{"pixels-missing-series", "pixels-invalid-series", "geometry-missing-series", "wrong-page", "storage-denied", "invalid-bytes"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				tool, repo := newReadDocumentFixture(2)
				ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
				part := "ppt/media/synthetic.emf"
				repo.ordered[0].Content = "<!-- Slide number: 1 -->\n![Observed figure](" + ref + ")\n来源：第1页 · " + part + "（原图）。"
				repo.ordered[1].Content = "<!-- Slide number: 2 -->\nAnother physical page"
				if source == "original" {
					k := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
					k.FileType = "pptx"
					ref = (types.OriginalMediaReference{KnowledgeID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, SourceSHA256: strings.Repeat("a", 64), Page: 1, Part: part, MediaSHA256: strings.Repeat("b", 64)}).Reference()
					repo.ordered[0].Content = "<!-- Slide number: 1 -->\nLegacy page with original figure"
					tool.WithOriginalImageCatalog(func(_ context.Context, _ *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
						if len(pages) != 1 || pages[0] != 1 {
							return nil, nil
						}
						return []types.OriginalDocumentImage{{Reference: ref, Page: 1, Part: part, Label: "Observed figure"}}, nil
					})
				}
				pixelReads, geometryReads := 0, 0
				tool.WithSourceImageReader(func(_ context.Context, k *types.Knowledge, actual string, page int, sourcePart string) ([]byte, error) {
					pixelReads++
					require.Equal(t, "doc-1", k.ID)
					require.Equal(t, ref, actual)
					require.Equal(t, 1, page)
					require.Equal(t, part, sourcePart)
					if mode == "storage-denied" {
						return nil, errors.New("private storage/ownership rejection")
					}
					if mode == "invalid-bytes" {
						return []byte("not an image"), nil
					}
					var b bytes.Buffer
					require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3))))
					return b.Bytes(), nil
				})
				tool.WithFigureReader(func(_ context.Context, _ *types.Knowledge, actual string, page int, sourcePart string, series []string) (map[string]any, error) {
					geometryReads++
					require.Equal(t, ref, actual)
					require.Equal(t, 1, page)
					require.Equal(t, part, sourcePart)
					if mode == "pixels-invalid-series" {
						require.Equal(t, []string{"not-a-legend"}, series)
					} else {
						require.Empty(t, series, "the program must never auto-select a legend")
					}
					return map[string]any{"version": "source_figure_selection_options_v1", "available_series": []string{"Alpha", "Beta"}, "geometry": map[string]any{"unexpected_numeric_payload": 98765}, "markdown": "unexpected numeric evidence 98765"}, nil
				})
				input := ReadDocumentInput{ID: "doc-1", Page: 1, Images: []string{ref}, ImageMode: "pixels", Focus: "Describe the observed figure", Detail: types.ImageDetailFull}
				if mode == "pixels-invalid-series" {
					input.ImageSeries = []string{"not-a-legend"}
				}
				if mode == "geometry-missing-series" {
					input.ImageMode = "geometry"
				}
				if mode == "wrong-page" {
					input.Page = 2
				}
				result, err := tool.Execute(t.Context(), mustReadDocumentJSON(t, input))
				require.NoError(t, err)
				if mode == "wrong-page" {
					require.False(t, result.Success)
					require.Empty(t, result.Images)
					require.Zero(t, pixelReads)
					require.Zero(t, geometryReads)
					return
				}
				if mode == "geometry-missing-series" {
					require.False(t, result.Success)
					require.Empty(t, result.Images)
					require.Zero(t, pixelReads)
					require.Equal(t, 1, geometryReads)
					return
				}
				require.True(t, result.Success, "the authorized text read stays available")
				require.Equal(t, 1, pixelReads)
				evidence := result.Data["image_evidence"].([]map[string]interface{})
				require.Len(t, evidence, 1)
				require.Empty(t, result.Data["image_geometry_evidence"])
				if mode == "storage-denied" || mode == "invalid-bytes" {
					require.Empty(t, result.Images)
					require.Equal(t, false, evidence[0]["read_success"])
					require.Zero(t, geometryReads)
					require.NotContains(t, result.Output, "private storage/ownership rejection")
					return
				}
				require.Equal(t, 1, geometryReads)
				require.Len(t, result.Images, 1)
				require.True(t, strings.HasPrefix(result.Images[0], "data:image/png;base64,"))
				require.Equal(t, input.Focus, result.ImageFocus)
				require.Equal(t, types.ImageDetailFull, result.ImageDetail)
				require.Equal(t, true, evidence[0]["read_success"])
				require.Equal(t, false, evidence[0]["geometry_read_success"])
				require.Equal(t, "selection_required", evidence[0]["geometry_status"])
				options := evidence[0]["geometry_selection_options"].(map[string]any)
				require.Equal(t, []string{"Alpha", "Beta"}, options["available_series"])
				require.Contains(t, result.Output, "Original pixels were returned")
				require.NotContains(t, result.Output, "98765")
				require.NotContains(t, result.Output, "<original_figure_geometry")
			})
		}
	}
}
