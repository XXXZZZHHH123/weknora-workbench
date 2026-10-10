package tools

import (
	"context"
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMCPStructuredConfirmationIsBoundToSourceService(t *testing.T) {
	registry := NewToolRegistry()
	add := func(serviceID, name string) string {
		tool := NewMCPTool(&types.MCPService{ID: serviceID, Name: serviceID}, &types.MCPTool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}, nil, nil, 0)
		registry.RegisterTool(tool)
		return tool.Name()
	}
	first := add("first", "resolve_fields")
	confirm := add("first", "confirm_fields")
	add("other", "confirm_fields")
	name, err := StructuredConfirmationTool(context.Background(), registry, first)
	require.NoError(t, err)
	require.Equal(t, confirm, name)
	_, err = StructuredConfirmationTool(context.Background(), registry, "")
	require.Error(t, err)
	_, err = StructuredConfirmationTool(context.Background(), registry, "unregistered")
	require.Error(t, err)
	foreign := add("foreign", "delete_data")
	_, err = StructuredConfirmationTool(context.Background(), registry, foreign)
	require.Error(t, err)
}
