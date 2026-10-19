package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type figureKBService struct {
	interfaces.KnowledgeBaseService
	kb   *types.KnowledgeBase
	deny bool
}

func (s *figureKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}
func (s *figureKBService) ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error) {
	if s.deny {
		return nil, errors.New("not bound")
	}
	return []byte("bound original"), nil
}

type figureReader struct {
	interfaces.DocumentReader
	calls   int
	mutate  func(*types.ReadResult)
	request *types.ReadRequest
}

func (r *figureReader) Read(_ context.Context, request *types.ReadRequest) (*types.ReadResult, error) {
	r.calls++
	r.request = request
	h := sha256.Sum256(request.FileContent)
	result := &types.ReadResult{MarkdownContent: "source geometry", Metadata: map[string]string{
		"evidence_version": "source_figure_geometry_v1", "source_file_sha256": hex.EncodeToString(h[:]),
		"source_page": request.ParserEngineOverrides["source_page"], "source_part": request.ParserEngineOverrides["source_part"],
		"geometry_json": `{"method":"original_emf_trace_geometry_v1","series":[]}`, "source_labels_json": `["Plot Alpha"]`,
	}}
	if r.mutate != nil {
		r.mutate(result)
	}
	return result, nil
}
func TestDocumentFigureEvidenceChecksScopeAndOriginalResponse(t *testing.T) {
	for _, variant := range []string{"valid", "permission", "kb", "part", "page", "hash", "response-page", "geometry", "failed", "selection-options"} {
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
			case "response-page":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["source_page"] = "3" }
			case "geometry":
				reader.mutate = func(r *types.ReadResult) { r.Metadata["geometry_json"] = `{"method":"guessed"}` }
			case "failed":
				reader.mutate = func(r *types.ReadResult) { r.Error = "unsupported chart" }
			case "selection-options":
				reader.mutate = func(r *types.ReadResult) {
					r.Metadata["evidence_version"] = "source_figure_selection_options_v1"
					r.Metadata["available_series_json"] = `["Alpha","Beta"]`
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
			if variant == "permission" || variant == "kb" || variant == "part" || variant == "page" {
				require.Zero(t, reader.calls)
				require.Empty(t, files.reads)
			}
		})
	}
}
