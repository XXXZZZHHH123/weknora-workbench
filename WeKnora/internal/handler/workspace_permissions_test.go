package handler

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func setDiagramWorkbenchPermission(t *testing.T, f *workspaceDiagramFixture, permission string) {
	t.Helper()
	var row types.ResearchWorkbench
	require.NoError(t, f.db.First(&row, "id = ?", f.workbenchID).Error)
	row.AccessPolicy = &types.ResourceAccessPolicy{SpacePermission: permission, Members: []types.ResourceMemberPermission{}}
	require.NoError(t, f.db.Save(&row).Error)
}

func TestWorkspaceDiagramRequiresIndependentEditGrant(t *testing.T) {
	for _, role := range []types.TenantRole{types.TenantRoleContributor, types.TenantRoleAdmin, types.TenantRoleOwner} {
		t.Run(string(role), func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			setDiagramWorkbenchPermission(t, f, "viewer")
			f.user, f.role = "peer", role
			response := f.request(diagramBody(t, ""))
			require.Equal(t, 403, response.Code, response.Body.String())
			require.Zero(t, f.models.client.calls)
		})
	}
	f := newWorkspaceDiagramFixture(t)
	setDiagramWorkbenchPermission(t, f, "editor")
	f.user = "peer"
	require.Equal(t, 200, f.request(diagramBody(t, "")).Code)
	require.Equal(t, 1, f.models.client.calls)
}

func TestWorkspaceDiagramDiscardsResultAfterPermissionRevocation(t *testing.T) {
	for _, change := range []string{"edit", "source"} {
		t.Run(change, func(t *testing.T) {
			f := newWorkspaceDiagramFixture(t)
			setDiagramWorkbenchPermission(t, f, "editor")
			f.user = "peer"
			f.models.client.after = func() {
				if change == "edit" {
					setDiagramWorkbenchPermission(t, f, "viewer")
				} else {
					f.kbs.allowed = false
				}
			}
			response := f.request(diagramBody(t, ""))
			require.Equal(t, 403, response.Code, response.Body.String())
			require.Equal(t, 1, f.models.client.calls)
			require.NotContains(t, response.Body.String(), "Generated configuration")
		})
	}
}

func TestWorkspaceJudgmentDraftRequiresIndependentEditGrant(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	setDiagramWorkbenchPermission(t, f, "viewer")
	f.user = "peer"
	response := judgmentRequest(f, diagramBody(t, ""))
	require.Equal(t, 403, response.Code, response.Body.String())
	require.Zero(t, f.models.client.calls)
	require.NotContains(t, response.Body.String(), "Generated configuration")
}
