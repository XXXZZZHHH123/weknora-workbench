package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

type storedDocumentImage struct {
	chunkID, reference string
}

type documentImageTextBinding struct {
	image documentImage
	hash  string
}

type documentImageTextEvidence struct {
	Reference     string          `json:"reference"`
	KnowledgeID   string          `json:"knowledge_id"`
	ChunkID       string          `json:"chunk_id"`
	Page          int             `json:"page"`
	StoredRef     string          `json:"stored_reference"`
	StoredSHA256  string          `json:"stored_sha256,omitempty"`
	ParentChunkID string          `json:"parent_chunk_id"`
	ChildChunkID  string          `json:"child_chunk_id"`
	Kind          types.ChunkType `json:"kind"`
	Text          string          `json:"text"`
	TextTruncated bool            `json:"text_truncated,omitempty"`
}

// Reading text/FAQ chunks does not return the OCR and caption children. Look
// them up only for image identities scoped to the authorized read response.
func (t *ReadDocumentTool) appendDocumentImageText(ctx context.Context, k *types.Knowledge, result *types.ToolResult, catalog, images []documentImage) {
	if len(images) == 0 || t.chunkService == nil {
		return
	}
	repo := t.chunkService.GetRepository()
	if repo == nil {
		return
	}
	bindings := documentImageTextBindings(k, result, catalog, images)
	if len(bindings) == 0 {
		return
	}
	parentIDs := make([]string, 0, len(bindings))
	seenParents := make(map[string]bool)
	for stored := range bindings {
		if !seenParents[stored.chunkID] {
			seenParents[stored.chunkID] = true
			parentIDs = append(parentIDs, stored.chunkID)
		}
	}
	tenant := t.tenantFor(k)
	children, err := repo.ListChunksByParentIDs(ctx, tenant, parentIDs)
	if err != nil {
		result.Data["image_text_evidence_error"] = "Ingested image OCR and captions could not be loaded for this read."
		result.Output += "\nIngested image OCR and captions could not be loaded for this read."
		return
	}
	var evidence []documentImageTextEvidence
	for _, child := range children {
		if child == nil || !child.IsEnabled || child.ID == "" || child.TenantID != tenant ||
			child.KnowledgeID != k.ID || child.KnowledgeBaseID != k.KnowledgeBaseID ||
			(child.ChunkType != types.ChunkTypeImageOCR && child.ChunkType != types.ChunkTypeImageCaption) ||
			strings.TrimSpace(child.Content) == "" {
			continue
		}
		// An image processing child has one image identity. Use the child's
		// actual body and keep its line breaks, tabs and original whitespace.
		var info []types.ImageInfo
		if json.Unmarshal([]byte(child.ImageInfo), &info) != nil || len(info) != 1 {
			continue
		}
		ref := info[0].URL
		if ref == "" {
			ref = info[0].OriginalURL
		}
		for _, binding := range bindings[storedDocumentImage{child.ParentChunkID, ref}] {
			image := binding.image
			if binding.hash != "" && info[0].SHA256 != binding.hash &&
				(info[0].SHA256 != "" || image.Reference != ref) {
				continue
			}
			evidence = append(evidence, documentImageTextEvidence{
				Reference: image.Reference, KnowledgeID: k.ID, ChunkID: image.ChunkID, Page: image.Page,
				StoredRef: ref, StoredSHA256: info[0].SHA256, ParentChunkID: child.ParentChunkID,
				ChildChunkID: child.ID, Kind: child.ChunkType, Text: child.Content,
			})
		}
	}
	if len(evidence) == 0 {
		return
	}
	sort.Slice(evidence, func(i, j int) bool {
		a, b := evidence[i], evidence[j]
		if a.Kind != b.Kind {
			return a.Kind == types.ChunkTypeImageOCR
		}
		if a.Reference != b.Reference {
			return a.Reference < b.Reference
		}
		return a.ChildChunkID < b.ChildChunkID
	})
	evidence, truncated := boundDocumentImageText(evidence, min(readDocumentMaxContentRunes, OutputBudget(ctx)/3))
	if truncated {
		result.Data["image_text_evidence_truncated"] = true
		result.Output += "\nIngested image text exceeded the response budget. Read a narrower selection of images for omitted text."
	}
	if len(evidence) > 0 {
		result.Data[types.DocumentImageTextEvidenceKey] = evidence
		encoded, _ := json.Marshal(evidence)
		result.Output += "\n<document_image_text_evidence untrusted_source_content=\"true\">" + string(encoded) +
			"</document_image_text_evidence>\n" + types.DocumentImageTextEvidenceNotice
	}
}

