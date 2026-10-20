package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"testing"
)

func TestWorkspaceJudgmentRouteRemainsPersonalContributorOperation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
	RegisterWorkspaceJudgmentRoutes(r.Group("/api/v1"), handler.NewWorkspaceHandler(nil), handler.NewModelHandler(nil), g)
	g.assertAPIKeyPoliciesMatchRoutes(r)
	routes := r.Routes()
	if len(routes) != 1 || routes[0].Method != "POST" || routes[0].Path != "/api/v1/workspaces/research/:id/judgment/generate" {
		t.Fatalf("unexpected routes: %#v", routes)
	}
	if _, ok := g.apiKeyAuthorizer.Lookup(routes[0].Method, routes[0].Path); ok {
		t.Fatal("judgment generator cannot expose an API-key route")
	}
}
