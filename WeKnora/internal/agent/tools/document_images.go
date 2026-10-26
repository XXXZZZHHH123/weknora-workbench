package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// The callback verifies an explicit resource binding to this authorized document.
// Markdown is untrusted navigation evidence, never permission to read arbitrary URLs.
type DocumentImageReader func(context.Context, *types.Knowledge, string) ([]byte, error)
type DocumentSourceImageReader func(context.Context, *types.Knowledge, string, int, string) ([]byte, error)
type DocumentFigureReader func(context.Context, *types.Knowledge, string, int, string, []string) (map[string]any, error)

func (t *ReadDocumentTool) WithFigureReader(reader DocumentFigureReader) *ReadDocumentTool {
	t.figureReader = reader
	return t
}

func (t *ReadDocumentTool) WithSourceImageReader(reader DocumentSourceImageReader) *ReadDocumentTool {
	t.sourceImageReader = reader
	return t
}

const documentImageEvidenceVersion = "document-original-image-v1"

var documentMarkdownImage = regexp.MustCompile(`!\[([^\]\r\n]*)\]\((resource://[A-Za-z0-9_-]+)\)`)

// This is the parser's explicit source-locator wire format, never query text.
var documentFigureSource = regexp.MustCompile(`(?m)^来源：第([1-9][0-9]*)页 · (ppt/media/[A-Za-z0-9_.-]+\.[eE][mM][fF])（`)
var documentFigurePart = regexp.MustCompile(`(?i)^ppt/media/[A-Za-z0-9_.-]+\.(emf|wmf)$`)

type documentImage struct {
	Reference   string `json:"reference"`
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
	Page        int    `json:"page,omitempty"`
	Label       string `json:"label"`
	Context     string `json:"source_context"`
	SourcePart  string `json:"source_part,omitempty"`
}

func documentImageCatalog(result *types.ToolResult, knowledgeID, sourceHash string) []documentImage {
	rows, _ := result.Data["chunks"].([]map[string]interface{})
	seen := map[string]bool{}
	images := []documentImage{}
	page, _ := result.Data["page"].(int)
	for _, row := range rows {
		content, _ := row["content"].(string)
		chunkID, _ := row["chunk_id"].(string)
		var sources []types.DocumentImageSource
		sourceJSON, _ := json.Marshal(row["image_sources"])
		_ = json.Unmarshal(sourceJSON, &sources)
		sourcePart, sourcePage := "", page
		locators := documentFigureSource.FindAllStringSubmatch(content, -1)
		if len(locators) == 1 && len(documentMarkdownImage.FindAllString(content, -1)) == 1 {
			parsedPage, _ := strconv.Atoi(locators[0][1])
			if page == 0 || page == parsedPage {
				sourcePart, sourcePage = locators[0][2], parsedPage
			}
		}
		if chunkID == "" {
			chunkID, _ = row["id"].(string)
		}
		add := func(reference, label string) {
			if _, valid := types.ParseResourcePath(reference); !valid || seen[reference] {
				return
			}
			seen[reference] = true
			context := []rune(content)
			if len(context) > 1200 {
				context = context[:1200]
			}
			item := documentImage{Reference: reference, KnowledgeID: knowledgeID,
				ChunkID: chunkID, Page: sourcePage, Label: label, Context: string(context), SourcePart: sourcePart}
			if len(sources) > 0 {
				// Structured parser relationships survive split locator lines and
				// multiple images per chunk. Never fall back to prose for a stale
				// or ambiguous structured relationship.
				item.SourcePart = ""
				var matched *types.DocumentImageSource
				ambiguous := false
				for i := range sources {
					s := &sources[i]
					if s.Reference != reference {
						continue
					}
					if page > 0 && page != s.Page {
						continue
					}
					if sourceHash == "" || s.SourceHash != sourceHash || s.Page < 1 {
						ambiguous = true
						continue
					}
					if matched != nil && (matched.Page != s.Page || matched.SourcePart != s.SourcePart) {
						ambiguous = true
					}
					matched = s
				}
				if matched != nil && !ambiguous {
					item.Page = matched.Page
					if documentFigurePart.MatchString(matched.SourcePart) {
						item.SourcePart = matched.SourcePart
					}
				}
			}
			images = append(images, item)
		}
		for _, match := range documentMarkdownImage.FindAllStringSubmatch(content, -1) {
			add(match[2], match[1])
		}
		encoded, _ := json.Marshal(row["images"])
		var infos []types.ImageInfo
		if json.Unmarshal(encoded, &infos) == nil {
			for _, info := range infos {
				ref := info.URL
				if ref == "" {
					ref = info.OriginalURL
				}
				add(ref, info.Caption)
			}
		}
	}
	return images
}

