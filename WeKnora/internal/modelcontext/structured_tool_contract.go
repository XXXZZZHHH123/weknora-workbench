package modelcontext

import (
	"strings"
	"sync/atomic"
)

type structuredToolContracts struct {
	current atomic.Value // immutable map[string]bool; replaced between model calls
}

// SetStructuredToolSourceContracts snapshots actual current registrations, not
// model-supplied names or schemas. True denotes a native adapter that owns the
// root knowledge_base_id selector; false denotes a current non-native tool.
// This does not grant source access. The execution boundary still authorizes
// every restored ID. Omitted historical names keep their legacy codec policy.
func (r *Registry) SetStructuredToolSourceContracts(current map[string]bool) {
	if r == nil || r.sources == nil {
		return
	}
	copy := make(map[string]bool, len(current))
	for name, native := range current {
		copy[name] = native
	}
	r.sources.structuredTools.current.Store(copy)
}

func (r *sourceRegistry) structuredToolContract(name string) (bool, bool) {
	current := r.structuredTools.current.Load()
	if current == nil {
		return false, false
	}
	native, exists := current.(map[string]bool)[name]
	return native, exists
}

func (r *sourceRegistry) argumentPathAllowed(toolName string, path []string) bool {
	if native, current := r.structuredToolContract(toolName); current && (native || len(toolHandlePolicies[toolName].rootOnlySourceIDKeys) > 0) {
		// A current external registration cannot inherit a retired native
		// policy merely by borrowing its name. Native payloads stay opaque.
		return native && len(path) == 1 && strings.EqualFold(path[0], "knowledge_base_id")
	}
	return sourceArgumentPathAllowed(toolName, path)
}

func (r *sourceRegistry) rootOnlyToolArguments(toolName string) bool {
	if native, current := r.structuredToolContract(toolName); current && (native || len(toolHandlePolicies[toolName].rootOnlySourceIDKeys) > 0) {
		return native
	}
	return len(toolHandlePolicies[toolName].rootOnlySourceIDKeys) > 0
}
