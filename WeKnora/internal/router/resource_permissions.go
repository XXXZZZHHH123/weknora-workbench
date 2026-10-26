package router

import (
	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// Legacy resources retain their existing ownership guard. Once their owner
// saves an ACL, explicit editors replace the creator-or-admin content rule.
func (g *rbacGuards) kbPolicyGuard(resolve middleware.KBIDResolver, creator middleware.CreatorLookup, manage bool) gin.HandlerFunc {
	legacy := middleware.RequireOwnershipOrRole(types.TenantRoleAdmin, creator, g.cfg)
	return func(c *gin.Context) {
		if g.kbService == nil {
			legacy(c)
			return
		}
		id, err := resolve(c)
		if err != nil {
			_ = c.Error(err)
			c.Abort()
			return
		}
		kb, err := g.kbService.GetKnowledgeBaseByID(c.Request.Context(), id)
		if err != nil || kb == nil {
			_ = c.Error(apperrors.NewNotFoundError("知识库不存在"))
			c.Abort()
			return
		}
		if kb.AccessPolicy == nil && kb.SystemRole != types.KnowledgeBaseSystemRoleCentral {
			legacy(c)
			return
		}
		caller := middleware.KBAccessRequest(c).Caller
		if kb.TenantID != caller.TenantID && !manage {
			if _, err := access.ResolveKB(c.Request.Context(), middleware.KBAccessRequest(c), kb, types.OrgRoleEditor, g.kbShareService, nil); err != nil {
				_ = c.Error(apperrors.NewForbiddenError("无权修改此资源"))
				c.Abort()
				return
			}
			c.Next()
			return
		}
		permission := access.LocalKBPermission(c.Request.Context(), caller, kb)
		required := types.OrgRoleEditor
		if manage {
			required = types.OrgRoleAdmin
		}
		if !permission.IsValid() || !permission.HasPermission(required) {
			_ = c.Error(apperrors.NewForbiddenError("无权修改此资源"))
			c.Abort()
			return
		}
		c.Next()
	}
}

func (g *rbacGuards) ManageKB() gin.HandlerFunc {
	return g.kbPolicyGuard(middleware.KBIDFromParam("id"), g.kbCreator, true)
}
