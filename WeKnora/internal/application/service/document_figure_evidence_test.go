package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type figureKBService struct {
	interfaces.KnowledgeBaseService
	kb    *types.KnowledgeBase
	deny  bool
	media []byte
}

func (s *figureKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}
func (s *figureKBService) ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error) {
	if s.deny {
		return nil, errors.New("not bound")
	}
	if s.media != nil {
		return s.media, nil
	}
	return []byte("bound original"), nil
}

type figureReader struct {
	interfaces.DocumentReader
	calls        int
	mutate       func(*types.ReadResult)
	request      *types.ReadRequest
	media        []byte
	rendered     []byte
	renderCaller types.Caller
	renderTenant uint64
}

func (r *figureReader) Read(ctx context.Context, request *types.ReadRequest) (*types.ReadResult, error) {
	r.calls++
	r.request = request
	h := sha256.Sum256(request.FileContent)
	mediaHash := sha256.Sum256([]byte("bound original"))
	if r.media != nil {
		mediaHash = sha256.Sum256(r.media)
	}
	if request.ParserEngine == "source_media_png_v1" {
		r.renderCaller = types.CallerFromContext(ctx)
		r.renderTenant, _ = types.TenantIDFromContext(ctx)
		if r.rendered == nil {
			return nil, errors.New("render unavailable")
		}
		pngHash := sha256.Sum256(r.rendered)
		return &types.ReadResult{MarkdownContent: "rendered original", Metadata: map[string]string{
			"evidence_version": "source_media_png_v1", "source_file_sha256": hex.EncodeToString(h[:]),
			"source_media_sha256": hex.EncodeToString(mediaHash[:]),
			"source_page":         request.ParserEngineOverrides["source_page"], "source_part": request.ParserEngineOverrides["source_part"],
			"rendered_png_sha256": hex.EncodeToString(pngHash[:]), "rendered_png_base64": base64.StdEncoding.EncodeToString(r.rendered),
		}}, nil
	}
	result := &types.ReadResult{MarkdownContent: "source geometry", Metadata: map[string]string{
		"evidence_version": "source_figure_geometry_v1", "source_file_sha256": hex.EncodeToString(h[:]),
		"source_media_sha256": hex.EncodeToString(mediaHash[:]),
		"source_page":         request.ParserEngineOverrides["source_page"], "source_part": request.ParserEngineOverrides["source_part"],
		"geometry_json": `{"method":"original_emf_trace_geometry_v1","series":[]}`, "source_labels_json": `["Plot Alpha"]`,
	}}
	if r.mutate != nil {
		r.mutate(result)
	}
	return result, nil
}

// A parse-time preview and its original vector have different bytes. Only the
// persisted exact source relationship can bind that owned preview to geometry.
func TestDocumentFigureEvidenceBindsDerivedPreviewToOriginalMember(t *testing.T) {
	const ref = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const part = "ppt/media/figure.emf"
	vector := []byte("original vector records")
	var deck, preview bytes.Buffer
	archive := zip.NewWriter(&deck)
	member, err := archive.Create(part)
	require.NoError(t, err)
	_, err = member.Write(vector)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	require.NoError(t, png.Encode(&preview, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	digest := sha256.Sum256(deck.Bytes())
	for _, variant := range []string{"valid", "legacy-exact-render", "legacy-authorized-shared-source", "legacy-denied-resource", "legacy-different-pixels", "missing-binding", "foreign-resource", "wrong-page", "wrong-part", "wrong-version", "missing-version", "replaced-document", "replaced-during-read", "deleted-during-read", "reader-media-hash", "unbound-raster-bytes"} {
		t.Run(variant, func(t *testing.T) {
			knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileType: "pptx", FilePath: "local://7/source.pptx", FileHash: hex.EncodeToString(digest[:])}
			current := *knowledge
			repo := &previewMediaRepo{knowledge: &current}
			binding := types.DocumentImageSource{Reference: ref, Page: 2, SourcePart: part, SourceHash: knowledge.FileHash}
			chunks := &figureSourceChunks{source: binding}
			kb := &figureKBService{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}, media: preview.Bytes()}
			reader := &figureReader{media: vector}
			switch variant {
			case "legacy-exact-render", "legacy-authorized-shared-source", "legacy-denied-resource", "legacy-different-pixels":
				chunks.source.Reference = ""
				reader.rendered = preview.Bytes()
				kb.deny = variant == "legacy-denied-resource"
				if variant == "legacy-different-pixels" {
					pixels := image.NewRGBA(image.Rect(0, 0, 2, 2))
					pixels.Set(0, 0, color.RGBA{255, 0, 0, 255})
					var different bytes.Buffer
					require.NoError(t, png.Encode(&different, pixels))
					reader.rendered = different.Bytes()
				}
			case "missing-binding":
				chunks.source.Reference = ""
			case "foreign-resource":
				chunks.source.Reference = "resource://ZbCdEfGhIjKlMnOpQrStUv"
			case "wrong-page":
				chunks.source.Page = 3
			case "wrong-part":
				chunks.source.SourcePart = "ppt/media/other.emf"
			case "wrong-version":
				chunks.source.SourceHash = "stale"
			case "missing-version":
				knowledge.FileHash = ""
			case "replaced-document":
				current.FilePath = "local://7/replacement.pptx"
			case "replaced-during-read":
				replaced := current
				replaced.FileHash = "changed"
				repo.second = &replaced
			case "deleted-during-read":
				deleted := current
				deleted.DeletedAt.Valid = true
				repo.second = &deleted
			case "reader-media-hash":
				reader.media = []byte("different original vector")
			case "unbound-raster-bytes":
				kb.media = []byte("unknown format is not a derived preview")
			}
			service := &knowledgeService{repo: repo, chunkRepo: chunks, kbService: kb, documentReader: reader, fileSvc: &imageReaderFiles{body: deck.Bytes()}}
			ctx := previewMediaContext()
			caller := types.Caller{TenantID: 9, UserID: "reader-in-other-workspace", Role: types.TenantRoleViewer}
			if variant == "legacy-authorized-shared-source" || variant == "legacy-denied-resource" {
				ctx = types.WithCaller(types.WithExecutionTenant(ctx, 9), caller)
			}
			result, err := service.ReadDocumentFigureEvidence(ctx, knowledge, ref, 2, part, []string{"Alpha"})
			if variant == "valid" || variant == "legacy-exact-render" || variant == "legacy-authorized-shared-source" {
				require.NoError(t, err)
				require.Equal(t, "source_figure_geometry_v1", result["version"])
				mediaSHA := sha256.Sum256(vector)
				require.Equal(t, hex.EncodeToString(mediaSHA[:]), result["source_media_sha256"])
				if variant == "valid" {
					require.Equal(t, 1, reader.calls)
				} else {
					require.Equal(t, 2, reader.calls)
				}
				if variant == "legacy-authorized-shared-source" {
					require.Equal(t, caller, reader.renderCaller)
					require.Equal(t, uint64(7), reader.renderTenant)
					require.Equal(t, caller, types.CallerFromContext(ctx))
				}
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				if variant == "legacy-denied-resource" {
					require.Zero(t, reader.calls)
				}
			}
		})
	}
}