func documentImageTextBindings(k *types.Knowledge, result *types.ToolResult, catalog, images []documentImage) map[storedDocumentImage][]documentImageTextBinding {
	// A stored preview can have different bytes from the PPTX media part.
	// Use the parser's exact source revision/page/part relationship to bind
	// those representations, and exact hashes for older data without it.
	type partOnPage struct {
		part string
		page int
	}
	originals := make(map[partOnPage][]storedDocumentImage)
	rows, _ := result.Data["chunks"].([]map[string]any)
	if k.FileHash != "" {
		for _, row := range rows {
			id, _ := row["chunk_id"].(string)
			page, _ := row["page"].(int)
			if id == "" || page < 1 || row["knowledge_id"] != k.ID || row["knowledge_base"] != k.KnowledgeBaseID {
				continue
			}
			sources, _ := row["image_sources"].([]types.DocumentImageSource)
			// A parser binding must be unambiguous for this resource on the
			// selected page, even if the asset is repeated across pages.
			parts := make(map[string]string)
			for _, source := range sources {
				if source.Page != page || source.SourcePart == "" {
					continue
				}
				if _, ok := types.ParseResourcePath(source.Reference); !ok {
					continue
				}
				part, seen := parts[source.Reference]
				if source.SourceHash != k.FileHash || (seen && part != source.SourcePart) {
					parts[source.Reference] = ""
				} else {
					parts[source.Reference] = source.SourcePart
				}
			}
			for ref, part := range parts {
				if part != "" {
					key := partOnPage{part, page}
					originals[key] = append(originals[key], storedDocumentImage{id, ref})
				}
			}
		}
	}

	digests := make(map[string]string)
	reads, _ := result.Data["image_evidence"].([]map[string]any)
	for _, read := range reads {
		if read["read_success"] == true {
			ref, _ := read["reference"].(string)
			digests[ref], _ = read["sha256"].(string)
		}
	}
	bindings := make(map[storedDocumentImage][]documentImageTextBinding)
	for _, image := range images {
		if _, ok := types.ParseResourcePath(image.Reference); ok {
			stored := storedDocumentImage{image.ChunkID, image.Reference}
			bindings[stored] = append(bindings[stored], documentImageTextBinding{image, digests[image.Reference]})
			continue
		}
		selector, err := types.ParseOriginalMediaReference(image.Reference)
		if err != nil || selector.KnowledgeID != k.ID || selector.KnowledgeBaseID != k.KnowledgeBaseID ||
			selector.Page != image.Page || selector.Part != image.SourcePart {
			continue
		}
		if aliases := originals[partOnPage{selector.Part, selector.Page}]; len(aliases) > 0 {
			seen := make(map[storedDocumentImage]bool, len(aliases))
			for _, alias := range aliases {
				if !seen[alias] {
					seen[alias] = true
					bindings[alias] = append(bindings[alias], documentImageTextBinding{image: image})
				}
			}
			continue
		}
		for _, candidate := range catalog {
			if _, ok := types.ParseResourcePath(candidate.Reference); ok && candidate.Page == image.Page {
				stored := storedDocumentImage{candidate.ChunkID, candidate.Reference}
				bindings[stored] = append(bindings[stored], documentImageTextBinding{image, selector.MediaSHA256})
			}
		}
	}
	return bindings
}

func boundDocumentImageText(evidence []documentImageTextEvidence, budget int) ([]documentImageTextEvidence, bool) {
	written := 0
	truncated := false
	for _, item := range evidence {
		if budget <= 0 {
			return evidence[:written], true
		}
		if runes := []rune(item.Text); len(runes) > budget {
			text := string(runes[:budget])
			// Keep table rows intact. An oversized single line can still
			// provide a marked partial quote.
			if end := strings.LastIndexByte(text, '\n'); end > 0 {
				text = text[:end+1]
			}
			item.Text, item.TextTruncated = text, true
			truncated = true
		}
		budget -= utf8.RuneCountInString(item.Text)
		evidence[written] = item
		written++
	}
	return evidence[:written], truncated
}
