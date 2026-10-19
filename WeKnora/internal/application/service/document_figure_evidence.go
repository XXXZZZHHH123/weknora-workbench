package service

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
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
	images, ok := s.kbService.(interface {
		ReadDocumentImage(context.Context, *types.Knowledge, string) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("original figure binding verifier unavailable")
	}
	if _, err := images.ReadDocumentImage(ctx, knowledge, reference); err != nil {
		return nil, err
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
		result.Metadata["source_file_sha256"] != fileHash || result.Metadata["source_page"] != strconv.Itoa(page) || result.Metadata["source_part"] != part {
		return nil, errors.New("original figure extraction did not verify its source")
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
