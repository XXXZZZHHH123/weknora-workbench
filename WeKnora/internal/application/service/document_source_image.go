package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
)

// ReadDocumentSourceImage preserves the registered image ownership check, then
// renders deferred vector bytes against their exact original page and part.
func (s *knowledgeService) ReadDocumentSourceImage(ctx context.Context, knowledge *types.Knowledge, reference string, page int, part string) ([]byte, error) {
	if knowledge == nil {
		return nil, errors.New("document source unavailable")
	}
	images, ok := s.kbService.(interface {
		ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("document image reader unavailable")
	}
	data, err := images.ReadDocumentImage(ctx, knowledge, reference)
	if err != nil {
		return nil, err
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return data, nil
	}
	if !validPreviewMediaPart(part) {
		return nil, errors.New("deferred vector source selector invalid")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, knowledge.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != knowledge.TenantID {
		return nil, errors.New("original source storage scope mismatch")
	}
	files := s.resolveFileServiceForPath(ctx, kb, knowledge.FilePath)
	if files == nil {
		return nil, errors.New("original source storage unavailable")
	}
	reader, err := files.GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, previewMediaSourceLimit+1))
	if err != nil || len(content) == 0 || len(content) > previewMediaSourceLimit {
		return nil, errors.New("original source size/read check failed")
	}
	sourceHash, mediaHash := sha256.Sum256(content), sha256.Sum256(data)
	return s.renderKnowledgeSourceMedia(ctx, knowledge, page, part, hex.EncodeToString(sourceHash[:]), hex.EncodeToString(mediaHash[:]))
}

// Deferred source vectors are navigation assets, not raster model input. They
// are rendered on demand, avoiding meaningless OCR/caption calls on raw EMF.
func rasterMultimodalImages(images []docparser.StoredImage) []docparser.StoredImage {
	out := make([]docparser.StoredImage, 0, len(images))
	for _, img := range images {
		ref := strings.ToLower(img.OriginalRef)
		if strings.HasSuffix(ref, ".emf") || strings.HasSuffix(ref, ".wmf") {
			continue
		}
		out = append(out, img)
	}
	return out
}
