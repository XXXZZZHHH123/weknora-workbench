package tools

import (
	"fmt"
	"slices"
)

type DocumentRequiredLocation struct {
	Period string `json:"period,omitempty"`
	Pages  []int  `json:"pages,omitempty"`
	Fact   string `json:"fact"`
}

type DocumentCoverageTarget struct {
	ByDocument bool
	Locations  []DocumentRequiredLocation
}

type DocumentCoverageBinding struct {
	Target   int   `json:"target_index"`
	Location *int  `json:"location_index,omitempty"`
	Pages    []int `json:"pages,omitempty"`
}

type DocumentCoverageRequirement struct {
	TargetIndex   int `json:"target_index"`
	LocationIndex int `json:"location_index"`
	DocumentRequiredLocation
	KnowledgeBaseID string `json:"knowledge_base_id,omitempty"`
	KnowledgeID     string `json:"knowledge_id,omitempty"`
}

func SetDocumentCoverageTargets(registry *ToolRegistry, targets []DocumentCoverageTarget) {
	if state := registeredDocumentScope(registry); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.coverageTargets = targets
	}
}

func ValidDocumentCoveragePages(pages []int) bool {
	seen := map[int]bool{}
	for _, page := range pages {
		if page <= 0 || seen[page] {
			return false
		}
		seen[page] = true
	}
	return len(pages) <= MaxPhysicalPagesPerRead
}

func (r *RequestedDocumentConstraint) validateCoverageBindings(source DocumentSourceSelection) error {
	seen := map[[2]int]bool{}
	for _, binding := range source.Coverage {
		location := -1
		if binding.Location != nil {
			location = *binding.Location
		}
		if binding.Target < 0 || binding.Target >= len(r.coverageTargets) ||
			(binding.Location != nil && (location < 0 || location >= len(r.coverageTargets[binding.Target].Locations))) ||
			(location < 0 && len(binding.Pages) > 0) ||
			!ValidDocumentCoveragePages(binding.Pages) || seen[[2]int{binding.Target, location}] {
			return fmt.Errorf("文档与核对要求的对应关系无效，请核对所选文档和页码")
		}
		seen[[2]int{binding.Target, location}] = true
		if source.UserPages != nil {
			for _, page := range binding.Pages {
				if !slices.Contains(source.UserPages.Pages, page) {
					return fmt.Errorf("计划核对的页码超出用户指定范围")
				}
			}
		}
	}
	if len(source.Coverage) > 32 {
		return fmt.Errorf("单份文档关联的核对要求过多")
	}
	return nil
}

func (r *RequestedDocumentConstraint) coverageRequirements(targetIndex int) []DocumentCoverageRequirement {
	var requirements []DocumentCoverageRequirement
	for index, location := range r.coverageTargets[targetIndex].Locations {
		start := len(requirements)
		for _, id := range r.order {
			source := r.sources[id]
			for _, binding := range source.Coverage {
				if binding.Target != targetIndex || binding.Location == nil || *binding.Location != index {
					continue
				}
				requirement := DocumentCoverageRequirement{TargetIndex: targetIndex, LocationIndex: index, DocumentRequiredLocation: location,
					KnowledgeBaseID: source.KnowledgeBaseID, KnowledgeID: id}
				// Evidence-based page hints can refine the plan without
				// changing an explicit user page restriction.
				if len(binding.Pages) > 0 {
					requirement.Pages = binding.Pages
				} else if len(location.Pages) == 0 && len(r.coverageTargets[targetIndex].Locations) == 1 {
					requirement.Pages = documentSelectionHints(source)
				}
				requirement.Pages = slices.Clone(requirement.Pages)
				requirements = append(requirements, requirement)
			}
		}
		if len(requirements) == start {
			location.Pages = slices.Clone(location.Pages)
			requirements = append(requirements, DocumentCoverageRequirement{TargetIndex: targetIndex, LocationIndex: index, DocumentRequiredLocation: location})
		}
	}
	return requirements
}

func BoundDocumentCoverageRequirements(registry *ToolRegistry) []DocumentCoverageRequirement {
	var requirements []DocumentCoverageRequirement
	if state := registeredDocumentScope(registry); state != nil {
		state.mu.RLock()
		defer state.mu.RUnlock()
		for index := range state.coverageTargets {
			requirements = append(requirements, state.coverageRequirements(index)...)
		}
	}
	return requirements
}

func (r *RequestedDocumentConstraint) searchCoverage(index *int, ids []string) ([]string, []DocumentCoverageRequirement) {
	if r == nil {
		return nil, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if index == nil {
		return nil, nil
	}
	target := *index
	if target < 0 || target >= len(r.coverageTargets) {
		return nil, nil
	}
	var documents []string
	if r.coverageTargets[target].ByDocument {
		for _, id := range dedupNonEmptyStrings(ids) {
			for _, binding := range r.sources[id].Coverage {
				if binding.Target == target {
					documents = append(documents, id)
					break
				}
			}
		}
	}
	if len(documents) < 2 {
		documents = nil
	}
	requirements := r.coverageRequirements(target)
	return documents, slices.DeleteFunc(requirements, func(requirement DocumentCoverageRequirement) bool {
		return requirement.KnowledgeID == "" || !slices.Contains(ids, requirement.KnowledgeID)
	})
}
