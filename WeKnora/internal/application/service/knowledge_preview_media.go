package service

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	previewMediaSourceLimit = 32 << 20
	previewMediaPNGLimit    = 8 << 20
)

var (
	previewMediaSHA256 = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)
)

func validPreviewMediaPart(part string) bool {
	const prefix = "ppt/media/"
	if !strings.HasPrefix(part, prefix) || len(part) > 1024 {
		return false
	}
	member := strings.TrimPrefix(part, prefix)
	if member == "" || strings.ContainsAny(member, `/\\`) || path.Clean(part) != part {
		return false
	}
	for _, r := range member {
		if unicode.IsControl(r) {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(member))
	stem := strings.TrimSuffix(member, path.Ext(member))
	return (ext == ".emf" || ext == ".wmf") && strings.TrimSpace(stem) != "" && stem != "." && stem != ".."
}

// RenderKnowledgePreviewMedia converts one authorized original PPTX media part
// to PNG for preview. The result is never persisted or attached to the source.
func (s *knowledgeService) RenderKnowledgePreviewMedia(ctx context.Context, knowledge *types.Knowledge, page int, part, expectedSourceSHA256 string) ([]byte, error) {
	if knowledge == nil || strings.ToLower(strings.TrimPrefix(knowledge.FileType, ".")) != "pptx" {
		return nil, apperrors.NewBadRequestError("Source media preview requires a PPTX knowledge file")
	}
	tenantID, ok := ctx.Value(types.TenantIDContextKey).(uint64)
	if !ok || tenantID == 0 || tenantID != knowledge.TenantID {
		return nil, errors.New("source media tenant context mismatch")
	}
	if knowledge.DeletedAt.Valid || knowledge.ParseStatus == types.ParseStatusDeleting {
		return nil, errors.New("knowledge source is no longer readable")
	}
	if page < 1 || !validPreviewMediaPart(part) || !previewMediaSHA256.MatchString(expectedSourceSHA256) {
		return nil, apperrors.NewBadRequestError("Invalid source media selector")
	}
	if s.documentReader == nil {
		return nil, errors.New("document reader unavailable")
	}
	current, err := s.repo.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	if err != nil || current == nil || current.DeletedAt.Valid || current.ParseStatus == types.ParseStatusDeleting ||
		current.TenantID != knowledge.TenantID || current.KnowledgeBaseID != knowledge.KnowledgeBaseID ||
		current.FilePath != knowledge.FilePath || current.FileHash != knowledge.FileHash || current.FileName != knowledge.FileName ||
		strings.ToLower(strings.TrimPrefix(current.FileType, ".")) != "pptx" {
		return nil, errors.New("knowledge source changed or is no longer readable")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, knowledge.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != knowledge.TenantID {
		return nil, errors.New("source media storage scope mismatch")
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
	fileSHA := sha256.Sum256(content)
	fileSHAHex := hex.EncodeToString(fileSHA[:])
	if !strings.EqualFold(fileSHAHex, expectedSourceSHA256) {
		return nil, apperrors.NewConflictError("Original source changed; reload the preview")
	}
	switch len(knowledge.FileHash) {
	case 32:
		stored := md5.Sum(content)
		if !strings.EqualFold(hex.EncodeToString(stored[:]), knowledge.FileHash) {
			return nil, errors.New("original source changed since upload")
		}
	case 64:
		if !strings.EqualFold(fileSHAHex, knowledge.FileHash) {
			return nil, errors.New("original source changed since upload")
		}
	default:
		return nil, errors.New("original source fingerprint unavailable")
	}

	readCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	result, err := s.documentReader.Read(readCtx, &types.ReadRequest{
		FileContent: content, FileName: knowledge.FileName, FileType: "pptx", ParserEngine: "source_media_png_v1",
		ParserEngineOverrides: map[string]string{"source_page": strconv.Itoa(page), "source_part": part},
	})
	if err != nil {
		return nil, err
	}
	if result != nil && result.Error != "" && result.Metadata["evidence_version"] == "source_media_png_v1" && result.Metadata["source_media_error_code"] == "invalid_selector" {
		return nil, apperrors.NewBadRequestError("Selected media does not belong to the requested source page")
	}
	if result == nil || result.Error != "" || strings.TrimSpace(result.MarkdownContent) == "" {
		return nil, errors.New("document reader returned no rendered media")
	}
	metadata := result.Metadata
	if metadata["evidence_version"] != "source_media_png_v1" ||
		!strings.EqualFold(metadata["source_file_sha256"], fileSHAHex) ||
		metadata["source_page"] != strconv.Itoa(page) || metadata["source_part"] != part ||
		!previewMediaSHA256.MatchString(metadata["source_media_sha256"]) ||
		!previewMediaSHA256.MatchString(metadata["rendered_png_sha256"]) {
		return nil, errors.New("document reader media binding did not verify")
	}
	encoded := metadata["rendered_png_base64"]
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(previewMediaPNGLimit) {
		return nil, errors.New("rendered media payload exceeds limits")
	}
	pngBytes, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(pngBytes) == 0 || len(pngBytes) > previewMediaPNGLimit {
		return nil, errors.New("invalid rendered media payload")
	}
	pngSHA := sha256.Sum256(pngBytes)
	if !strings.EqualFold(hex.EncodeToString(pngSHA[:]), metadata["rendered_png_sha256"]) ||
		!bytes.HasPrefix(pngBytes, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errors.New("rendered PNG integrity check failed")
	}
	config, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 12000 || config.Height > 12000 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, fmt.Errorf("rendered PNG dimensions are invalid")
	}
	current, err = s.repo.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	if err != nil || current == nil || current.DeletedAt.Valid || current.ParseStatus == types.ParseStatusDeleting ||
		current.TenantID != knowledge.TenantID || current.KnowledgeBaseID != knowledge.KnowledgeBaseID ||
		current.FilePath != knowledge.FilePath || current.FileHash != knowledge.FileHash || current.FileName != knowledge.FileName ||
		strings.ToLower(strings.TrimPrefix(current.FileType, ".")) != "pptx" {
		return nil, errors.New("knowledge source changed or is no longer readable")
	}
	return pngBytes, nil
}
