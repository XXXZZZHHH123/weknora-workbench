package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		"Batch\tIncoming\tRemaining\nAmber\t23.6\t91.7\nBlue\t18.2\t66.4",
		"Batch\tRemaining\tIncoming\nAmber\t91.7\t23.6\nBlue\t66.4\t18.2",
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
			require.Len(t, pageTexts, 6)
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
				model := modelcontext.NewRegistry(true).ModelToolResultForTool(ToolReadDocument, read)
				require.Contains(t, model, ocr)
				require.Contains(t, model, "VLM observations are visual readings and may be estimates")
				require.Contains(t, model, "recheck that location in the original image or explicitly report the unresolved conflict")
				require.NotContains(t, model, "900.0")
				require.NotContains(t, SanitizeToolDataForClient(ToolReadDocument, read.Data), types.DocumentImageTextEvidenceKey)
				if image.Reference == table {
					repo.children = repo.children[:len(repo.children)-1]
				}
			}
		})
	}
}
