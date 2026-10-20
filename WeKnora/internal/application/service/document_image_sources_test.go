package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDocumentImageSourcesSurviveSplitAndKeepEachSlideRelationship(t *testing.T) {
	ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
	other := "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	raw := `[{"original_ref":"images/a.png","page":2,"source_part":"ppt/media/a.emf"},{"original_ref":"images/b.png","page":2,"source_part":"ppt/media/b.emf"},{"original_ref":"images/a.png","page":3,"source_part":"ppt/media/a.emf"},{"original_ref":"missing","page":2,"source_part":"ppt/media/c.emf"},{"original_ref":"images/a.png","page":2,"source_part":"ppt/media/../private.emf"}]`
	result := &types.ReadResult{Metadata: map[string]string{"pptx_image_sources_v1": raw}}
	sources := resolvedDocumentImageSources(result, []docparser.StoredImage{{OriginalRef: "images/a.png", ServingURL: ref}, {OriginalRef: "images/b.png", ServingURL: other}}, "hash-a")
	require.Len(t, sources, 3)
	// Both figures survive without a text source locator: figures are not
	// native paragraphs and must not depend on text-to-slide alignment.
	metadata := documentImageSourceMetadata("![A]("+ref+")\n![B]("+other+")", sources)
	got := types.ChunkDocumentImageSources(metadata)
	require.Len(t, got, 3)
	require.Equal(t, "ppt/media/a.emf", got[0].SourcePart)
	require.Equal(t, "ppt/media/b.emf", got[1].SourcePart)
	require.Equal(t, 3, got[2].Page)
	require.Empty(t, documentImageSourceMetadata("unrelated", sources))
	require.Empty(t, resolvedDocumentImageSources(&types.ReadResult{Metadata: map[string]string{"pptx_image_sources_v1": "invalid"}}, nil, "hash-a"))
}

func TestCloneDocumentImageSourcesOnlyKeepsNewOwnedResources(t *testing.T) {
	oldRef := "resource://AbCdEfGhIjKlMnOpQrStUv"
	newRef := "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	metadata, _ := json.Marshal(map[string]any{"other": "preserved", types.DocumentImageSourcesKey: []types.DocumentImageSource{{Reference: oldRef, Page: 2, SourcePart: "ppt/media/a.emf", SourceHash: "hash-a"}}})
	got := cloneDocumentImageSourceMetadata(types.JSON(metadata), map[string]string{oldRef: newRef})
	require.Equal(t, newRef, types.ChunkDocumentImageSources(got)[0].Reference)
	require.Contains(t, string(got), `"other":"preserved"`)
	require.Empty(t, types.ChunkDocumentImageSources(cloneDocumentImageSourceMetadata(types.JSON(metadata), nil)))
}

func TestRasterMultimodalImagesExcludesOnlyDeferredSourceVectors(t *testing.T) {
	images := []docparser.StoredImage{{OriginalRef: "images/a.emf"}, {OriginalRef: "images/b.WMF"}, {OriginalRef: "images/c.png"}, {OriginalRef: "images/d.jpg"}}
	got := rasterMultimodalImages(images)
	require.Len(t, got, 2)
	require.Equal(t, "images/c.png", got[0].OriginalRef)
	require.Len(t, images, 4)
	require.Empty(t, rasterMultimodalImages(images[:2]))
}
