package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkbenchSourceModulesValidateExactProtocol(t *testing.T) {
	for _, modules := range [][]string{nil, {}, {"market", "diagram"}, WorkbenchViewModules()} {
		require.True(t, ValidWorkbenchSourceModules(modules), "%v", modules)
	}
	for _, modules := range [][]string{{"unknown"}, {"market", "market"}, {""}, {" market"}, {"MARKET"}, {"home", "market", "indicators", "judgments", "strategies", "diagram", "reports", "market"}} {
		require.False(t, ValidWorkbenchSourceModules(modules), "%v", modules)
	}
}

func TestWorkbenchSourceModuleAccessRequiresValidScope(t *testing.T) {
	row := &ResearchWorkbench{SourceDataAvailable: true, SourceSharingApproved: true, SourceSharingModules: []string{"market", "diagram"}}
	require.True(t, row.CanViewSourceModule("market"))
	require.True(t, row.CanViewSourceModule("diagram"))
	require.False(t, row.CanViewSourceModule("indicators"))
	require.False(t, row.CanViewSourceModule("unknown"))
	row.SourceSharingApproved = false
	require.False(t, row.CanViewSourceModule("market"))
	row.CanReadSource = true
	for _, module := range WorkbenchViewModules() {
		require.True(t, row.CanViewSourceModule(module))
	}
	require.False(t, row.CanViewSourceModule("unknown"))
	row.SourceDataAvailable = false
	require.False(t, row.CanViewSourceModule("market"))
	require.False(t, (*ResearchWorkbench)(nil).CanViewSourceModule("market"))
}
