package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReadDocumentBindsImageOCRAndCaptionToStoredAndOriginalPixels(t *testing.T) {
	const table = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const neighbor = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	for _, ocr := range []string{
		"Batch\tOpening\tAdded\tClosing\nAmber\t23.6\t68.1\t91.7\nBlue\t91.7\t18.2\t109.9",
		"Batch\tClosing\tAdded\tOpening\nAmber\t91.7\t68.1\t23.6\nBlue\t109.9\t18.2\t91.7",
	} {
		t.Run(strings.SplitN(ocr, "\n", 2)[0], func(t *testing.T) {
			marshal := func(value any) json.RawMessage {
				encoded, err := json.Marshal(value)
				require.NoError(t, err)
				return encoded
			}
			tool, repo := newReadDocumentFixture(2)
			doc := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
			doc.FileType, doc.FileHash = "pptx", "fixture-source-revision"
			repo.ordered[0].Content = "<!-- Slide number: 3 -->\nWorkshop summary"
			parent := repo.ordered[1]
			parent.Content = "![Table](" + table + ")\n![Neighbor](" + neighbor + ")"
			parent.ImageInfo = string(marshal([]types.ImageInfo{
				{URL: table, Caption: "Parser placeholder"}, {URL: neighbor, Caption: "Parser placeholder"},
			}))
			part := "ppt/media/table.png"
			parent.Metadata = types.JSON(marshal(map[string]any{types.DocumentImageSourcesKey: []types.DocumentImageSource{
				{Reference: table, Page: 3, SourcePart: part, SourceHash: doc.FileHash},
				{Reference: neighbor, Page: 3, SourcePart: "ppt/media/neighbor.png", SourceHash: doc.FileHash},
			}}))
			figure, _, _ := documentFigureFixture(t)
			pixels, err := figure.imageReader(t.Context(), doc, table)
			require.NoError(t, err)
			sha := sha256.Sum256(pixels)
			storedDigest := hex.EncodeToString(sha[:])
			child := func(id, ref string, kind types.ChunkType, body string) *types.Chunk {
				return &types.Chunk{
					ID: id, TenantID: doc.TenantID, KnowledgeID: doc.ID, KnowledgeBaseID: doc.KnowledgeBaseID,
					ParentChunkID: parent.ID, ChunkType: kind, Content: body, IsEnabled: true,
					ImageInfo: string(marshal([]types.ImageInfo{{URL: ref, OriginalURL: ref, SHA256: storedDigest}})),
				}
			}
			foreign := child("foreign", table, types.ChunkTypeImageOCR, "Foreign OCR")
			foreign.KnowledgeID = "foreign-doc"
			wrongParent := child("wrong-parent", table, types.ChunkTypeImageOCR, "Other parent OCR")
			wrongParent.ParentChunkID = repo.ordered[0].ID
			disabled := child("disabled", table, types.ChunkTypeImageOCR, "Disabled OCR")
			disabled.IsEnabled = false
			stale := child("obsolete", table, types.ChunkTypeImageOCR, "Obsolete preview OCR")
			stale.ImageInfo = string(marshal([]types.ImageInfo{{URL: table, SHA256: strings.Repeat("f", 64)}}))
			repo.children = []*types.Chunk{
				child("transcription", table, types.ChunkTypeImageOCR, ocr),
				child("description", table, types.ChunkTypeImageCaption, "Amounts by workshop batch."),
				child("neighbor-transcription", neighbor, types.ChunkTypeImageOCR, "Different table\t900.0"),
				foreign, wrongParent, disabled,
			}

			// An ingested preview and the original ZIP media part may have
			// different bytes. Bind the original to the exact parser relation.
			original := types.OriginalMediaReference{KnowledgeID: doc.ID, KnowledgeBaseID: doc.KnowledgeBaseID,
				SourceSHA256: strings.Repeat("a", 64), MediaSHA256: strings.Repeat("b", 64), Page: 3, Part: part}
			neighborOriginal := original
			neighborOriginal.Part = "ppt/media/neighbor.png"
			tool.WithOriginalImageCatalog(func(_ context.Context, _ *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
				require.Equal(t, []int{3}, pages)
				return []types.OriginalDocumentImage{
					{Reference: original.Reference(), Page: 3, Part: part},
					{Reference: neighborOriginal.Reference(), Page: 3, Part: neighborOriginal.Part},
				}, nil
			})
			tool.WithImageReader(figure.imageReader)
			tool.WithSourceImageReader(func(_ context.Context, _ *types.Knowledge, ref string, page int, sourcePart string) ([]byte, error) {
				require.Equal(t, original.Reference(), ref)
				require.Equal(t, 3, page)
				require.Equal(t, part, sourcePart)
				return pixels, nil
			})
			page, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":3}`))
			require.NoError(t, err)
			require.True(t, page.Success, page.Error)
			require.Empty(t, page.Images)
			pageTexts := page.Data[types.DocumentImageTextEvidenceKey].([]documentImageTextEvidence)
			require.Len(t, pageTexts, 3)
			model := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, page)
			require.Contains(t, model, ocr, "model must receive the original row breaks and tabs")
			require.Contains(t, model, "Amounts by workshop batch.")

			for _, image := range page.Data["image_catalog"].([]documentImage) {
				if image.Reference != table && image.Reference != original.Reference() {
					continue
				}
				if image.Reference == table {
					repo.children = append(repo.children, stale)
				}
				read, err := tool.Execute(t.Context(), mustReadDocumentJSON(t, image.ReadArgs))
				require.NoError(t, err)
				require.True(t, read.Success, read.Error)
				require.Len(t, read.Images, 1)
				texts := read.Data[types.DocumentImageTextEvidenceKey].([]documentImageTextEvidence)
				require.Len(t, texts, 2)
				require.Equal(t, ocr, texts[0].Text)
				require.Equal(t, "transcription", texts[0].ChildChunkID)
				require.Equal(t, types.ChunkTypeImageOCR, texts[0].Kind)
				require.Equal(t, "Amounts by workshop batch.", texts[1].Text)
				require.Equal(t, types.ChunkTypeImageCaption, texts[1].Kind)
				for _, item := range texts {
					require.Equal(t, image.Reference, item.Reference)
					require.Equal(t, image.ChunkID, item.ChunkID)
					require.Equal(t, parent.ID, item.ParentChunkID)
					require.Equal(t, table, item.StoredRef)
					require.Equal(t, storedDigest, item.StoredSHA256)
				}
				registry := modelcontext.NewRegistry(true)
				model := registry.ModelToolResultForTool(ToolReadDocument, read)
				boundOCR := fmt.Sprintf("<ingested_image_text reference=%q chunk_id=%q "+
					"page=\"3\" kind=\"image_ocr\" truncated=\"false\">\n%s\n</ingested_image_text>",
					registry.CompactKnownText(image.Reference), registry.CompactKnownText(image.ChunkID), ocr)
				require.Contains(t, model, boundOCR)
				require.Contains(t, model, "VLM observations are visual readings and may be estimates")
				require.Contains(t, model, "same image and cell")
				require.Contains(t, model, "neighboring row or column")
				require.Contains(t, model, "evidence already supplied")
				require.NotContains(t, model, "900.0")
				require.NotContains(t, model, "Other parent OCR")
				require.NotContains(t, SanitizeToolDataForClient(ToolReadDocument, read.Data), types.DocumentImageTextEvidenceKey)
				if image.Reference == table {
					repo.children = repo.children[:len(repo.children)-1]
				}
			}
		})
	}
}