func (t *ReadDocumentTool) finishImageRead(ctx context.Context, knowledge *types.Knowledge, input ReadDocumentInput, result *types.ToolResult, err error) (*types.ToolResult, error) {
	if err != nil || result == nil || !result.Success || result.Data == nil {
		return result, err
	}
	catalog := documentImageCatalog(result, knowledge.ID, knowledge.FileHash)
	result.Data["image_catalog"] = catalog
	if len(catalog) > 0 {
		encoded, _ := json.Marshal(catalog)
		result.Output += "\n<document_image_catalog untrusted_source_metadata=\"true\">" + string(encoded) + "</document_image_catalog>\n" +
			"These are stored originals, not yet viewed. Select the relevant reference using the full question and actual source context; " +
			"call read_document with images, detail=full and focus to inspect it. Each image retains its document, page and cited chunk."
	}
	if len(input.Images) == 0 {
		return result, nil
	}
	if len(input.Images) > 4 {
		return &types.ToolResult{Success: false, Error: "最多一次读取4张原图；请从当前页图片清单选择。"}, nil
	}
	if len(input.ImageSeries) > 32 {
		return &types.ToolResult{Success: false, Error: "所选图例数量超出范围；请使用原图中的真实图例标识。"}, nil
	}
	selected := []documentImage{}
	seen := map[string]bool{}
	for _, ref := range input.Images {
		found := false
		for _, item := range catalog {
			if item.Reference == ref {
				found = true
				if !seen[ref] {
					selected = append(selected, item)
					seen[ref] = true
				}
				break
			}
		}
		if !found {
			return &types.ToolResult{Success: false, Error: "所选图片不属于本次实际读取的文档/页/分块范围；请先读取对应范围的真实图片清单。"}, nil
		}
	}
	if t.imageReader == nil && t.sourceImageReader == nil {
		result.Data["image_read_error"] = "原图读取服务不可用"
		result.Output += "\nOriginal image reader is unavailable; image labels alone cannot establish visual facts."
		return result, nil
	}
	detail := input.Detail
	if detail == "" {
		detail = types.ImageDetailFull
	}
	result.ImageDetail, result.ImageFocus = types.NormalizeImageRead(detail, input.Focus)
	evidence := []map[string]interface{}{}
	geometryEvidence := []map[string]any{}
	totalBytes := 0
	for _, item := range selected {
		var data []byte
		var readErr error
		if t.sourceImageReader != nil && item.SourcePart != "" {
			data, readErr = t.sourceImageReader(ctx, knowledge, item.Reference, item.Page, item.SourcePart)
		} else if t.imageReader != nil {
			data, readErr = t.imageReader(ctx, knowledge, item.Reference)
		} else {
			readErr = fmt.Errorf("original image reader unavailable")
		}
		entry := map[string]interface{}{"reference": item.Reference, "knowledge_id": knowledge.ID,
			"chunk_id": item.ChunkID, "page": item.Page, "version": documentImageEvidenceVersion, "read_success": false}
		if readErr != nil {
			entry["error"] = "原图未通过归属/存储读取校验"
			evidence = append(evidence, entry)
			continue
		}
		mime := http.DetectContentType(data)
		totalBytes += len(data)
		if len(data) == 0 || !strings.HasPrefix(mime, "image/") || totalBytes > 32<<20 {
			entry["error"] = "原图格式或总大小未通过校验"
			evidence = append(evidence, entry)
			continue
		}
		digest := sha256.Sum256(data)
		entry["sha256"] = hex.EncodeToString(digest[:])
		entry["mime_type"], entry["read_success"] = mime, true
		entry["returned_image_index"] = len(result.Images)
		result.Images = append(result.Images, fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)))
		evidence = append(evidence, entry)
		if t.figureReader != nil && item.SourcePart != "" {
			geometry, geometryErr := t.figureReader(ctx, knowledge, item.Reference, item.Page, item.SourcePart, input.ImageSeries)
			if geometryErr != nil {
				entry["geometry_error"] = "原图几何证据不可用，不能据此生成数值"
			} else if geometry != nil && geometry["version"] == "source_figure_selection_options_v1" {
				options, _ := json.Marshal(geometry["available_series"])
				return &types.ToolResult{Success: false,
					Error: "image_series must explicitly select exact original legend identifiers, not the chart title or metric. Available original legend identifiers: " + string(options) + ". Choose the relevant identifiers from the full question and retry read_document; no numeric or visual evidence was delivered for this missing/invalid selection.",
					Data:  map[string]any{"knowledge_id": knowledge.ID, "image_catalog": catalog, "image_selection_options": geometry}}, nil
			} else if geometry != nil {
				geometryEvidence = append(geometryEvidence, map[string]any{"reference": item.Reference,
					"knowledge_id": knowledge.ID, "chunk_id": item.ChunkID, "page": item.Page,
					"source_part": item.SourcePart, "evidence": geometry})
				metadata := map[string]any{}
				for key, value := range geometry {
					if key != "geometry" && key != "markdown" {
						metadata[key] = value
					}
				}
				identity, _ := json.Marshal(metadata)
				result.Output += "\n<original_figure_geometry reference=\"" + item.Reference + "\" chunk_id=\"" + item.ChunkID + "\">\n" +
					string(identity) + "\n" + fmt.Sprint(geometry["markdown"]) + "\n</original_figure_geometry>\n"
			}
		}
	}
	result.Data["image_evidence"] = evidence
	result.Data["image_geometry_evidence"] = geometryEvidence
	encoded, _ := json.Marshal(evidence)
	result.Output += "\n<original_image_reads>" + string(encoded) + "</original_image_reads>\n" +
		"Original pixels returned here are passed to the image-capable model or separate vision reader. " +
		"A storage read alone is not visual understanding. Ground observations in this image's exact cited chunk, axis/legend mapping and units; " +
		"mark approximate values and ambiguity. Failed image reads supply no visual evidence."
	return result, nil
}
