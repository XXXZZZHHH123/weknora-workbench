package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func workspaceAdminContext() context.Context {
	return types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin})
}

func TestTenantMemberService_AdminCannotManageOwners(t *testing.T) {
	for _, action := range []string{"promote", "demote", "remove"} {
		t.Run(action, func(t *testing.T) {
			svc, repo := newServiceWithRepo()
			for _, member := range []struct {
				id   string
				role types.TenantRole
			}{{"owner", types.TenantRoleOwner}, {"other-owner", types.TenantRoleOwner}, {"editor", types.TenantRoleContributor}} {
				if _, err := svc.AddMember(context.Background(), member.id, 1, member.role, nil); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch action {
			case "promote":
				err = svc.UpdateRole(workspaceAdminContext(), "editor", 1, types.TenantRoleOwner)
			case "demote":
				err = svc.UpdateRole(workspaceAdminContext(), "owner", 1, types.TenantRoleViewer)
			case "remove":
				err = svc.RemoveMember(workspaceAdminContext(), "owner", 1)
			}
			if err == nil {
				t.Fatal("administrator must not change workspace ownership")
			}
			owner, _ := repo.Get(context.Background(), "owner", 1)
			editor, _ := repo.Get(context.Background(), "editor", 1)
			if owner == nil || owner.Role != types.TenantRoleOwner || editor.Role != types.TenantRoleContributor {
				t.Fatal("denied operation changed membership")
			}
		})
	}
}

func TestTenantMemberService_AdminManagesNonOwnersWithAudit(t *testing.T) {
	repo := newFakeRepo()
	audit, events, _ := newSvcForTest()
	svc := NewTenantMemberService(repo, audit, nil, nil)
	ctx := workspaceAdminContext()
	if _, err := svc.AddMember(ctx, "editor", 1, types.TenantRoleContributor, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateRole(ctx, "editor", 1, types.TenantRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveMember(ctx, "editor", 1); err != nil {
		t.Fatal(err)
	}
	if len(events.created) != 3 {
		t.Fatalf("audit events = %d, want 3", len(events.created))
	}
	for _, entry := range events.created {
		if entry.ActorUserID != "admin" || entry.ActorRole != "admin" || entry.TargetUserID != "editor" || entry.Outcome != types.AuditOutcomeSuccess {
			t.Fatalf("incomplete audit: %#v", entry)
		}
	}
}

func TestInvitationService_AdminCannotInviteOwners(t *testing.T) {
	for _, shareLink := range []bool{false, true} {
		svc, repo, _ := newInvitationSvc()
		var err error
		if shareLink {
			_, _, err = svc.CreateShareLink(workspaceAdminContext(), 1, types.TenantRoleOwner, nil, "")
		} else {
			_, err = svc.Create(workspaceAdminContext(), 1, "new-owner", types.TenantRoleOwner, nil, "")
		}
		if err == nil {
			t.Fatalf("admin owner invitation unexpectedly allowed: shareLink=%v", shareLink)
		}
		if len(repo.rows) != 0 {
			t.Fatal("denied invitation was persisted")
		}
	}
}
