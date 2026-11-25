package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchFileVersionKeepsApprovedSourceAndAdvancesRevision(t *testing.T) {
	s, _, wb, owner := restrictedProfilesFixture(t)
	approved := true
	policy := &types.ResourceAccessPolicy{SpacePermission: "viewer", Members: []types.ResourceMemberPermission{}}
	profiles := []types.WorkbenchRestrictedViewProfile{
		{ID: "market-view", Name: "Market", Modules: []string{"market"}, MemberIDs: []string{"reader-a"}},
	}
	_, err := s.UpdateWorkbenchPermissionSettings(owner, wb.ID, WorkbenchPermissionSettings{
		ResourceAccessPolicy: policy, RestrictedViewProfiles: &profiles, SourceSharingApproved: &approved,
	})
	require.NoError(t, err)
	other, err := s.CreateWorkbench(owner, WorkspaceInput{
		Name: workspaceString("Unbound"), Sources: sourceInputs(), IdempotencyKey: "version-unbound",
	})
	require.NoError(t, err)
	file := types.Knowledge{ID: "workbook", TenantID: 1, KnowledgeBaseID: "own", Type: "file",
		FileType: "xlsx", FilePath: "before.xlsx", FileHash: strings.Repeat("a", 32)}
	require.NoError(t, s.db.Create(&file).Error)
	reader := workspaceContext("reader-a", 1, types.TenantRoleViewer)
	previous, err := s.GetWorkbenchSourceScope(reader, wb.ID)
	require.NoError(t, err)
	require.Len(t, previous.Sources, 1)
	binding := previous.Sources[0]
	require.True(t, binding.Delegated)
	require.Equal(t, []string{"market"}, binding.ViewModules)
	require.Len(t, binding.Knowledge, 1)
	require.Equal(t, file.FileHash, binding.Knowledge[0].FileHash)

	updated := file
	now := time.Now().UTC()
	fallback := strings.Repeat("c", 32)
	require.NoError(t, updated.SetFileVersionHistory(types.KnowledgeFileVersionHistory{
		UpdatedAt: now, Previous: []types.KnowledgeFileVersion{{FileHash: file.FileHash}},
		LastPublishedFileHash: fallback,
	}))
	hash := strings.Repeat("b", 32)
	err = repository.NewKnowledgeRepository(s.db).UpdateKnowledgeFileSource(owner, &file, map[string]interface{}{
		"file_path": "after.xlsx", "file_hash": hash, "metadata": updated.Metadata,
		"parse_status": types.ParseStatusPending,
	})
	require.NoError(t, err)

	current, err := s.GetWorkbenchSourceScope(reader, wb.ID)
	require.NoError(t, err)
	require.Equal(t, previous.SourcesRevision+1, current.SourcesRevision)
	require.Len(t, current.Sources, 1)
	require.Equal(t, binding.ID, current.Sources[0].ID)
	require.True(t, current.Sources[0].Delegated)
	require.Equal(t, binding.ViewModules, current.Sources[0].ViewModules)
	require.Len(t, current.Sources[0].Knowledge, 1)
	require.Equal(t, file.ID, current.Sources[0].Knowledge[0].ID)
	require.Equal(t, hash, current.Sources[0].Knowledge[0].FileHash)
	require.Equal(t, []string{file.FileHash, fallback}, current.Sources[0].Knowledge[0].PreviousFileHashes)
	require.NotNil(t, current.Sources[0].Knowledge[0].FileVersionAt)
	require.True(t, now.Equal(*current.Sources[0].Knowledge[0].FileVersionAt))
	direct, err := s.GetWorkbenchSourceScope(owner, wb.ID)
	require.NoError(t, err)
	require.False(t, direct.Sources[0].Delegated)
	require.Equal(t, current.Sources[0].Knowledge, direct.Sources[0].Knowledge)
	assertNoWorkbenchSourceGrant(t, s, workspaceContext("reader-c", 1, types.TenantRoleViewer), wb.ID)
	unbound, err := s.GetWorkbenchSourceScope(owner, other.ID)
	require.NoError(t, err)
	require.Equal(t, other.SourcesRevision, unbound.SourcesRevision)
}
