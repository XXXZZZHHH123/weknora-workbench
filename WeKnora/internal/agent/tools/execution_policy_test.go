package tools

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStructuredReadConcurrencyUsesExactProtocolOperation(t *testing.T) {
	for _, operation := range []string{"describe_tools", "list_datasets", "list_series", "list_charts", "query_series", "analyze_table", "inspect_source", "get_result_evidence"} {
		t.Run(operation, func(t *testing.T) {
			require.True(t, CanRunToolCallConcurrently(ToolStructuredData, fmt.Sprintf(`{"operation":%q,"arguments":{}}`, operation)))
		})
	}
	for _, operation := range []string{"resolve_fields", "confirm_fields", "execute_field_plan", "compare_series", "convert_price", "update", "list_datasets_unknown"} {
		t.Run(operation, func(t *testing.T) {
			require.False(t, CanRunToolCallConcurrently(ToolStructuredData, fmt.Sprintf(`{"operation":%q,"arguments":{}}`, operation)))
		})
	}
	for _, input := range []string{`invalid`, `null`, `[]`, `{}`, `{"operation":"list_datasets"}`, `{"operation":"list_datasets","arguments":null}`, `{"operation":"list_datasets","arguments":{},"tenant_id":99}`} {
		require.False(t, CanRunToolCallConcurrently(ToolStructuredData, input), input)
	}
}

func TestTypedStructuredReadsOverlapButUnknownOrStatefulToolsRemainBarriers(t *testing.T) {
	for _, tool := range []string{"structured_list_series", "structured_query_series", "structured_analyze_table", "structured_inspect_source", "structured_get_result_evidence", ToolSearchKnowledge} {
		require.True(t, CanRunToolCallConcurrently(tool, `{}`), tool)
	}
	for _, tool := range []string{"structured_resolve_fields", "structured_confirm_fields", "structured_update", "external_read", ToolBindDocumentScope, ToolShellExec, ToolWriteSandboxFile} {
		require.False(t, CanRunToolCallConcurrently(tool, `{}`), tool)
	}
	require.False(t, CanRunConcurrently(ToolStructuredData), "a multipurpose gateway without arguments is not a read capability")
}
