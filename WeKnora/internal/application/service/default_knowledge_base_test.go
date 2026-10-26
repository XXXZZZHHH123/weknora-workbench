package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func defaultKBFixture(t *testing.T) (*WorkspaceService, *knowledgeBaseService, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "default.db")+"?_busy_timeout=5000"), &gorm.Config{})
	require.NoError(t, err)
	sql, _ := db.DB()
	t.Cleanup(func() { sql.Close() })
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.CentralDataset{}))
	migration, err := os.ReadFile("../../../migrations/sqlite/000040_default_knowledge_base.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	// The native service has no model, queue or network dependency in this fixture:
	// any accidental generation in the initialization path cannot succeed silently.
	native := &knowledgeBaseService{repo: repository.NewKnowledgeBaseRepository(db)}
	ctx := types.WithExecutionTenant(workspaceContext("alice", 1, types.TenantRoleContributor), 1)
	return NewWorkspaceService(db, native, nil), native, ctx
}

func TestDefaultKnowledgeBaseExplicitIdempotentInitialization(t *testing.T) {
	s, native, ctx := defaultKBFixture(t)
	ordinary, err := native.CreateKnowledgeBase(ctx, &types.KnowledgeBase{Name: "宏观数据"})
	require.NoError(t, err)
	view, err := s.GetDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Nil(t, view)
	var count int64
	require.NoError(t, s.db.Model(&types.DefaultKnowledgeBaseBinding{}).Count(&count).Error)
	require.Zero(t, count)
	_, err = s.InitializeDefaultKnowledgeBase(workspaceContext("reader", 1, types.TenantRoleViewer))
	workspaceStatus(t, err, 403)
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view, err := s.InitializeDefaultKnowledgeBase(ctx)
			if err != nil {
				errs <- err
				return
			}
			ids <- view.KnowledgeBaseID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	selected := ""
	for id := range ids {
		require.NotEqual(t, ordinary.ID, id)
		if selected != "" {
			require.Equal(t, selected, id)
		}
		selected = id
	}
	require.NotEmpty(t, selected)
	view, err = s.GetDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Equal(t, selected, view.KnowledgeBaseID)
	require.Equal(t, "macro", view.Kind)
	require.Equal(t, "knowledge_base", view.SourceScope)
	var kb types.KnowledgeBase
	require.NoError(t, s.db.First(&kb, "id = ?", selected).Error)
	kb.EnsureDefaults()
	require.True(t, kb.IndexingStrategy.Disabled)
	require.False(t, kb.IndexingStrategy.HasAnyIndexing())
	require.False(t, kb.ProfileConfig.Enabled)
	require.False(t, kb.AutoTagConfig.Enabled)
	require.False(t, kb.QuestionGenerationConfig.Enabled)
	require.Empty(t, kb.EmbeddingModelID)
	require.Empty(t, kb.SummaryModelID)
	require.NoError(t, s.db.Model(&types.KnowledgeBase{}).Count(&count).Error)
	require.Equal(t, int64(2), count)
	other, err := s.GetDefaultKnowledgeBase(workspaceContext("alice", 2, types.TenantRoleContributor))
	require.NoError(t, err)
	require.Nil(t, other)
	other, err = s.InitializeDefaultKnowledgeBase(workspaceContext("alice", 2, types.TenantRoleContributor))
	require.NoError(t, err)
	require.NotEqual(t, selected, other.KnowledgeBaseID)
	require.NoError(t, s.db.Delete(&kb).Error)
	_, err = s.InitializeDefaultKnowledgeBase(ctx)
	workspaceStatus(t, err, 409)
}

