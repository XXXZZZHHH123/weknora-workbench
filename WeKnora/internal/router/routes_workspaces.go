package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

func RegisterWorkspaceRoutes(r *gin.RouterGroup, h *handler.WorkspaceHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	r.GET("/workspaces/default-knowledge-base", g.Viewer(), h.GetDefaultKnowledgeBase)
	r.POST("/workspaces/default-knowledge-base", g.Contributor(), h.InitializeDefaultKnowledgeBase)
	r.GET("/workspaces/central-datasets", g.Viewer(), h.ListCentralDatasets)
	r.POST("/workspaces/central-datasets/default", g.Admin(), h.InitializeCentralDataset)
	// Personal navigation is intentionally unavailable to API keys: no capability
	// declaration means the existing API-key middleware denies these endpoints.
	projects := r.Group("/workspaces/projects", g.Viewer())
	projects.GET("", h.ListProjects)
	projects.POST("", h.CreateProject)
	projects.GET("/:id", h.GetProject)
	projects.PUT("/:id", h.UpdateProject)
	projects.DELETE("/:id", h.DeleteProject)
	projects.GET("/:id/sessions", h.ProjectSessions)
	projects.PUT("/:id/sessions/:session_id", h.AssignSession)
	projects.DELETE("/:id/sessions/:session_id", h.RemoveSession)
	workbenches := r.Group("/workspaces/research", g.Viewer())
	workbenches.GET("", h.ListWorkbenches)
	workbenches.GET("/templates", h.ListWorkbenchTemplates)
	workbenches.GET("/:id", h.GetWorkbench)
	workbenches.PUT("/:id/pin", h.SetWorkbenchPin)
	workbenches.GET("/:id/permissions", h.GetWorkbenchPermissions)
	workbenches.PUT("/:id/permissions", g.Contributor(), h.UpdateWorkbenchPermissions)
	workbenches.GET("/:id/source-scope", h.GetWorkbenchSourceScope)
	workbenches.GET("/:id/sources", h.GetWorkbenchSources)
	workbenches.PUT("/:id/sources", g.Contributor(), h.UpdateWorkbenchSources)
	workbenches.GET("/:id/sources/:source_binding_id/sharing", h.GetSourceBindingSharing)
	workbenches.PUT("/:id/sources/:source_binding_id/sharing", g.Contributor(), h.UpdateSourceBindingSharing)
	workbenches.POST("/:id/sources/:source_binding_id/sharing/request", g.Contributor(), h.RequestSourceBindingSharing)
	workbenches.GET("/:id/source-sharing", h.GetWorkbenchSourceSharing)
	workbenches.PUT("/:id/source-sharing", g.Contributor(), h.UpdateWorkbenchSourceSharing)
	workbenches.POST("/:id/source-sharing/request", g.Contributor(), h.RequestWorkbenchSourceSharing)
	workbenches.POST("", g.Contributor(), h.CreateWorkbench)
	workbenches.PUT("/:id", g.Contributor(), h.UpdateWorkbench)
	workbenches.PUT("/:id/onboarding", h.UpdateWorkbenchOnboarding)
	workbenches.DELETE("/:id", g.Contributor(), h.DeleteWorkbench)
}

// The generation draft is a personal workspace operation; API-key access
// remains denied by the existing authorizer's undeclared-route policy.
func RegisterWorkspaceDiagramRoutes(r *gin.RouterGroup, h *handler.WorkspaceHandler, models *handler.ModelHandler, g *rbacGuards) {
	if h == nil || models == nil {
		return
	}
	r.POST("/workspaces/research/:id/diagram/generate", g.Contributor(), handler.WorkspaceDiagramGenerator(h, models))
}

// Judgment analysis is a separate fixed purpose and remains web-user/contributor only.
func RegisterWorkspaceJudgmentRoutes(r *gin.RouterGroup, h *handler.WorkspaceHandler, models *handler.ModelHandler, g *rbacGuards) {
	if h == nil || models == nil {
		return
	}
	r.POST("/workspaces/research/:id/judgment/generate", g.Contributor(), handler.WorkspaceJudgmentGenerator(h, models))
}

func RegisterWorkspaceStrategyRoutes(r *gin.RouterGroup, h *handler.WorkspaceHandler, models *handler.ModelHandler, g *rbacGuards) {
	if h == nil || models == nil {
		return
	}
	r.POST("/workspaces/research/:id/strategy/generate", g.Contributor(), handler.WorkspaceStrategyGenerator(h, models))
}
