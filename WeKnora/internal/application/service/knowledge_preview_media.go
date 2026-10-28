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
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	previewMediaSourceLimit = 32 << 20
	previewMediaPNGLimit    = 8 << 20
	previewSourceSHACap     = 512
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

// previewSourceKey identifies one immutable stored original. A reparse or
// replacement changes FilePath or FileHash, so stale digests are never reused.
type previewSourceKey struct{ knowledgeID, filePath, fileHash string }

// previewSourceSHAs remembers original SHA-256 digests so each preview media
// request does not re-read and re-hash the whole presentation.
var previewSourceSHAs = struct {
	sync.Mutex
	digests map[previewSourceKey]string
	order   []previewSourceKey
}{digests: map[previewSourceKey]string{}}

func cachedPreviewSourceSHA(key previewSourceKey) string {
	previewSourceSHAs.Lock()
	defer previewSourceSHAs.Unlock()
	return previewSourceSHAs.digests[key]
}

func rememberPreviewSourceSHA(key previewSourceKey, digest string) {
	previewSourceSHAs.Lock()
	defer previewSourceSHAs.Unlock()
	if _, ok := previewSourceSHAs.digests[key]; ok {
		return
	}
	if len(previewSourceSHAs.order) >= previewSourceSHACap {
		delete(previewSourceSHAs.digests, previewSourceSHAs.order[0])
		previewSourceSHAs.order = previewSourceSHAs.order[1:]
	}
	previewSourceSHAs.digests[key] = digest
	previewSourceSHAs.order = append(previewSourceSHAs.order, key)
}

// RenderKnowledgePreviewMedia returns one authorized original PPTX media part
// as PNG for preview. Parsing already rasterized most vector parts and bound
// them to their source page/part; those are served from storage. Only parts the
// parse budget left as raw vectors are converted on demand.
func (s *knowledgeService) RenderKnowledgePreviewMedia(ctx context.Context, knowledge *types.Knowledge, page int, part, expectedSourceSHA256 string) ([]byte, error) {
	if stored := s.storedKnowledgePreviewMedia(ctx, knowledge, page, part, expectedSourceSHA256); stored != nil {
		return stored, nil
	}
	return s.renderKnowledgeSourceMedia(ctx, knowledge, page, part, expectedSourceSHA256, "")
}

// storedKnowledgePreviewMedia returns the parse-time PNG for a source part, or
// nil whenever any binding cannot be proven; the caller then takes the fully
// verified render path, which also reports the precise error.
func (s *knowledgeService) storedKnowledgePreviewMedia(ctx context.Context, knowledge *types.Knowledge, page int, part, expectedSourceSHA256 string) []byte {
	if knowledge == nil || s.chunkRepo == nil || s.resourceCatalog == nil ||
		strings.ToLower(strings.TrimPrefix(knowledge.FileType, ".")) != "pptx" ||
		page < 1 || !validPreviewMediaPart(part) || !previewMediaSHA256.MatchString(expectedSourceSHA256) {
		return nil
	}
	tenantID, ok := ctx.Value(types.TenantIDContextKey).(uint64)
	if !ok || tenantID == 0 || tenantID != knowledge.TenantID {
		return nil
	}
	current, err := s.repo.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	if err != nil || current == nil || current.DeletedAt.Valid || current.ParseStatus == types.ParseStatusDeleting ||
		current.TenantID != knowledge.TenantID || current.KnowledgeBaseID != knowledge.KnowledgeBaseID ||
		current.FilePath != knowledge.FilePath || current.FileHash != knowledge.FileHash || current.FileHash == "" {
		return nil
	}
	refs, err := s.chunkRepo.FindDocumentImageSourceReferences(ctx, tenantID, current.ID, page, part, current.FileHash)
	if err != nil || len(refs) != 1 {
		return nil
	}
	resource, err := s.resourceCatalog.Resolve(ctx, refs[0])
	if err != nil || resource == nil || resource.TenantID != tenantID ||
		resource.State != types.ResourceStateActive || resource.MimeType != "image/png" {
		return nil
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, current.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != tenantID {
		return nil
	}
	key := previewSourceKey{current.ID, current.FilePath, current.FileHash}
	digest := cachedPreviewSourceSHA(key)
	if digest == "" {
		if digest = s.previewSourceSHA(ctx, kb, current); digest == "" {
			return nil
		}
		rememberPreviewSourceSHA(key, digest)
	}
	if !strings.EqualFold(digest, expectedSourceSHA256) {
		return nil
	}
	files := s.resolveFileServiceForPath(ctx, kb, refs[0])
	if files == nil {
		return nil
	}
	reader, err := files.GetFile(ctx, refs[0])
	if err != nil {
		return nil
	}
	defer reader.Close()
	pngBytes, err := io.ReadAll(io.LimitReader(reader, previewMediaPNGLimit+1))
	if err != nil || len(pngBytes) == 0 || len(pngBytes) > previewMediaPNGLimit {
		return nil
	}
	config, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 12000 || config.Height > 12000 ||
		int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil
	}
	return pngBytes
}