func TestDocumentFigurePreviewPixelIdentityRejectsNearMatches(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 8, 6))
	pixels.Set(2, 3, color.RGBA{10, 20, 30, 255})
	var a, b, c bytes.Buffer
	require.NoError(t, png.Encode(&a, pixels))
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	require.NoError(t, encoder.Encode(&b, pixels))
	require.NotEqual(t, a.Bytes(), b.Bytes())
	require.True(t, sameFigurePreviewPixels(a.Bytes(), b.Bytes()))
	pixels.Set(2, 3, color.RGBA{11, 20, 30, 255})
	require.NoError(t, png.Encode(&c, pixels))
	require.False(t, sameFigurePreviewPixels(a.Bytes(), c.Bytes()))
	require.False(t, sameFigurePreviewPixels(a.Bytes(), []byte("invalid PNG")))
}

func TestDocumentFigureMemberIdentityRequiresUniqueBoundedZIPMember(t *testing.T) {
	for _, variant := range []string{"valid", "missing", "duplicate", "oversized", "traversal"} {
		t.Run(variant, func(t *testing.T) {
			var content bytes.Buffer
			archive := zip.NewWriter(&content)
			part := "ppt/media/plot.emf"
			name := part
			if variant == "missing" {
				name = "ppt/media/other.emf"
			}
			if variant == "traversal" {
				part, name = "ppt/media/../plot.emf", "ppt/media/../plot.emf"
			}
			member, err := archive.Create(name)
			require.NoError(t, err)
			body := []byte("original vector")
			if variant == "oversized" {
				body = bytes.Repeat([]byte("a"), (16<<20)+1)
			}
			_, err = member.Write(body)
			require.NoError(t, err)
			if variant == "duplicate" {
				member, err = archive.Create(name)
				require.NoError(t, err)
				_, err = member.Write([]byte("other vector"))
				require.NoError(t, err)
			}
			require.NoError(t, archive.Close())
			digest, err := originalFigureMemberSHA(content.Bytes(), part)
			if variant == "valid" {
				require.NoError(t, err)
				want := sha256.Sum256(body)
				require.Equal(t, hex.EncodeToString(want[:]), digest)
			} else {
				require.Error(t, err)
			}
		})
	}
}

type figureSourceChunks struct {
	interfaces.ChunkRepository
	source types.DocumentImageSource
}

