package agent

import (
	"encoding/json"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
)

// Refresh the request-local source codec from actual registered adapters before
// encoding a model call. Protocol refresh can add/remove native definitions;
// a matching name, schema field or external registration grants no contract.
func (e *AgentEngine) syncStructuredToolSourceContracts() {
	if e.modelContext == nil || e.toolRegistry == nil {
		return
	}
	current := map[string]bool{}
	for _, definition := range e.toolRegistry.GetFunctionDefinitions() {
		current[definition.Name] = false
		tool, err := e.toolRegistry.GetTool(definition.Name)
		if err != nil {
			continue
		}
		_, legacy := tool.(*agenttools.StructuredDataTool)
		descriptor, _, native := e.toolRegistry.StructuredInvocation(definition.Name, json.RawMessage(`{}`))
		if !legacy && (!native || descriptor.Operation == "describe_tools" || descriptor.ToolName != definition.Name) {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(definition.Parameters, &schema) == nil && schema.Properties["knowledge_base_id"] != nil {
			current[definition.Name] = true
		}
	}
	e.modelContext.SetStructuredToolSourceContracts(current)
}
