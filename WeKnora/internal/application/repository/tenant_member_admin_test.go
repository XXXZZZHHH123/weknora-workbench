package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTenantMemberRepository_AdminOwnerProtectionAtWrite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "members.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.TenantMember{}); err != nil {
		t.Fatal(err)
	}
	repo := NewTenantMemberRepository(db)
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1, UserID: "admin", Role: types.TenantRoleAdmin})
	for _, role := range []types.TenantRole{types.TenantRoleOwner, types.TenantRoleContributor} {
		if err := repo.Create(context.Background(), &types.TenantMember{UserID: string(role), TenantID: 1, Role: role, Status: types.TenantMemberStatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []func() error{
		func() error { return repo.UpdateRole(ctx, "owner", 1, types.TenantRoleViewer) },
		func() error { return repo.SoftDelete(ctx, "owner", 1) },
		func() error { return repo.UpdateRole(ctx, "contributor", 1, types.TenantRoleOwner) },
	} {
		if err := operation(); !errors.Is(err, ErrOwnerMembershipProtected) {
			t.Fatalf("want owner protection, got %v", err)
		}
	}
	owner, err := repo.Get(context.Background(), "owner", 1)
	if err != nil || owner == nil || owner.Role != types.TenantRoleOwner {
		t.Fatalf("owner changed: %#v err=%v", owner, err)
	}
	if err := repo.UpdateRole(ctx, "contributor", 1, types.TenantRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, "contributor", 1); err != nil {
		t.Fatal(err)
	}
}
