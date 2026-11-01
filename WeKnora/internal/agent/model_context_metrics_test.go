package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestMeasureModelContextCountsWireBytesRolesAndOmittedDefinitions(t *testing.T) {
	messages := []chat.Message{
		{Role: "system", Content: "system fixture"},
		{Role: "user", Content: "private fixture text"},
		{Role: "tool", Name: "catalog", Content: `{"status":"ok"}`},
	}
	kept := chat.Tool{Type: "function", Function: chat.FunctionDef{
		Name: "query", Description: "read exact rows", Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	omitted := chat.Tool{Type: "function", Function: chat.FunctionDef{
		Name: "unused", Description: "not exposed", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}
	options := &chat.ChatOptions{Tools: []chat.Tool{kept}}
	metrics := measureModelContext(messages, options, []chat.Tool{kept, omitted})

	wantMessages, err := json.Marshal(messages)
	require.NoError(t, err)
	wantProjected, err := json.Marshal(options.Tools)
	require.NoError(t, err)
	wantOmitted, err := json.Marshal(omitted.Function)
	require.NoError(t, err)
	wantFullKept, err := json.Marshal(kept.Function)
	require.NoError(t, err)

	require.Equal(t, len(wantMessages), metrics.MessageJSONBytes)
	require.Equal(t, len(wantProjected), metrics.ToolSchemaJSONBytes)
	require.Equal(t, len(wantOmitted), metrics.ProjectedToolSchemaOmittedJSONBytes)
	require.Equal(t, len(wantOmitted)+len(wantFullKept), metrics.FullRegistryToolSchemaJSONBytes)
	require.Equal(t, len(wantMessages)+len(wantProjected), metrics.MessagesAndToolsJSONBytes)
	require.Equal(t, map[string]int{"system": 1, "user": 1, "tool": 1}, metrics.MessageRoleCounts)
	require.Equal(t, 1, metrics.ProjectedToolCount)
	require.Equal(t, 2, metrics.FullRegistryToolCount)
	require.Equal(t, 1, metrics.ToolCount)

	metadata, err := json.Marshal(metrics)
	require.NoError(t, err)
	require.NotContains(t, string(metadata), "private fixture text", "metrics must not retain message content")
	require.NotContains(t, string(metadata), "read exact rows", "metrics must not retain tool descriptions")
}
