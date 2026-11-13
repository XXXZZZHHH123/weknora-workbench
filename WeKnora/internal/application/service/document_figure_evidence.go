package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

var originalFigurePart = regexp.MustCompile(`(?i)^ppt/media/[A-Za-z0-9_.-]+\.emf$`)

// This reads a selected original, not an ingestion job. The image binding and
// file storage scope are checked before any bytes reach the document reader.
func (s *knowledgeService) ReadDocumentFigureEvidence(ctx context.Context, knowledge *types.Knowledge, reference string, page int, part string, series []string) (map[string]any, error) {
	if knowledge == nil || strings.ToLower(strings.TrimPrefix(knowledge.FileType, ".")) != "pptx" {
		return nil, nil
	}
	if page < 1 || !originalFigurePart.MatchString(part) || s.documentReader == nil {
		return nil, errors.New("original figure extraction is unavailable")
	}
	var media []byte
	var err error
	originalSelection := strings.HasPrefix(reference, types.OriginalMediaPrefix)
	if originalSelection {
		media, _, err = s.readOriginalMediaSelection(ctx, knowledge, reference, page, part)
	} else {
		images, ok := s.kbService.(interface {
			ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error)
		})
		if !ok {
			return nil, errors.New("original figure binding verifier unavailable")
		}
		media, err = images.ReadDocumentImage(ctx, knowledge, reference)
	}
	if err != nil {
		return nil, err
	}
	if len(media) == 0 || len(media) > 32<<20 {
		return nil, errors.New("original figure size/read check failed")
	}
	mediaDigest := sha256.Sum256(media)
	mediaHash := hex.EncodeToString(mediaDigest[:])
	derivedPreview := http.DetectContentType(media) == "image/png"
	if derivedPreview {
		// Legacy preview verification and geometry share one bounded request;
		// their individual RPC budgets must not accumulate beyond this limit.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 50*time.Second)
		defer cancel()
	}
	if (derivedPreview || originalSelection) && !s.figureSourceCurrent(ctx, knowledge) {
		return nil, errors.New("original figure source changed or is no longer readable")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, knowledge.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != knowledge.TenantID {
		return nil, errors.New("original figure storage scope mismatch")
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
	content, err := io.ReadAll(io.LimitReader(reader, (32<<20)+1))
	if err != nil || len(content) == 0 || len(content) > 32<<20 {
		return nil, errors.New("original source size/read check failed")
	}
	digest := sha256.Sum256(content)
	fileHash := hex.EncodeToString(digest[:])
	// Existing uploads store an MD5 content fingerprint; it is an integrity
	// check here, while authorization remains the independent scope checks.
	if len(knowledge.FileHash) == 32 {
		stored := md5.Sum(content)
		if !strings.EqualFold(hex.EncodeToString(stored[:]), knowledge.FileHash) {
			return nil, errors.New("original source changed since upload")
		}
	} else if len(knowledge.FileHash) == 64 && !strings.EqualFold(fileHash, knowledge.FileHash) {
		return nil, errors.New("original source changed since upload")
	}
	if derivedPreview {
		// Raster previews are not the original vector's bytes. Bind them through
		// an exact parse-time relationship to this immutable source revision.
		mediaHash, err = originalFigureMemberSHA(content, part)
		if err != nil {
			return nil, err
		}
		bound := false
		if s.chunkRepo != nil {
			refs, bindErr := s.chunkRepo.FindDocumentImageSourceReferences(ctx, knowledge.TenantID, knowledge.ID, page, part, knowledge.FileHash)
			if bindErr != nil {
				return nil, bindErr
			}
			bound = slices.Contains(refs, reference)
		}
		if !bound {
			// Older imports lack the parse-time relationship. Prove the identity
			// by rendering the exact original page/member and comparing pixels,
			// never by interpreting Markdown locators or visual similarity.
			// The tool has already authorized this document, including shared
			// sources. Execute in its storage tenant without changing the caller
			// identity used by any subsequent authorization checks.
			renderCtx := types.WithExecutionTenant(ctx, knowledge.TenantID)
			rendered, renderErr := s.renderKnowledgeSourceMedia(renderCtx, knowledge, page, part, fileHash, mediaHash)
			if renderErr != nil {
				return nil, renderErr
			}
			if !sameFigurePreviewPixels(media, rendered) {
				return nil, errors.New("stored preview does not match selected original media")
			}
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	selected, err := json.Marshal(series)
	if err != nil {
		return nil, err
	}
	result, err := s.documentReader.Read(readCtx, &types.ReadRequest{FileContent: content,
		FileName: knowledge.FileName, FileType: "pptx", ParserEngine: "source_figure_geometry_v1",
		ParserEngineOverrides: map[string]string{"source_page": strconv.Itoa(page), "source_part": part, "series_json": string(selected)}})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Error != "" ||
		result.Metadata["source_file_sha256"] != fileHash || result.Metadata["source_media_sha256"] != mediaHash ||
		result.Metadata["source_page"] != strconv.Itoa(page) || result.Metadata["source_part"] != part {
		return nil, errors.New("original figure extraction did not verify its source")
	}
	if (derivedPreview || originalSelection) && !s.figureSourceCurrent(ctx, knowledge) {
		return nil, errors.New("original figure source changed during extraction")
	}
	if result.Metadata["evidence_version"] == "source_figure_selection_options_v1" {
		var available []string
		if json.Unmarshal([]byte(result.Metadata["available_series_json"]), &available) != nil || len(available) == 0 || len(available) > 32 {
			return nil, errors.New("invalid original legend selection options")
		}
		known := map[string]bool{}
		for _, label := range available {
			if strings.TrimSpace(label) == "" || known[label] {
				return nil, errors.New("ambiguous original legend selection options")
			}
			known[label] = true
		}
		invalid := len(series) == 0
		for _, label := range series {
			invalid = invalid || !known[label]
		}
		if !invalid {
			return nil, errors.New("original legend selection response contradicts the request")
		}
		return map[string]any{"version": "source_figure_selection_options_v1", "available_series": available,
			"source_file_sha256": fileHash, "source_page": page, "source_part": part}, nil
	}
	if result.Metadata["evidence_version"] != "source_figure_geometry_v1" {
		return nil, errors.New("unknown original figure evidence version")
	}
	var geometry map[string]any
	var labels []string
	if json.Unmarshal([]byte(result.Metadata["geometry_json"]), &geometry) != nil || geometry["method"] != "original_emf_trace_geometry_v1" ||
		json.Unmarshal([]byte(result.Metadata["source_labels_json"]), &labels) != nil || strings.TrimSpace(result.MarkdownContent) == "" {
		return nil, fmt.Errorf("invalid original figure geometry evidence")
	}
	return map[string]any{"version": result.Metadata["evidence_version"], "source_file_sha256": fileHash,
		"source_media_sha256": result.Metadata["source_media_sha256"], "source_page": page, "source_part": part,
		"source_labels": labels, "geometry": geometry, "markdown": result.MarkdownContent}, nil
}

func (s *knowledgeService) figureSourceCurrent(ctx context.Context, knowledge *types.Knowledge) bool {
	if s.repo == nil || (len(knowledge.FileHash) != 32 && len(knowledge.FileHash) != 64) {
		return false
	}
	current, err := s.repo.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	return err == nil && current != nil && !current.DeletedAt.Valid && current.ParseStatus != types.ParseStatusDeleting &&
		current.TenantID == knowledge.TenantID && current.KnowledgeBaseID == knowledge.KnowledgeBaseID &&
		current.FilePath == knowledge.FilePath && current.FileHash == knowledge.FileHash && current.FileName == knowledge.FileName &&
		strings.ToLower(strings.TrimPrefix(current.FileType, ".")) == "pptx"
}

// Hash the precise member independently of the reader's response. Duplicate ZIP
// names and oversized decompressed members cannot establish a unique identity.
func originalFigureMemberSHA(content []byte, part string) (string, error) {
	if !originalFigurePart.MatchString(part) {
		return "", errors.New("invalid original figure part")
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", errors.New("invalid original figure container")
	}
	var selected *zip.File
	for _, member := range archive.File {
		if member.Name == part {
			if selected != nil {
				return "", errors.New("ambiguous original figure member")
			}
			selected = member
		}
	}
	const limit = 16 << 20
	if selected == nil || selected.UncompressedSize64 == 0 || selected.UncompressedSize64 > limit {
		return "", errors.New("original figure member unavailable or oversized")
	}
	reader, err := selected.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(reader, limit+1))
	if err != nil || size == 0 || size > limit {
		return "", errors.New("original figure member integrity check failed")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func sameFigurePreviewPixels(stored, rendered []byte) bool {
	for _, data := range [][]byte{stored, rendered} {
		if len(data) == 0 || len(data) > previewMediaPNGLimit {
			return false
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 12000 || config.Height > 12000 || int64(config.Width)*int64(config.Height) > 40_000_000 {
			return false
		}
	}
	if bytes.Equal(stored, rendered) {
		return true
	}
	a, err := png.Decode(bytes.NewReader(stored))
	if err != nil {
		return false
	}
	b, err := png.Decode(bytes.NewReader(rendered))
	if err != nil || a.Bounds() != b.Bounds() {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return false
			}
		}
	}
	return true
}
