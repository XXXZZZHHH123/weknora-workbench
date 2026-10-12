package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

// NewStructuredReadOperationTools exposes only the read operations described by
// the configured service. Registration parses an already fetched directory;
// it never contacts a service or infers schemas from names or examples.
func NewStructuredReadOperationTools(base *StructuredDataTool, data map[string]any) []types.Tool {
	if base == nil {
		return nil
	}
	allowed := map[string]bool{"list_series": true, "query_series": true, "analyze_table": true, "inspect_source": true, "get_result_evidence": true}
	directory, ok := data["tools"].([]any)
	if !ok {
		return nil
	}
	// An ambiguous duplicate entry must not choose which schema the model sees.
	counts := map[string]int{}
	for _, item := range directory {
		if entry, ok := item.(map[string]any); ok {
			if name, ok := entry["name"].(string); ok {
				counts[name]++
			}
		}
	}
	var result []types.Tool
	for _, item := range directory {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if !allowed[name] || counts[name] != 1 {
			continue
		}
		schema, ok := entry["inputSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		raw, err := json.Marshal(schema)
		if err != nil || len(raw) > 64*1024 {
			continue
		}
		description, _ := entry["description"].(string)
		if len(description) > 4096 {
			description = description[:4096]
		}
		keys := make(map[string]bool, len(properties))
		for key := range properties {
			keys[key] = true
		}
		result = append(result, &structuredReadOperationTool{
			BaseTool: NewBaseTool("structured_"+name, description, raw), base: base,
			operation: name, argumentKeys: keys,
		})
	}
	return result
}

type structuredReadOperationTool struct {
	BaseTool
	base         *StructuredDataTool
	operation    string
	argumentKeys map[string]bool
}

func (t *structuredReadOperationTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var input map[string]json.RawMessage
	if len(args) > 16384 || json.Unmarshal(args, &input) != nil || input == nil {
		return nil, fmt.Errorf("结构化表格参数需要符合工具schema的对象")
	}
	for key := range input {
		if !t.argumentKeys[key] {
			return nil, fmt.Errorf("结构化表格不接受未声明参数 %q", key)
		}
	}
	// query_series has its own aggregation argument named operation. It stays
	// inside arguments; the signed HTTP operation is always bound by this tool.
	wrapped, err := json.Marshal(map[string]any{"operation": t.operation, "arguments": input})
	if err != nil {
		return nil, fmt.Errorf("结构化表格参数格式无效")
	}
	return t.base.Execute(ctx, wrapped)
}
