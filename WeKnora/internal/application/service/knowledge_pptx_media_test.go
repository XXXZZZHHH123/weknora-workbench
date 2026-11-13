package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestPPTXMediaReceiptRetainsDeferredOriginalsAndOtherMetadata(t *testing.T) {
	k := &types.Knowledge{FileType: "pptx", FileHash: "current-source", Metadata: types.JSON(`{"process_overrides":{"summary_enabled":false}}`)}
	err := setPPTXMediaProcessingMetadata(k, map[string]string{pptxMediaProcessingKey: `{"slides":3,"vector_parts":4,"vector_previews":1,"deferred_vector_previews":2,"unavailable_vector_parts":1,"preview_status":"ready","source_hash":"untrusted","extra":"ignored"}`})
	require.NoError(t, err)
	require.JSONEq(t, `{"process_overrides":{"summary_enabled":false},"pptx_media_processing_v1":{"slides":3,"vector_parts":4,"vector_previews":1,"deferred_vector_previews":2,"unavailable_vector_parts":1,"preview_status":"unavailable","source_hash":"current-source"}}`, string(k.Metadata))
	// Same row reused by a successful parse of a replacement file must not
	// retain counts from the previous source or an unsupported reader version.
	k.FileType = "pdf"
	require.NoError(t, setPPTXMediaProcessingMetadata(k, nil))
	require.JSONEq(t, `{"process_overrides":{"summary_enabled":false}}`, string(k.Metadata))
}

func TestPPTXMediaReceiptRejectsMalformedOrInconsistentCounts(t *testing.T) {
	for _, raw := range []string{
		`garbage`,
		`{"slides":1,"vector_parts":1,"vector_previews":-1,"deferred_vector_previews":2}`,
		`{"slides":1,"vector_parts":2,"vector_previews":1}`,
		`{"slides":1,"vector_parts":1,"vector_previews":0.5,"deferred_vector_previews":0.5}`,
		`{"slides":1001,"vector_parts":0}`,
	} {
		k := &types.Knowledge{FileType: "pptx", FileHash: "source", Metadata: types.JSON(`{"existing":true}`)}
		require.Error(t, setPPTXMediaProcessingMetadata(k, map[string]string{pptxMediaProcessingKey: raw}))
		require.JSONEq(t, `{"existing":true}`, string(k.Metadata))
	}
}
