package types

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidateWorkbenchRestrictedViewProfiles(profiles []WorkbenchRestrictedViewProfile) error {
	if len(profiles) > 32 {
		return fmt.Errorf("受限视角配置最多为32组")
	}
	ids := make(map[string]struct{}, len(profiles))
	totalMembers := 0
	for _, profile := range profiles {
		if len(profile.ID) == 0 || len(profile.ID) > 64 || strings.IndexFunc(profile.ID, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
		}) >= 0 {
			return fmt.Errorf("受限视角配置标识无效")
		}
		if _, exists := ids[profile.ID]; exists {
			return fmt.Errorf("受限视角配置标识不能重复")
		}
		ids[profile.ID] = struct{}{}
		if strings.TrimSpace(profile.Name) == "" || utf8.RuneCountInString(profile.Name) > 80 || strings.IndexFunc(profile.Name, unicode.IsControl) >= 0 {
			return fmt.Errorf("请填写80字以内的受限视角配置名称")
		}
		if len(profile.Modules) == 0 || !ValidWorkbenchSourceModules(profile.Modules) {
			return fmt.Errorf("每组受限视角请选择至少一个有效页面，页面不能重复")
		}
		if profile.AllMembers && len(profile.MemberIDs) > 0 {
			return fmt.Errorf("受限视角请选择全部成员或指定成员，不能同时设置")
		}
		totalMembers += len(profile.MemberIDs)
		if totalMembers > 1000 {
			return fmt.Errorf("受限视角成员配置总计不能超过1000项")
		}
		members := make(map[string]struct{}, len(profile.MemberIDs))
		for _, id := range profile.MemberIDs {
			if id == "" || len(id) > 512 || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 {
				return fmt.Errorf("受限视角成员标识无效")
			}
			if _, exists := members[id]; exists {
				return fmt.Errorf("同一受限视角的成员不能重复")
			}
			members[id] = struct{}{}
		}
	}
	return nil
}

// Nil retains the old, explicit module grant. An empty slice revokes every
// profile. Neither a group assignment nor its union can exceed source consent.
func EffectiveWorkbenchSourceModules(profiles []WorkbenchRestrictedViewProfile, userID string, approved []string) []string {
	if userID == "" || !ValidWorkbenchSourceModules(approved) || ValidateWorkbenchRestrictedViewProfiles(profiles) != nil {
		return []string{}
	}
	if profiles == nil {
		return append([]string{}, approved...)
	}
	result := []string{}
	for _, module := range WorkbenchViewModules() {
		if !slices.Contains(approved, module) {
			continue
		}
		for _, profile := range profiles {
			if (profile.AllMembers || slices.Contains(profile.MemberIDs, userID)) && slices.Contains(profile.Modules, module) {
				result = append(result, module)
				break
			}
		}
	}
	return result
}

func WorkbenchRestrictedProfileModules(profiles []WorkbenchRestrictedViewProfile) []string {
	result := []string{}
	for _, module := range WorkbenchViewModules() {
		for _, profile := range profiles {
			if slices.Contains(profile.Modules, module) {
				result = append(result, module)
				break
			}
		}
	}
	return result
}
