package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

// NewStructuredReadOperationTools retains its legacy API name while exposing
// permitted data reads and guarded resolution-state operations from the real
// service schema. It never exposes import/publication mutations, contacts a
// service during registration or infers schemas from names/examples.
func NewStructuredReadOperationTools(base *StructuredDataTool, data map[string]any) []types.Tool {
	if base == nil {
		return nil
	}
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
		descriptor, supported := structuredDescriptor(name, "structured_"+name)
		if !supported || name == "describe_tools" || counts[name] != 1 {
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
		if base.scopeConfigured {
			if _, exists := properties["knowledge_base_id"]; exists {
				continue
			}
			// Copy the server schema before adding the client-only selector;
			// discovery metadata and the service operation contract stay intact.
			var scopedSchema map[string]any
			if json.Unmarshal(raw, &scopedSchema) != nil {
				continue
			}
			scopedSchema["properties"].(map[string]any)["knowledge_base_id"] = map[string]any{
				"type": "string", "description": "本轮已授权知识库ID；单库默认，多库必填且须从真实授权目录选择",
			}
			if len(base.knowledgeScopes) > 1 && descriptor.ScopeMode == "knowledge_base" {
				required, _ := scopedSchema["required"].([]any)
				scopedSchema["required"] = append(required, "knowledge_base_id")
			}
			raw, err = json.Marshal(scopedSchema)
			if err != nil || len(raw) > 64*1024 {
				continue
			}
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
			descriptor: func() StructuredOperationDescriptor {
				descriptor.SchemaSHA256 = structuredDigest(json.RawMessage(raw))
				return descriptor
			}(),
		})
	}
	return result
}

type structuredReadOperationTool struct {
	BaseTool
	base         *StructuredDataTool
	operation    string
	argumentKeys map[string]bool
	descriptor   StructuredOperationDescriptor
	generation   uint64
}

func (t *structuredReadOperationTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	// Hold this generation through transport. Capability refresh is an execution
	// barrier and cannot change the interpretation beneath an in-flight read.
	t.base.protocolMu.RLock()
	defer t.base.protocolMu.RUnlock()
	executionDescriptor := t.descriptor
	if t.generation > 0 {
		if t.generation != t.base.protocolGeneration {
			return structuredProtocolFailure("manifest_changed", "该工具定义已过期；请查看当前 structured_capabilities，按本轮真实扁平 schema 修正后重试", "manifest", false), nil
		}
	} else if t.base.protocolGeneration > 0 {
		current, available := t.base.protocolDescriptors[t.operation]
		if !available || current.SchemaSHA256 != t.descriptor.SchemaSHA256 {
			return structuredProtocolFailure("manifest_changed", "保留的同名工具定义与本轮真实服务协议不一致；未执行，不会覆盖其它注册者。请核对当前能力冲突", "manifest", false), nil
		}
		executionDescriptor = current
	}
	var input map[string]json.RawMessage
	if len(args) > 16384 || json.Unmarshal(args, &input) != nil || input == nil {
		return nil, fmt.Errorf("结构化表格参数需要符合工具schema的对象")
	}
	for key := range input {
		if key == "knowledge_base_id" && t.base.scopeConfigured {
			continue
		}
		if !t.argumentKeys[key] {
			return nil, fmt.Errorf("结构化表格不接受未声明参数 %q", key)
		}
	}
	// query_series has its own aggregation argument named operation. It stays
	// inside arguments; the signed HTTP operation is always bound by this tool.
	envelope := map[string]any{"operation": t.operation, "arguments": input}
	if selected, exists := input["knowledge_base_id"]; exists && t.base.scopeConfigured {
		delete(input, "knowledge_base_id")
		envelope["knowledge_base_id"] = selected
	}
	wrapped, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("结构化表格参数格式无效")
	}
	result, err := t.base.Execute(withStructuredInvocation(ctx, executionDescriptor), wrapped)
	if result != nil {
		if result.Data == nil {
			result.Data = map[string]any{}
		}
		result.Data["_structured_execution"] = executionDescriptor
		attachStructuredDiagnosticDescriptor(result, executionDescriptor)
	}
	return result, err
}

func attachStructuredDiagnosticDescriptor(result *types.ToolResult, descriptor StructuredOperationDescriptor) {
	if result == nil || result.Data == nil || (result.Data["kind"] != "structured_protocol_error" && result.Data["status"] != "scope_selection_required") {
		return
	}
	result.Data["_structured_execution"] = descriptor
	result.Data["manifest_version"], result.Data["schema_sha256"] = descriptor.ManifestSHA256, descriptor.SchemaSHA256
	result.Data["protocol_version"], result.Data["generation"] = descriptor.ProtocolVersion, descriptor.Generation
	encoded, _ := json.Marshal(result.Data)
	result.Output = string(encoded)
}