// previewSourceSHA hashes the stored original once it matches the upload
// fingerprint, returning "" when the source cannot be read or has changed.
func (s *knowledgeService) previewSourceSHA(ctx context.Context, kb *types.KnowledgeBase, knowledge *types.Knowledge) string {
	files := s.resolveFileServiceForPath(ctx, kb, knowledge.FilePath)
	if files == nil {
		return ""
	}
	reader, err := files.GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return ""
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, previewMediaSourceLimit+1))
	if err != nil || len(content) == 0 || len(content) > previewMediaSourceLimit {
		return ""
	}
	fileSHA := sha256.Sum256(content)
	fileSHAHex := hex.EncodeToString(fileSHA[:])
	switch len(knowledge.FileHash) {
	case 32:
		stored := md5.Sum(content)
		if !strings.EqualFold(hex.EncodeToString(stored[:]), knowledge.FileHash) {
			return ""
		}
	case 64:
		if !strings.EqualFold(fileSHAHex, knowledge.FileHash) {
			return ""
		}
	default:
		return ""
	}
	return fileSHAHex
}

func (s *knowledgeService) renderKnowledgeSourceMedia(ctx context.Context, knowledge *types.Knowledge, page int, part, expectedSourceSHA256, expectedMediaSHA256 string) ([]byte, error) {
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
	rememberPreviewSourceSHA(previewSourceKey{current.ID, current.FilePath, current.FileHash}, fileSHAHex)

	// Allow the reader's bounded capacity wait (10s) and conversion (30s)
	// separately, with transport overhead. A queued request retains its full
	// conversion budget; caller cancellation still ends the RPC immediately.
	readCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	result, err := s.documentReader.Read(readCtx, &types.ReadRequest{
		FileContent: content, FileName: knowledge.FileName, FileType: "pptx", ParserEngine: "source_media_png_v1",
		ParserEngineOverrides: map[string]string{"source_page": strconv.Itoa(page), "source_part": part},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readCtx.Err() == context.DeadlineExceeded {
			return nil, previewMediaTimeoutError()
		}
		return nil, err
	}
	if result != nil && result.Error != "" && result.Metadata["evidence_version"] == "source_media_png_v1" {
		switch result.Metadata["source_media_error_code"] {
		case "invalid_selector":
			return nil, apperrors.NewBadRequestError("Selected media does not belong to the requested source page")
		case "busy":
			return nil, apperrors.NewServiceUnavailableError("Media preview capacity is busy; retry the page")
		case "timeout":
			return nil, previewMediaTimeoutError()
		case "cancelled":
			return nil, context.Canceled
		}
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
	if expectedMediaSHA256 != "" && !strings.EqualFold(metadata["source_media_sha256"], expectedMediaSHA256) {
		return nil, errors.New("stored vector does not match selected original media")
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

func previewMediaTimeoutError() *apperrors.AppError {
	return &apperrors.AppError{Code: apperrors.ErrTimeout, HTTPCode: http.StatusGatewayTimeout, Message: "Source media conversion timed out"}
}
