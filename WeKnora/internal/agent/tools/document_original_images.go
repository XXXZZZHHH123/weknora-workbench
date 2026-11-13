package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

type DocumentOriginalImageCatalog func(context.Context, *types.Knowledge, []int) ([]types.OriginalDocumentImage, error)

func (t *ReadDocumentTool) WithOriginalImageCatalog(reader DocumentOriginalImageCatalog) *ReadDocumentTool {
	t.originalImageCatalog = reader
	return t
}

// Only physical pages already returned by this authorized scoped read qualify.
// Never enumerate the full deck from a text search, guessed label, or offset.
func (t *ReadDocumentTool) appendOriginalImageCatalog(ctx context.Context, k *types.Knowledge, input ReadDocumentInput, result *types.ToolResult, catalog []documentImage) []documentImage {
	if t.originalImageCatalog == nil || !strings.EqualFold(strings.TrimPrefix(k.FileType, "."), "pptx") || (input.Page < 1 && len(input.Pages) == 0) {
		return catalog
	}
	rows, _ := result.Data["chunks"].([]map[string]interface{})
	owners := map[int]string{}
	contexts := map[int]string{}
	defaultPage, _ := result.Data["page"].(int)
	requested := map[int]bool{}
	if input.Page > 0 {
		requested[input.Page] = true
	}
	for _, p := range input.Pages {
		requested[p] = true
	}
	for _, row := range rows {
		page, _ := row["page"].(int)
		if page < 1 {
			page = defaultPage
		}
		if !requested[page] {
			continue
		}
		id, _ := row["chunk_id"].(string)
		if id == "" {
			id, _ = row["id"].(string)
		}
		if id == "" {
			continue
		}
		if owners[page] == "" {
			owners[page] = id
			content, _ := row["content"].(string)
			r := []rune(content)
			if len(r) > 1200 {
				r = r[:1200]
			}
			contexts[page] = string(r)
		}
	}
	pages := make([]int, 0, len(owners))
	for p := range owners {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	if len(pages) == 0 || len(pages) > 8 {
		return catalog
	}
	images, err := t.originalImageCatalog(ctx, k, pages)
	if err != nil {
		var limit *types.OriginalMediaCatalogLimitError
		if errors.As(err, &limit) {
			result.Data["original_image_catalog_total"] = limit.Total
			result.Data["original_image_catalog_limit"] = limit.Limit
			result.Data["original_image_catalog_truncated"] = true
		}
		result.Data["original_image_catalog_error"] = "Current original page relationships could not be verified; existing stored catalog is incomplete evidence, not proof that no other figures exist. Retry a single physical page if multiple pages were selected."
		result.Output += "\nOriginal media catalog unavailable: cannot infer that missing figures do not exist."
		return catalog
	}
	// Keep existing entries unchanged. Source selectors intentionally coexist
	// with stored resources when their exact original relationship is unknown.
	seen := map[string]bool{}
	for _, item := range catalog {
		seen[item.Reference] = true
	}
	complete := true
	for _, item := range images {
		sel, e := types.ParseOriginalMediaReference(item.Reference)
		if e != nil || sel.KnowledgeID != k.ID || sel.KnowledgeBaseID != k.KnowledgeBaseID || sel.Page != item.Page || sel.Part != item.Part || owners[item.Page] == "" {
			complete = false
			continue
		}
		if seen[item.Reference] {
			continue
		}
		seen[item.Reference] = true
		catalog = append(catalog, documentImage{Reference: item.Reference, KnowledgeID: k.ID, ChunkID: owners[item.Page], Page: item.Page, PageVerified: true, Label: item.Label, LabelTruncated: item.LabelTruncated, Context: contexts[item.Page], SourcePart: item.Part, ReadArgs: ReadDocumentInput{ID: k.ID, Page: item.Page, Images: []string{item.Reference}, ImageMode: "pixels", Detail: types.ImageDetailFull}})
	}
	if complete {
		result.Data["original_image_catalog_status"] = "verified_page_relationships"
		result.Data["original_image_catalog_pages"] = pages
		result.Data["original_image_catalog_complete"] = true
		result.Output += fmt.Sprintf("\nOriginal-media image catalog is complete only for verified physical pages %v; body text may still be paginated. No images have been viewed by listing this catalog. Choose the relevant figure from the full question and actual source context, then use its read_args directly; further text offsets are not required to complete these pages' original-image catalog. Catalog metadata is not numeric or visual evidence.\n", pages)
	}
	return catalog
}
