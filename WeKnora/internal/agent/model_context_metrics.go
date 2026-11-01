package agent

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
)

// modelContextMetrics contains sizes and protocol shape only. It deliberately
// never records message text, tool descriptions, argument values, or hashes of
// user/source content.
type modelContextMetrics struct {
	MessageCount                        int            `json:"message_count"`
	MessageJSONBytes                    int            `json:"message_json_bytes"`
	MessageRoleCounts                   map[string]int `json:"message_role_counts"`
	ToolSchemaJSONBytes                 int            `json:"tool_schema_json_bytes"`
	FullRegistryToolSchemaJSONBytes     int            `json:"full_registry_tool_schema_json_bytes"`
	ProjectedToolSchemaOmittedJSONBytes int            `json:"projected_tool_schema_omitted_json_bytes"`
	ToolCount                           int            `json:"tool_count"`
	FullRegistryToolCount               int            `json:"full_registry_tool_count"`
	ProjectedToolCount                  int            `json:"projected_tool_count"`
	MessagesAndToolsJSONBytes           int            `json:"messages_and_tools_json_bytes"`
}

func measureModelContext(messages []chat.Message, options *chat.ChatOptions, fullTools []chat.Tool) modelContextMetrics {
	metrics := modelContextMetrics{MessageCount: len(messages), MessageRoleCounts: map[string]int{}}
	messageJSON, err := json.Marshal(messages)
	if err == nil {
		metrics.MessageJSONBytes = len(messageJSON)
	}
	for _, message := range messages {
		role := message.Role
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			role = "other"
		}
		metrics.MessageRoleCounts[role]++
	}

	var projected []chat.Tool
	if options != nil {
		projected = options.Tools
	}
	toolJSON, err := json.Marshal(projected)
	if err == nil {
		metrics.ToolSchemaJSONBytes = len(toolJSON)
	}
	metrics.ToolCount = len(projected)
	metrics.ProjectedToolCount = len(projected)

	projectedByName := make(map[string]chat.Tool, len(projected))
	for _, tool := range projected {
		projectedByName[tool.Function.Name] = tool
	}
	metrics.FullRegistryToolCount = len(fullTools)
	for _, tool := range fullTools {
		encoded, marshalErr := json.Marshal(tool.Function)
		if marshalErr == nil {
			metrics.FullRegistryToolSchemaJSONBytes += len(encoded)
		}
		if _, included := projectedByName[tool.Function.Name]; !included {
			// Count the exact serialized registry function definition omitted by
			// the model-facing tool projection. Included definitions are not
			// considered omitted even when a provider codec rewrites an enum.
			metrics.ProjectedToolSchemaOmittedJSONBytes += len(encoded)
		}
	}
	metrics.MessagesAndToolsJSONBytes = metrics.MessageJSONBytes + metrics.ToolSchemaJSONBytes
	return metrics
}

func registeredToolSchemasForMetrics(registry interface {
	GetModelFunctionDefinitions() []types.FunctionDefinition
}) []chat.Tool {
	if registry == nil {
		return nil
	}
	definitions := registry.GetModelFunctionDefinitions()
	tools := make([]chat.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, chat.Tool{Type: "function", Function: chat.FunctionDef{
			Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
		}})
	}
	return tools
}

func beginModelContextObservation(ctx context.Context, stage string, messages []chat.Message, options *chat.ChatOptions, fullTools []chat.Tool) (context.Context, *langfuse.Span) {
	manager := langfuse.GetManager()
	if !manager.Enabled() {
		return ctx, nil
	}
	metrics := measureModelContext(messages, options, fullTools)
	metadata := map[string]interface{}{
		"stage":                                    stage,
		"message_count":                            metrics.MessageCount,
		"message_json_bytes":                       metrics.MessageJSONBytes,
		"message_role_counts":                      metrics.MessageRoleCounts,
		"tool_schema_json_bytes":                   metrics.ToolSchemaJSONBytes,
		"full_registry_tool_schema_json_bytes":     metrics.FullRegistryToolSchemaJSONBytes,
		"projected_tool_schema_omitted_json_bytes": metrics.ProjectedToolSchemaOmittedJSONBytes,
		"tool_count":                               metrics.ToolCount,
		"full_registry_tool_count":                 metrics.FullRegistryToolCount,
		"projected_tool_count":                     metrics.ProjectedToolCount,
		"messages_and_tools_json_bytes":            metrics.MessagesAndToolsJSONBytes,
	}
	return manager.StartChildSpan(ctx, langfuse.SpanOptions{
		Name: "agent.model_context", Metadata: metadata,
	})
}
