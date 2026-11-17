package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestContentReceiptPreservesIndependentResultsAndUnknownImageCount(t *testing.T) {
	knowledge := &types.Knowledge{FileHash: "source-version", Metadata: types.JSON(`{"_knowledge_ingestion":{"result":{"accepted_count":1}},"pages":9}`)}
	zero := 0
	require.NoError(t, setContentProcessingMetadata(knowledge, 3, &zero, false))
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(knowledge.Metadata, &metadata))
	counts := metadata[contentProcessingKey].(map[string]any)
	require.Equal(t, float64(3), counts["text_chunk_count"])
	require.Equal(t, float64(0), counts["image_count"])
	require.Equal(t, "source-version", counts["source_hash"])
	require.NotContains(t, counts, "complete")
	require.NotNil(t, metadata[ingestionMetadataKey])
	require.NoError(t, setContentProcessingMetadata(knowledge, 1, nil, true))
	require.NoError(t, json.Unmarshal(knowledge.Metadata, &metadata))
	counts = metadata[contentProcessingKey].(map[string]any)
	require.NotContains(t, counts, "image_count", "unknown must not inherit old counts")
	require.Equal(t, true, counts["text_is_catalog"])
	require.Error(t, setContentProcessingMetadata(knowledge, -1, nil, false))
}

func TestContentReceiptReplacementDiscardsOldSourceButKeepsNewIngestionGrant(t *testing.T) {
	knowledge := &types.Knowledge{FileHash: "replacement", Metadata: types.JSON(`{"_content_processing_v1":{"text_chunk_count":3},"_knowledge_ingestion":{"source_hash":"old-source","result":{"accepted_count":1}},"custom":true}`)}
	clearContentProcessingMetadata(knowledge)
	require.JSONEq(t, `{"custom":true}`, string(knowledge.Metadata))
	require.NoError(t, setIngestionState(knowledge, knowledgeIngestionState{Attempt: "new-attempt", SourceHash: "replacement", Status: "pending"}))
	clearContentProcessingMetadata(knowledge)
	state, err := ingestionState(knowledge)
	require.NoError(t, err)
	require.Equal(t, "new-attempt", state.Attempt)
}
