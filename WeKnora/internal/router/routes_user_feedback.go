package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// Feedback is a web-user operation; API keys have no capability declaration
// and are denied by the API-key authorizer.
func RegisterUserFeedbackRoutes(r *gin.RouterGroup, h *handler.UserFeedbackHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	r.POST("/feedback", g.Viewer(), h.Submit)
	admin := r.Group("/system/admin/feedback", g.SystemAdmin())
	admin.GET("", h.List)
	admin.PUT("/:id/status", h.SetStatus)
}
