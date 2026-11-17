package service

import (
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

const contentProcessingKey = "_content_processing_v1"

// Reprocessing replaces the old chunks; counts must not describe that previous
// attempt while the replacement is pending or has failed. Preserve a newly
// admitted ingestion grant, but discard receipts bound to a different original.
func clearContentProcessingMetadata(knowledge *types.Knowledge) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(knowledge.Metadata, &fields) != nil || fields == nil {
		return
	}
	delete(fields, contentProcessingKey)
	if state, err := ingestionState(knowledge); err == nil && state.SourceHash != knowledge.FileHash {
		delete(fields, ingestionMetadataKey)
	}
	if encoded, err := json.Marshal(fields); err == nil {
		knowledge.Metadata = types.JSON(encoded)
	}
}

// Counts describe persisted outputs, never source completeness. Text chunks
// are counted after writing them, rather than estimated from pages or tokens.
func setContentProcessingMetadata(knowledge *types.Knowledge, textChunks int, images *int, catalog bool) error {
	if textChunks < 0 || (images != nil && *images < 0) {
		return fmt.Errorf("negative content count")
	}
	fields := map[string]json.RawMessage{}
	if len(knowledge.Metadata) > 0 {
		if err := json.Unmarshal(knowledge.Metadata, &fields); err != nil {
			return err
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	receipt := struct {
		SourceHash string `json:"source_hash"`
		TextChunks int    `json:"text_chunk_count"`
		Images     *int   `json:"image_count,omitempty"`
		Catalog    bool   `json:"text_is_catalog"`
	}{knowledge.FileHash, textChunks, images, catalog}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	fields[contentProcessingKey] = raw
	encoded, err := json.Marshal(fields)
	if err == nil {
		knowledge.Metadata = types.JSON(encoded)
	}
	return err
}
