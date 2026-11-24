package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadDocumentCatalogGeometryArgsRoundTripWithActualLegends(t *testing.T) {
	for _, source := range []string{"stored", "original"} {
		t.Run(source, func(t *testing.T) {
			tool, repo := newReadDocumentFixture(2)
			k := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
			k.FileType = "pptx"
			const part = "ppt/media/short-trace.emf"
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			repo.ordered[0].Content = "<!-- Slide number: 1 -->\n![Descriptive heading, unrelated to legends](" + ref + ")\n来源：第1页 · " + part + "（原图）"
			repo.ordered[1].Content = "<!-- Slide number: 2 -->\nAnother physical page"
			if source == "original" {
				ref = types.OriginalMediaReference{
					KnowledgeID: k.ID, KnowledgeBaseID: k.KnowledgeBaseID, SourceSHA256: strings.Repeat("a", 64),
					Page: 1, Part: part, MediaSHA256: strings.Repeat("b", 64),
				}.Reference()
				repo.ordered[0].Content = "<!-- Slide number: 1 -->\nLegacy page without saved preview"
				tool.WithOriginalImageCatalog(func(_ context.Context, _ *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
					require.Equal(t, []int{1}, pages)
					return []types.OriginalDocumentImage{{Reference: ref, Page: 1, Part: part, Label: "Descriptive heading, unrelated to legends"}}, nil
				})
			}
			tool.WithSourceImageReader(func(context.Context, *types.Knowledge, string, int, string) ([]byte, error) {
				t.Error("catalog discovery and geometry mode must leave pixel reads to an explicit pixel request")
				return nil, errors.New("unexpected pixel read")
			})
			var metadataCalls, geometryCalls atomic.Int32
			tool.WithFigureReader(func(_ context.Context, authorized *types.Knowledge, imageRef string, page int, sourcePart string, series []string) (map[string]any, error) {
				assert.Equal(t, k.ID, authorized.ID)
				assert.Equal(t, ref, imageRef)
				assert.Equal(t, 1, page)
				assert.Equal(t, part, sourcePart)
				if len(series) == 0 {
					metadataCalls.Add(1)
					return map[string]any{"version": "source_figure_selection_options_v1", "available_series": []string{"Short Run", "Control"},
						"markdown": "METADATA_MUST_NOT_REPORT_MEASUREMENTS"}, nil
				}
				geometryCalls.Add(1)
				assert.Equal(t, []string{"Short Run"}, series)
				return map[string]any{"version": "source_figure_geometry_v1", "markdown": "Short Run falls to about 12 and rebounds to about 19 at its last drawn point before the middle tick."}, nil
			})

			listed, err := tool.Execute(t.Context(), mustReadDocumentJSON(t, ReadDocumentInput{ID: k.ID, Page: 1}))
			require.NoError(t, err)
			require.True(t, listed.Success, listed.Error)
			require.Empty(t, listed.Images)
			require.EqualValues(t, 1, metadataCalls.Load())
			require.Zero(t, geometryCalls.Load())
			require.Empty(t, listed.Data["image_geometry_evidence"])
			item := listed.Data["image_catalog"].([]documentImage)[0]
			require.Equal(t, "pixels", item.ReadArgs.ImageMode)
			require.NotNil(t, item.GeometryReadArgs)
			require.Equal(t, item.ReadArgs.ID, item.GeometryReadArgs.ID)
			require.Equal(t, item.ReadArgs.Page, item.GeometryReadArgs.Page)
			require.Equal(t, item.ReadArgs.Images, item.GeometryReadArgs.Images)
			require.Equal(t, []string{"Short Run", "Control"}, item.GeometryReadArgs.ImageSeries)

			registry := modelcontext.NewRegistry(true)
			modelOutput := registry.ModelToolResultForTool(ToolReadDocument, listed)
			require.NotContains(t, modelOutput, "METADATA_MUST_NOT_REPORT_MEASUREMENTS")
			require.NotContains(t, modelOutput, ref)
			_, encoded, found := strings.Cut(modelOutput, `<document_image_evidence untrusted_source_content="true">`)
			require.True(t, found)
			encoded, _, found = strings.Cut(encoded, "</document_image_evidence>")
			require.True(t, found)
			var projected struct {
				Catalog []documentImage `json:"image_catalog"`
			}
			require.NoError(t, json.Unmarshal([]byte(encoded), &projected))
			require.Len(t, projected.Catalog, 1)
			args := projected.Catalog[0].GeometryReadArgs
			require.NotNil(t, args)
			require.Equal(t, []string{"Short Run", "Control"}, args.ImageSeries)
			// The model can choose just one original legend and preserve the
			// selector exactly as the catalog supplied it.
			args.ImageSeries = []string{args.ImageSeries[0]}
			args.Focus = "Describe the Short Run trace through its last drawn point."
			calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: ToolReadDocument, Arguments: string(mustReadDocumentJSON(t, *args))}}}
			registry.DecodeToolCalls(calls)
			require.Empty(t, calls[0].UnresolvedHandles)
			read, err := tool.Execute(t.Context(), json.RawMessage(calls[0].Function.Arguments))
			require.NoError(t, err)
			require.True(t, read.Success, read.Error)
			require.Empty(t, read.Images)
			require.EqualValues(t, 1, metadataCalls.Load(), "an explicit geometry read should not redo catalog probes")
			require.EqualValues(t, 1, geometryCalls.Load())
			require.Len(t, read.Data["image_geometry_evidence"], 1)
			require.Contains(t, read.Output, "rebounds to about 19 at its last drawn point before the middle tick")
		})
	}
}
