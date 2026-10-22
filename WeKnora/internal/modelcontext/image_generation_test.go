package modelcontext

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationUsesResourceHandlesWithoutSourceEvidence(t *testing.T) {
	registry := NewRegistry(true)
	require.True(t, HasToolPolicy("generate_image"))
	const ref = "resource://AbCdEfGhIjKlMnOpQrStUv"
	result := &types.ToolResult{Success: true, Output: "Generated image: ![preview](" + ref + ")", Data: map[string]interface{}{"display_type": "image_generation", "status": "completed"}}
	output := registry.ModelToolResultForTool("generate_image", result)
	require.Contains(t, output, "res://0001")
	require.NotContains(t, output, ref)
	require.Contains(t, registry.DecodeOutputText(output), ref)
}