func TestReadDocumentPageBudgetsOCRBeforeOriginalImageAliases(t *testing.T) {
	const table = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const second = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	const page = 4
	tool, repo := newReadDocumentFixture(1)
	doc := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
	doc.FileType, doc.FileHash = "pptx", "fixture-source-revision"
	parent := repo.ordered[0]
	parent.Content = "# Page 4\n![First table](" + table + ")\n![Second table](" + second + ")"
	original := types.OriginalMediaReference{
		KnowledgeID: doc.ID, KnowledgeBaseID: doc.KnowledgeBaseID,
		SourceSHA256: strings.Repeat("a", 64), MediaSHA256: strings.Repeat("b", 64),
		Page: page, Part: "ppt/media/table.png",
	}
	metadata, err := json.Marshal(map[string]any{types.DocumentImageSourcesKey: []types.DocumentImageSource{
		{Reference: table, Page: page, SourcePart: original.Part, SourceHash: doc.FileHash},
	}})
	require.NoError(t, err)
	parent.Metadata = types.JSON(metadata)
	tool.WithOriginalImageCatalog(func(_ context.Context, _ *types.Knowledge, pages []int) ([]types.OriginalDocumentImage, error) {
		require.Equal(t, []int{page}, pages)
		return []types.OriginalDocumentImage{{Reference: original.Reference(), Page: page, Part: original.Part}}, nil
	})
	firstOCR := "Batch\tOpening\tClosing\n" + strings.Repeat("Workshop\t71.2\t64.7\n", 28) + "Museum\t88.9\t67.4"
	secondOCR := "Room\tOpening\tClosing\nGallery\t83.1\t52.7"
	for _, item := range []struct{ id, ref, body string }{
		{"transcription-a", table, firstOCR},
		{"transcription-b", second, secondOCR},
	} {
		info, err := json.Marshal([]types.ImageInfo{{URL: item.ref, OriginalURL: item.ref}})
		require.NoError(t, err)
		repo.children = append(repo.children, &types.Chunk{
			ID: item.id, TenantID: doc.TenantID, KnowledgeID: doc.ID, KnowledgeBaseID: doc.KnowledgeBaseID,
			ParentChunkID: parent.ID, ChunkType: types.ChunkTypeImageOCR, Content: item.body, IsEnabled: true,
			ImageInfo: string(info),
		})
	}

	registry := NewToolRegistry()
	registry.RegisterTool(tool)
	registry.SetMaxToolOutputSize(3000)
	result, err := registry.ExecuteTool(t.Context(), ToolReadDocument, json.RawMessage(`{"id":"doc-1","page":4}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Empty(t, result.Images)
	require.Len(t, result.Data["image_catalog"], 3)
	require.NotContains(t, result.Data, "image_text_evidence_truncated")
	evidence, ok := result.Data[types.DocumentImageTextEvidenceKey].([]documentImageTextEvidence)
	require.True(t, ok)
	require.Len(t, evidence, 2)
	require.Equal(t, firstOCR, evidence[0].Text)
	require.Equal(t, secondOCR, evidence[1].Text)
	for _, item := range evidence {
		require.Equal(t, parent.ID, item.ChunkID)
		require.Equal(t, parent.ID, item.ParentChunkID)
		require.Equal(t, item.StoredRef, item.Reference)
		require.Equal(t, page, item.Page)
	}

	model := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, result)
	require.Equal(t, 1, strings.Count(model, firstOCR))
	require.Contains(t, model, secondOCR)
	require.Contains(t, model, "use the source-bound image_ocr transcription as primary evidence")
	require.Contains(t, model, `"image_catalog":`)
	require.Less(t, strings.Index(model, secondOCR), strings.Index(model, `"image_catalog":`))
}
