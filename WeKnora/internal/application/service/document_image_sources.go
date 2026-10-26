package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
)

var pptxMediaSourcePart = regexp.MustCompile(`(?i)^ppt/media/[A-Za-z0-9_.-]+\.(emf|wmf|png|jpe?g|gif|bmp|tiff?|webp)$`)

// resolvedDocumentImageSources joins parser ZIP relationships to successfully
// stored images before text splitting. No numeric geometry is precomputed.
func resolvedDocumentImageSources(result *types.ReadResult, images []docparser.StoredImage, sourceHash string) []types.DocumentImageSource {
	if result == nil || sourceHash == "" {
		return nil
	}
	var out []types.DocumentImageSource
	seen := map[types.DocumentImageSource]bool{}
	for _, img := range images {
		if _, ok := types.ParseResourcePath(img.ServingURL); ok && img.SourcePage > 0 {
			binding := types.DocumentImageSource{Reference: img.ServingURL, Page: img.SourcePage, SourceHash: sourceHash}
			if !seen[binding] {
				out = append(out, binding)
				seen[binding] = true
			}
		}
	}
	var parsed []struct {
		OriginalRef string `json:"original_ref"`
		Page        int    `json:"page"`
		SourcePart  string `json:"source_part"`
	}
	if raw := result.Metadata["pptx_image_sources_v1"]; len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &parsed) != nil || len(parsed) > 4096 {
		return out
	}
	stored := map[string]string{}
	for _, img := range images {
		if _, ok := types.ParseResourcePath(img.ServingURL); ok {
			stored[img.OriginalRef] = img.ServingURL
		}
	}
	for _, item := range parsed {
		ref := stored[item.OriginalRef]
		if ref == "" || item.Page < 1 || !pptxMediaSourcePart.MatchString(item.SourcePart) {
			continue
		}
		binding := types.DocumentImageSource{Reference: ref, Page: item.Page, SourcePart: item.SourcePart, SourceHash: sourceHash}
		if !seen[binding] {
			out = append(out, binding)
			seen[binding] = true
		}
	}
	return out
}

func documentImageSourceMetadata(content string, sources []types.DocumentImageSource) types.JSON {
	var matched []types.DocumentImageSource
	for _, source := range sources {
		if !strings.Contains(content, "]("+source.Reference+")") {
			continue
		}
		// Text source locators often map only native paragraphs, excluding
		// embedded figures. The parser's exact resource/slide relationship is
		// independent of that text alignment. A page read selects its own
		// relationship; ambiguous selectors are rejected by the figure reader.
		matched = append(matched, source)
	}
	if len(matched) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(map[string]any{types.DocumentImageSourcesKey: matched})
	return types.JSON(encoded)
}

// Clones copy the same original but own new image resources. Keep the parser
// relationship on that new resource; never leave a reference to the old owner.
func cloneDocumentImageSourceMetadata(metadata types.JSON, urls map[string]string) types.JSON {
	sources := types.ChunkDocumentImageSources(metadata)
	if len(sources) == 0 {
		return metadata
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(metadata, &envelope) != nil {
		return metadata
	}
	var rewritten []types.DocumentImageSource
	for _, source := range sources {
		if ref := urls[source.Reference]; ref != "" {
			source.Reference = ref
			rewritten = append(rewritten, source)
		}
	}
	encoded, _ := json.Marshal(rewritten)
	envelope[types.DocumentImageSourcesKey] = encoded
	out, _ := json.Marshal(envelope)
	return types.JSON(out)
}

// Original pixels are usable gallery assets even when OCR is disabled or skipped.
// Captions describe their provenance only; no model observations are invented.
func storedSourceImageInfo(content string, images []docparser.StoredImage) string {
	var info []types.ImageInfo
	seen := map[string]bool{}
	for _, img := range images {
		if _, valid := types.ParseResourcePath(img.ServingURL); !valid || seen[img.ServingURL] {
			continue
		}
		switch img.MimeType {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		default:
			continue
		}
		at := strings.Index(content, "]("+img.ServingURL+")")
		if at < 0 {
			continue
		}
		seen[img.ServingURL] = true
		caption := "原文件图片"
		if img.SourcePage > 0 {
			caption = fmt.Sprintf("PDF 第 %d 页原件", img.SourcePage)
		}
		start := len([]rune(content[:at+2]))
		info = append(info, types.ImageInfo{URL: img.ServingURL, OriginalURL: img.ServingURL, Caption: caption, StartPos: start, EndPos: start + len([]rune(img.ServingURL))})
	}
	if len(info) == 0 {
		return ""
	}
	raw, _ := json.Marshal(info)
	return string(raw)
}
