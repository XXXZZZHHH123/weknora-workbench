package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

func RegisterStructuredModelRoutes(r *gin.RouterGroup, knowledge *handler.KnowledgeHandler, models *handler.ModelHandler, g *rbacGuards) {
	// This UI-only helper deliberately has no API-key capability declaration.
	// Machine principals remain default-denied by APIKeyGate and the handler.
	r.POST("/knowledge-bases/:id/structured-model/chat", g.Contributor(), handler.StructuredModelChat(knowledge, models))
	r.POST("/knowledge-bases/:id/structured-model/plan", g.Viewer(), handler.StructuredModelPlan(knowledge, models))
}
