package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedStructuredIntegrationRequiresInstallerOwnershipMarker(t *testing.T) {
	require.True(t, IsManagedStructuredIntegration("改名后的服务 [managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421]"))
	for _, description := range []string{"表格数据分析（结构化）", "managed-by:cotton-structured-data/example", "[managed-by:cotton-structured-data/718d26f7-7952-4207-abd8-7210df12b421-extra]", "[managed-by:other/718d26f7-7952-4207-abd8-7210df12b421]"} {
		require.False(t, IsManagedStructuredIntegration(description))
	}
}
