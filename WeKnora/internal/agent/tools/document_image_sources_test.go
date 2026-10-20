package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReadDocumentStructuredImageSourcesUseExactImageAcrossSplitLocatorLines(t *testing.T) {
	for _, variant := range []string{"valid", "stale", "other-page", "ambiguous", "other-image", "traversal", "repeated-across-pages", "deferred"} {
		t.Run(variant, func(t *testing.T) {
			tool, repo := newReadDocumentFixture(3)
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].FileHash = "hash-a"
			repo.ordered[0].Content = "<!-- Slide number: 1 -->\n![Logo](resource://ZyXwVuTsRqPoNmLkJiHgFe)\n![A](" + ref + ")"
			repo.ordered[1].Content = "来源：第1页 · ppt/media/a.emf（原图）。"
			repo.ordered[2].Content = "<!-- Slide number: 2 -->\nother page"
			source := types.DocumentImageSource{Reference: ref, Page: 1, SourcePart: "ppt/media/a.emf", SourceHash: "hash-a"}
			switch variant {
			case "stale":
				source.SourceHash = "old-hash"
			case "other-page":
				source.Page = 2
			case "other-image":
				source.Reference = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
			case "traversal":
				source.SourcePart = "ppt/media/../secret.emf"
			}
			sources := []types.DocumentImageSource{source}
			if variant == "repeated-across-pages" {
				other := source
				other.Page = 2
				sources = append(sources, other)
			}
			if variant == "ambiguous" {
				other := source
				other.SourcePart = "ppt/media/b.emf"
				sources = append(sources, other)
			}
			metadata, _ := json.Marshal(map[string]any{types.DocumentImageSourcesKey: sources})
			repo.ordered[0].Metadata = types.JSON(metadata)
			// A successful storage read does not bypass a missing/invalid binding.
			original, _, _ := documentFigureFixture(t)
			tool.imageReader = original.imageReader
			if variant == "deferred" {
				tool.WithSourceImageReader(func(ctx context.Context, k *types.Knowledge, reference string, page int, part string) ([]byte, error) {
					require.Equal(t, ref, reference)
					require.Equal(t, 1, page)
					require.Equal(t, "ppt/media/a.emf", part)
					return original.imageReader(ctx, k, reference)
				})
				tool.imageReader = func(context.Context, *types.Knowledge, string) ([]byte, error) {
					t.Fatal("deferred read bypassed source renderer")
					return nil, nil
				}
			}
			calls := 0
			tool.WithFigureReader(func(_ context.Context, _ *types.Knowledge, reference string, page int, part string, _ []string) (map[string]any, error) {
				calls++
				require.Equal(t, ref, reference)
				require.Equal(t, 1, page)
				require.Equal(t, "ppt/media/a.emf", part)
				return map[string]any{"version": "source_figure_geometry_v1", "markdown": "Fresh geometry"}, nil
			})
			input, _ := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 1, Images: []string{ref}, ImageSeries: []string{"Alpha"}})
			result, err := tool.Execute(t.Context(), input)
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			if variant == "valid" || variant == "repeated-across-pages" || variant == "deferred" {
				require.Equal(t, 1, calls)
				require.Len(t, result.Data["image_geometry_evidence"], 1)
			} else {
				require.Zero(t, calls)
				require.Empty(t, result.Data["image_geometry_evidence"])
			}
		})
	}
}
