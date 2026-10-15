package tools

import "encoding/json"

// CanRunConcurrently is an explicit allowlist of built-in reads. Mutations,
// arbitrary code, and unknown/MCP tools form execution barriers. In particular,
// a file write must finish before the next shell or skill call starts.
func CanRunConcurrently(name string) bool {
	switch name {
	case ToolSearchKnowledge, ToolReadDocument, ToolListDocuments,
		ToolQueryKnowledgeGraph, ToolSearchConversations,
		ToolSearchMemory, ToolDataSchema, ToolWebSearch, ToolWebFetch,
		ToolReadFile, ToolListSandboxFiles,
		ToolWikiSearch, ToolWikiReadPage, ToolWikiReadIssue,
		"structured_list_series", "structured_query_series", "structured_analyze_table",
		"structured_inspect_source", "structured_get_result_evidence":
		return true
	default:
		return false
	}
}

// The structured gateway also exposes stateful resolution/confirmation, so
// its name alone cannot grant concurrency. Only explicit read-only protocol
// operations overlap. Each call still executes its ordinary authorization,
// parameter validation and read-only SQL/source checks.
func CanRunToolCallConcurrently(name, arguments string) bool {
	if name != ToolStructuredData {
		return CanRunConcurrently(name)
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
		return false
	}
	for key := range input {
		if key != "operation" && key != "arguments" && key != "knowledge_base_id" {
			return false
		}
	}
	var operation string
	var params map[string]json.RawMessage
	if json.Unmarshal(input["operation"], &operation) != nil || json.Unmarshal(input["arguments"], &params) != nil || params == nil {
		return false
	}
	switch operation {
	case "describe_tools", "list_datasets", "list_series", "list_charts",
		"query_series", "analyze_table", "inspect_source", "get_result_evidence":
		return true
	default:
		return false
	}
}
