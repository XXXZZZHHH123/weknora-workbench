package service

import (
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

const pptxMediaProcessingKey = "pptx_media_processing_v1"

// This receipt describes optional previews, independently of document indexing.
// Counts come only from the parser; no source text or client metadata is copied.
type pptxMediaProcessing struct {
	Slides                 int    `json:"slides"`
	VectorParts            int    `json:"vector_parts"`
	VectorPreviews         int    `json:"vector_previews"`
	DeferredVectorPreviews int    `json:"deferred_vector_previews"`
	UnavailableVectorParts int    `json:"unavailable_vector_parts"`
	PreviewStatus          string `json:"preview_status"`
	SourceHash             string `json:"source_hash"`
}

func setPPTXMediaProcessingMetadata(knowledge *types.Knowledge, parserMetadata map[string]string) error {
	if knowledge == nil {
		return nil
	}
	fields := map[string]json.RawMessage{}
	if len(knowledge.Metadata) > 0 {
		if err := json.Unmarshal(knowledge.Metadata, &fields); err != nil {
			return err
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	// A new successful parse supersedes the previous preview receipt, including
	// replacement by another file type or an older reader without this protocol.
	_, hadReceipt := fields[pptxMediaProcessingKey]
	if !hadReceipt && parserMetadata[pptxMediaProcessingKey] == "" {
		return nil
	}
	delete(fields, pptxMediaProcessingKey)
	cleared, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	knowledge.Metadata = types.JSON(cleared)
	if raw := parserMetadata[pptxMediaProcessingKey]; knowledge.FileType == "pptx" && knowledge.FileHash != "" && raw != "" {
		var receipt pptxMediaProcessing
		if len(raw) > 4096 || json.Unmarshal([]byte(raw), &receipt) != nil {
			return fmt.Errorf("invalid PPTX preview receipt")
		}
		if receipt.Slides < 1 || receipt.Slides > 1000 || receipt.VectorParts < 0 || receipt.VectorParts > 4096 ||
			receipt.VectorPreviews < 0 || receipt.DeferredVectorPreviews < 0 || receipt.UnavailableVectorParts < 0 ||
			receipt.VectorPreviews > receipt.VectorParts || receipt.DeferredVectorPreviews > receipt.VectorParts || receipt.UnavailableVectorParts > receipt.VectorParts ||
			receipt.VectorPreviews+receipt.DeferredVectorPreviews+receipt.UnavailableVectorParts != receipt.VectorParts {
			return fmt.Errorf("inconsistent PPTX preview counts")
		}
		receipt.PreviewStatus = "ready"
		if receipt.DeferredVectorPreviews > 0 {
			receipt.PreviewStatus = "deferred"
		}
		if receipt.UnavailableVectorParts > 0 {
			receipt.PreviewStatus = "unavailable"
		}
		receipt.SourceHash = knowledge.FileHash
		value, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		fields[pptxMediaProcessingKey] = value
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	knowledge.Metadata = types.JSON(encoded)
	return nil
}
