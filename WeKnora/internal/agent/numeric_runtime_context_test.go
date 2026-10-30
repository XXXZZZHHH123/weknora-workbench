package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDateAndRoutingNumbersCannotGroundAnAnswer(t *testing.T) {
	metadata := `<runtime_context scope="this_turn"><current_time>2031-07-23</current_time><document chunk_count="734" /></runtime_context>`
	evidence := chat.Message{Role: "tool", Content: `{"status":"ok","aggregate":11.25}`}
	messages := []chat.Message{evidence, {Role: "user", Content: metadata + "\n\nReturn the observed total."}}
	require.Equal(t, []string{"23", "734"}, ungroundedStructuredNumbers("Total 23; observations 734.", messages))
	require.Empty(t, ungroundedStructuredNumbers("Total 11.25.", messages))

	// Actual human-supplied numbers keep their existing numeric semantics.
	messages[1].Content = metadata + "\n\n<must_use>Use skill 876.</must_use>\n\nCompare with my supplied value 23."
	require.Empty(t, ungroundedStructuredNumbers("Supplied value 23.", messages))
	require.Equal(t, []string{"876"}, ungroundedStructuredNumbers("Observed value 876.", messages))
}
