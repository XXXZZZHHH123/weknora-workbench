package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCreateTenantSearchDefaultIsOwnedAndKeyless(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	tenants := NewTenantRepository(db)
	providers := NewWebSearchProviderRepository(db)
	var ids []string
	for _, name := range []string{"first", "second"} {
		tenant := &types.Tenant{Name: name, WebSearchConfig: &types.WebSearchConfig{MaxResults: 4}}
		require.NoError(t, tenants.CreateTenant(ctx, tenant))
		provider, err := providers.GetDefault(ctx, tenant.ID)
		require.NoError(t, err)
		require.NotNil(t, provider)
		_, err = uuid.Parse(provider.ID)
		require.NoError(t, err)
		require.Equal(t, tenant.ID, provider.TenantID)
		require.Equal(t, types.WebSearchProviderTypeKeenable, provider.Provider)
		require.Equal(t, types.WebSearchProviderParameters{}, provider.Parameters)
		rows, err := providers.List(ctx, tenant.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		stored, err := tenants.GetTenantByID(ctx, tenant.ID)
		require.NoError(t, err)
		require.Equal(t, 4, stored.WebSearchConfig.MaxResults)
		ids = append(ids, provider.ID)
		if len(ids) == 2 {
			other, err := providers.GetByID(ctx, tenant.ID, ids[0])
			require.NoError(t, err)
			require.Nil(t, other, "provider IDs cannot cross workspace boundaries")
		}
	}
	require.NotEqual(t, ids[0], ids[1])
}

func TestCreateTenantPreservesExplicitLegacySearch(t *testing.T) {
	for name, cfg := range map[string]*types.WebSearchConfig{
		"provider":   {Provider: "bing", MaxResults: 3},
		"key":        {APIKey: "fixture-legacy-key"},
		"proxy":      {ProxyURL: "https://proxy.example.com"},
		"whitespace": {Provider: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t)
			tenant := &types.Tenant{Name: name, WebSearchConfig: cfg}
			repo := NewTenantRepository(db)
			require.NoError(t, repo.CreateTenant(context.Background(), tenant))
			rows, err := NewWebSearchProviderRepository(db).List(context.Background(), tenant.ID)
			require.NoError(t, err)
			require.Empty(t, rows, "a new default must not shadow an explicit legacy configuration")
			stored, err := repo.GetTenantByID(context.Background(), tenant.ID)
			require.NoError(t, err)
			require.Equal(t, cfg, stored.WebSearchConfig)
		})
	}
}

func TestCreateTenantSearchFailureRollsBackTenant(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Migrator().DropTable(&types.WebSearchProviderEntity{}))
	err := NewTenantRepository(db).CreateTenant(context.Background(), &types.Tenant{Name: "must-rollback"})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Unscoped().Model(&types.Tenant{}).Count(&count).Error)
	require.Zero(t, count, "failed provisioning must not leave a half-created workspace")
}

func TestDeleteTenantRemovesProvisionedSearchOnlyWithinScope(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTenantRepository(db)
	ctx := context.Background()
	a, b := &types.Tenant{Name: "rollback"}, &types.Tenant{Name: "keep"}
	require.NoError(t, repo.CreateTenant(ctx, a))
	require.NoError(t, repo.CreateTenant(ctx, b))
	require.NoError(t, repo.DeleteTenant(ctx, a.ID))
	providers := NewWebSearchProviderRepository(db)
	rows, err := providers.List(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = providers.List(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var count int64
	require.NoError(t, db.Unscoped().Model(&types.WebSearchProviderEntity{}).Where("tenant_id = ?", a.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "rollback keeps recoverable soft-deleted settings")
}
