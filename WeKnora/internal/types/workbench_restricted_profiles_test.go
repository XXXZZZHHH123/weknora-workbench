package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkbenchRestrictedProfileModulesOnlyAssignedConsent(t *testing.T) {
	profiles := []WorkbenchRestrictedViewProfile{
		{ID: "everyone", Name: "Home", Modules: []string{"home"}, AllMembers: true},
		{ID: "a", Name: "Market", Modules: []string{"market"}, MemberIDs: []string{"a"}},
		{ID: "ab", Name: "Diagram and reports", Modules: []string{"diagram", "reports"}, MemberIDs: []string{"a", "b"}},
	}
	approved := []string{"home", "market", "diagram"}
	require.Equal(t, []string{"home", "market", "diagram"}, EffectiveWorkbenchSourceModules(profiles, "a", approved))
	require.Equal(t, []string{"home", "diagram"}, EffectiveWorkbenchSourceModules(profiles, "b", approved))
	require.Equal(t, []string{"home"}, EffectiveWorkbenchSourceModules(profiles, "c", approved))
	require.Empty(t, EffectiveWorkbenchSourceModules(profiles, "", approved))
	require.Empty(t, EffectiveWorkbenchSourceModules(profiles, "a", []string{}))
	require.Equal(t, approved, EffectiveWorkbenchSourceModules(nil, "a", approved))
	require.Empty(t, EffectiveWorkbenchSourceModules([]WorkbenchRestrictedViewProfile{}, "a", approved))
	require.Equal(t, []string{"home", "market", "diagram", "reports"}, WorkbenchRestrictedProfileModules(profiles))
	profiles[1].Modules = []string{"arbitrary"}
	require.Empty(t, EffectiveWorkbenchSourceModules(profiles, "a", approved), "invalid persisted configuration must fail closed")
}

func TestWorkbenchRestrictedProfileProtocolLimits(t *testing.T) {
	require.NoError(t, ValidateWorkbenchRestrictedViewProfiles(nil))
	require.NoError(t, ValidateWorkbenchRestrictedViewProfiles([]WorkbenchRestrictedViewProfile{}))
	valid := WorkbenchRestrictedViewProfile{ID: "view", Name: "View", Modules: []string{"diagram"}, MemberIDs: []string{}}
	require.NoError(t, ValidateWorkbenchRestrictedViewProfiles([]WorkbenchRestrictedViewProfile{valid}))
	for name, modify := range map[string]func(*WorkbenchRestrictedViewProfile){
		"empty ID":          func(p *WorkbenchRestrictedViewProfile) { p.ID = "" },
		"long ID":           func(p *WorkbenchRestrictedViewProfile) { p.ID = strings.Repeat("x", 65) },
		"invalid ID":        func(p *WorkbenchRestrictedViewProfile) { p.ID = "group name" },
		"empty name":        func(p *WorkbenchRestrictedViewProfile) { p.Name = "  " },
		"long name":         func(p *WorkbenchRestrictedViewProfile) { p.Name = strings.Repeat("名", 81) },
		"control name":      func(p *WorkbenchRestrictedViewProfile) { p.Name = "name\n" },
		"empty pages":       func(p *WorkbenchRestrictedViewProfile) { p.Modules = nil },
		"duplicate pages":   func(p *WorkbenchRestrictedViewProfile) { p.Modules = []string{"diagram", "diagram"} },
		"unknown page":      func(p *WorkbenchRestrictedViewProfile) { p.Modules = []string{"sql"} },
		"duplicate members": func(p *WorkbenchRestrictedViewProfile) { p.MemberIDs = []string{"a", "a"} },
		"empty member":      func(p *WorkbenchRestrictedViewProfile) { p.MemberIDs = []string{""} },
		"long member":       func(p *WorkbenchRestrictedViewProfile) { p.MemberIDs = []string{strings.Repeat("a", 513)} },
		"space member":      func(p *WorkbenchRestrictedViewProfile) { p.MemberIDs = []string{" a"} },
		"control member":    func(p *WorkbenchRestrictedViewProfile) { p.MemberIDs = []string{"a\nb"} },
		"mixed assignment":  func(p *WorkbenchRestrictedViewProfile) { p.AllMembers = true; p.MemberIDs = []string{"a"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			modify(&p)
			require.Error(t, ValidateWorkbenchRestrictedViewProfiles([]WorkbenchRestrictedViewProfile{p}))
		})
	}
	require.Error(t, ValidateWorkbenchRestrictedViewProfiles([]WorkbenchRestrictedViewProfile{valid, valid}))
	groups := make([]WorkbenchRestrictedViewProfile, 33)
	require.Error(t, ValidateWorkbenchRestrictedViewProfiles(groups))
	members := make([]string, 1001)
	for i := range members {
		members[i] = strings.Repeat("a", i%100+1)
	}
	valid.MemberIDs = members
	require.Error(t, ValidateWorkbenchRestrictedViewProfiles([]WorkbenchRestrictedViewProfile{valid}))
}