func TestDefaultKnowledgeBaseFailedCreationRetriesReservedIdentity(t *testing.T) {
	s, _, ctx := defaultKBFixture(t)
	require.NoError(t, s.db.Exec("CREATE TRIGGER fail_default BEFORE INSERT ON knowledge_bases BEGIN SELECT RAISE(ABORT,'creation unavailable'); END").Error)
	_, err := s.InitializeDefaultKnowledgeBase(ctx)
	require.ErrorContains(t, err, "creation unavailable")
	var reserved types.DefaultKnowledgeBaseBinding
	require.NoError(t, s.db.First(&reserved).Error)
	view, err := s.GetDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Nil(t, view)
	require.NoError(t, s.db.Exec("DROP TRIGGER fail_default").Error)
	view, err = s.InitializeDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Equal(t, reserved.KnowledgeBaseID, view.KnowledgeBaseID)
	var count int64
	require.NoError(t, s.db.Model(&types.KnowledgeBase{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestDefaultKnowledgeBaseNameAndCentralListFollowCurrentKnowledgeBaseName(t *testing.T) {
	s, native, ctx := defaultKBFixture(t)
	view, err := s.InitializeDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	var registered types.CentralDataset
	require.NoError(t, s.db.First(&registered, "backing_knowledge_base_id = ?", view.KnowledgeBaseID).Error)
	t.Run("new-default-name", func(t *testing.T) {
		require.Equal(t, "default库", view.Name)
		require.Equal(t, "default库", registered.Name)
	})
	registeredName := registered.Name
	admin := types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin})
	renamed, err := native.UpdateKnowledgeBase(admin, view.KnowledgeBaseID, "Custom source name", "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, view.KnowledgeBaseID, renamed.ID)
	listed, err := s.ListCentralDatasets(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, registered.ID, listed[0].ID)
	require.Equal(t, view.KnowledgeBaseID, listed[0].BackingKnowledgeBaseID)
	require.Equal(t, "Custom source name", listed[0].Name)
	require.NoError(t, s.db.First(&registered, "id = ?", registered.ID).Error)
	require.Equal(t, registeredName, registered.Name, "listing must not rewrite the registry")
	repeated, err := s.InitializeDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Equal(t, view.KnowledgeBaseID, repeated.KnowledgeBaseID)
	require.Equal(t, "Custom source name", repeated.Name, "initialization must preserve an existing custom name")
}

func TestDefaultKnowledgeBaseSettingsPreserveOptOutAndPermitExplicitEnable(t *testing.T) {
	s, native, ctx := defaultKBFixture(t)
	view, err := s.InitializeDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	_, err = native.UpdateKnowledgeBase(ctx, view.KnowledgeBaseID, "Denied", "", nil, nil)
	workspaceStatus(t, err, 403)
	ctx = types.WithCaller(ctx, types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin})
	updated, err := native.UpdateKnowledgeBase(ctx, view.KnowledgeBaseID, "Renamed", "", &types.KnowledgeBaseConfig{IndexingStrategy: &types.IndexingStrategy{}}, nil)
	require.NoError(t, err)
	require.True(t, updated.IndexingStrategy.Disabled)
	require.False(t, updated.IndexingStrategy.HasAnyIndexing())
	updated, err = native.UpdateKnowledgeBase(ctx, view.KnowledgeBaseID, "Renamed", "", &types.KnowledgeBaseConfig{IndexingStrategy: &types.IndexingStrategy{VectorEnabled: true}}, nil)
	require.NoError(t, err)
	require.False(t, updated.IndexingStrategy.Disabled)
	require.True(t, updated.IndexingStrategy.VectorEnabled)
	view, err = s.GetDefaultKnowledgeBase(ctx)
	require.NoError(t, err)
	require.Equal(t, "Renamed", view.Name)
	require.Equal(t, "macro", view.Kind)
	ordinary, err := native.CreateKnowledgeBase(ctx, &types.KnowledgeBase{Name: "Ordinary"})
	require.NoError(t, err)
	require.True(t, ordinary.IndexingStrategy.VectorEnabled)
	require.True(t, ordinary.IndexingStrategy.KeywordEnabled)
	_, err = native.UpdateKnowledgeBase(ctx, ordinary.ID, "Ordinary", "", &types.KnowledgeBaseConfig{IndexingStrategy: &types.IndexingStrategy{Disabled: true}}, nil)
	require.ErrorContains(t, err, "at least one indexing")
}