func (r *figureSourceChunks) FindDocumentImageSourceReferences(_ context.Context, tenantID uint64, knowledgeID string, page int, part, hash string) ([]string, error) {
	if tenantID == 7 && knowledgeID == "doc" && r.source.Page == page && r.source.SourcePart == part && r.source.SourceHash == hash && r.source.Reference != "" {
		return []string{r.source.Reference}, nil
	}
	return nil, nil
}
func TestDocumentFigureEvidenceChecksScopeAndOriginalResponse(t *testing.T) {
	for _, variant := range []string{"valid", "permission", "kb", "part", "page", "hash", "stored-hash", "media-hash", "response-page", "response-part", "geometry", "failed", "selection-options", "selection-media-hash"} {
		t.Run(variant, func(t *testing.T) {
			kb := &figureKBService{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7}}
			reader := &figureReader{}
			files := &imageReaderFiles{body: []byte("original deck")}
			service := &knowledgeService{kbService: kb, documentReader: reader, fileSvc: files}
			knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileType: "pptx", FilePath: "local://7/source.pptx"}
			page, part := 2, "ppt/media/figure.emf"
			switch variant {
			case "permission":
				kb.deny = true
			case "kb":
				kb.kb.TenantID = 8
			case "part":
				part = "ppt/media/../private.emf"
			case "page":
				page = 0
			case "hash":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["source_file_sha256"] = "wrong" }
			case "stored-hash":
				knowledge.FileHash = "00000000000000000000000000000000"
			case "media-hash":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["source_media_sha256"] = "another-bound-image" }
			case "response-page":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["source_page"] = "3" }
			case "response-part":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["source_part"] = "ppt/media/another.emf" }
			case "geometry":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["geometry_json"] = `{"method":"guessed"}` }
			case "failed":
				reader.mutate = func(r *types.ReadResult) { r.Error = "unsupported chart" }
			case "selection-options", "selection-media-hash":
				reader.mutate = func(r *types.ReadResult) {
					r.Metadata["evidence_version"] = "source_figure_selection_options_v1"
					r.Metadata["available_series_json"] = `["Alpha","Beta"]`
					if variant == "selection-media-hash" {
						r.Metadata["source_media_sha256"] = "another-image"
					}
				}
			}
			selection := []string(nil)
			if variant == "selection-options" {
				selection = []string{"not-a-legend"}
			}
			result, err := service.ReadDocumentFigureEvidence(t.Context(), knowledge, "resource://AbCdEfGhIjKlMnOpQrStUv", page, part, selection)
			if variant == "valid" {
				require.NoError(t, err)
				require.Equal(t, "ppt/media/figure.emf", result["source_part"])
				require.Equal(t, "source_figure_geometry_v1", reader.request.ParserEngine)
				require.Equal(t, files.body, reader.request.FileContent)
			} else if variant == "selection-options" {
				require.NoError(t, err)
				require.Equal(t, []string{"Alpha", "Beta"}, result["available_series"])
				require.NotContains(t, result, "geometry")
				require.NotContains(t, result, "markdown")
			} else {
				require.Error(t, err)
				require.Nil(t, result)
			}
			if variant == "stored-hash" {
				require.Zero(t, reader.calls)
			}
			if variant == "permission" || variant == "kb" || variant == "part" || variant == "page" {
				require.Zero(t, reader.calls)
				require.Empty(t, files.reads)
			}
		})
	}
}

func TestDocumentFigureGeometryPreservesRealResourceOwnershipChecks(t *testing.T) {
	for _, variant := range []string{"valid", "other-document", "other-tenant", "foreign-kb", "deleted", "bad-ref"} {
		t.Run(variant, func(t *testing.T) {
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			kb := &types.KnowledgeBase{ID: "kb", TenantID: 7}
			knowledge := &types.Knowledge{ID: "doc", TenantID: 7, KnowledgeBaseID: "kb", FileType: "pptx", FilePath: "local://7/source.pptx"}
			catalog := &documentImageCatalogFake{owner: "doc", refs: []string{ref},
				resource: &types.StoredResource{TenantID: 7, State: types.ResourceStateActive}}
			switch variant {
			case "other-document":
				catalog.owner = "different-document"
			case "other-tenant":
				catalog.resource.TenantID = 8
			case "foreign-kb":
				kb.TenantID = 8
			case "deleted":
				catalog.resource.State = types.ResourceStateDeleted
			case "bad-ref":
				ref = "https://example.invalid/unbound.emf"
			}
			imageFiles := &imageReaderFiles{body: []byte("bound original")}
			kbService := &knowledgeBaseService{repo: &imageReaderKBRepo{kb: kb}, tenantRepo: &attrsTenantRepo{},
				fileSvc: imageFiles, resourceCatalog: catalog}
			originalFiles := &imageReaderFiles{body: []byte("original deck")}
			reader := &figureReader{}
			service := &knowledgeService{kbService: kbService, fileSvc: originalFiles, documentReader: reader}
			result, err := service.ReadDocumentFigureEvidence(t.Context(), knowledge, ref, 2, "ppt/media/figure.emf", []string{"Alpha"})
			if variant == "valid" {
				require.NoError(t, err)
				require.Equal(t, "source_figure_geometry_v1", result["version"])
				require.Equal(t, 1, reader.calls)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Empty(t, imageFiles.reads)
				require.Empty(t, originalFiles.reads)
				require.Zero(t, reader.calls)
			}
		})
	}
}
