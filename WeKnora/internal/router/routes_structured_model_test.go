package router

import (
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStructuredModelRouteStaysWebMemberOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	g := &rbacGuards{apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
	RegisterStructuredModelRoutes(router.Group("/api/v1"), &handler.KnowledgeHandler{}, handler.NewModelHandler(nil), g)
	g.assertAPIKeyPoliciesMatchRoutes(router)
	require.Len(t, router.Routes(), 2)
	for _, purpose := range []string{"chat", "plan"} {
		path := "/api/v1/knowledge-bases/:id/structured-model/" + purpose
		found := false
		for _, route := range router.Routes() {
			found = found || route.Path == path
		}
		require.True(t, found, path)
		_, allowed := g.apiKeyAuthorizer.Lookup(http.MethodPost, path)
		require.False(t, allowed, "these endpoints must not inherit ingest/chat capabilities from adjacent routes")
	}
}
