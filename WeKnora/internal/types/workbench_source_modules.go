package types

import "slices"

// WorkbenchViewModules returns a fresh list so callers cannot change the shared
// protocol. A source grant never implies modules added by a future release.
func WorkbenchViewModules() []string {
	return []string{"home", "market", "indicators", "judgments", "strategies", "diagram", "reports"}
}

func ValidWorkbenchSourceModules(modules []string) bool {
	allowed := WorkbenchViewModules()
	if len(modules) > len(allowed) {
		return false
	}
	seen := make(map[string]struct{}, len(modules))
	for _, module := range modules {
		if !slices.Contains(allowed, module) {
			return false
		}
		if _, exists := seen[module]; exists {
			return false
		}
		seen[module] = struct{}{}
	}
	return true
}

func (w *ResearchWorkbench) CanViewSourceModule(module string) bool {
	if w == nil || !w.SourceDataAvailable || !slices.Contains(WorkbenchViewModules(), module) {
		return false
	}
	if w.ScopeVersion == 2 {
		return w.CanReadSource || slices.Contains(w.EffectiveViewModules, module)
	}
	return w.CanReadSource || (w.SourceSharingApproved && ValidWorkbenchSourceModules(w.SourceSharingModules) && slices.Contains(w.SourceSharingModules, module))
}
