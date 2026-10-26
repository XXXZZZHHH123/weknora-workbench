package router

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceStrategyRouteRemainsPersonalContributorOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
	RegisterWorkspaceStrategyRoutes(r.Group("/api/v1"), handler.NewWorkspaceHandler(nil), handler.NewModelHandler(nil), g)
	g.assertAPIKeyPoliciesMatchRoutes(r)
	routes := r.Routes()
	require.Len(t, routes, 1)
	require.Equal(t, "POST", routes[0].Method)
	require.Equal(t, "/api/v1/workspaces/research/:id/strategy/generate", routes[0].Path)
	_, allowed := g.apiKeyAuthorizer.Lookup(routes[0].Method, routes[0].Path)
	require.False(t, allowed)
}
