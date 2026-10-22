package tools

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationCapabilityDoesNotConsumeFilesOrRequireKnowledgeBase(t *testing.T) {
	requirement, known := ToolCapabilityRequirements[ToolGenerateImage]
	require.True(t, known, "known text-to-image tool must not take the permissive unknown-tool fallback")
	require.Empty(t, requirement.AnyOf)
	require.Empty(t, requirement.AllOf)
	require.False(t, requirement.ConsumesFiles)
	require.False(t, ToolsConsumeFiles([]string{ToolGenerateImage}))
	require.False(t, ToolsConsumeFiles([]string{ToolGenerateImage, ToolThinking}))
	require.True(t, DeriveKBFilterFromTools([]string{ToolGenerateImage}).IsEmpty())
	require.True(t, KBSatisfiesAgentRequirements(types.KBCapabilities{}, types.AgentModeSmartReasoning, []string{ToolGenerateImage}))
	require.True(t, ToolsConsumeFiles([]string{ToolGenerateImage, ToolReadDocument}), "document access still consumes file references")
	require.True(t, ToolsConsumeFiles([]string{"custom_mcp_tool"}), "unknown tools retain their permissive fallback")
}
