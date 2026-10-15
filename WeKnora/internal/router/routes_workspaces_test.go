package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"testing"
)

func TestWorkspaceRoutesStayPersonalAndRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
	RegisterWorkspaceRoutes(r.Group("/api/v1"), handler.NewWorkspaceHandler(nil), g)
	g.assertAPIKeyPoliciesMatchRoutes(r)
	routes := r.Routes()
	if len(routes) != 17 {
		t.Fatalf("routes = %d", len(routes))
	}
	for _, route := range routes {
		if g.apiKeyAuthorizer != nil {
			if _, ok := g.apiKeyAuthorizer.Lookup(route.Method, route.Path); ok {
				t.Fatalf("personal route declared for API key: %s %s", route.Method, route.Path)
			}
		}
	}
}

func TestWorkspaceDiagramRouteRequiresPersonalContributorContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
	RegisterWorkspaceDiagramRoutes(r.Group("/api/v1"), handler.NewWorkspaceHandler(nil), handler.NewModelHandler(nil), g)
	g.assertAPIKeyPoliciesMatchRoutes(r)
	routes := r.Routes()
	if len(routes) != 1 || routes[0].Method != "POST" || routes[0].Path != "/api/v1/workspaces/research/:id/diagram/generate" {
		t.Fatalf("diagram route = %#v", routes)
	}
	if _, ok := g.apiKeyAuthorizer.Lookup(routes[0].Method, routes[0].Path); ok {
		t.Fatal("personal diagram generator must not become an API-key route")
	}
}
