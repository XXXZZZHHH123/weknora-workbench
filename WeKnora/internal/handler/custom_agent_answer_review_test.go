package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAgentAnswerReviewModeCreateUpdateAndPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		for _, mode := range []string{"", "standard", "strict", "unknown", "STANDARD"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				repo := &agentAvatarRepository{agent: patchTestAgent()}
				h := &CustomAgentHandler{service: service.NewCustomAgentService(repo, nil, nil, nil, nil, nil, nil, nil)}
				router := gin.New()
				router.Use(middleware.ErrorHandler())
				router.POST("/agents", h.CreateAgent)
				router.PUT("/agents/:id", h.UpdateAgent)
				router.PATCH("/agents/:id", h.PatchAgent)
				body := map[string]any{"name": "Review settings", "config": map[string]any{"answer_review_mode": mode}}
				if method == http.MethodPatch {
					body = map[string]any{"answer_review_mode": mode}
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				path := "/agents/custom-1"
				if method == http.MethodPost {
					path = "/agents"
				}
				request := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
				request.Header.Set("Content-Type", "application/json")
				request = request.WithContext(context.WithValue(request.Context(), types.TenantIDContextKey, uint64(1)))
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if mode == "unknown" || mode == "STANDARD" {
					require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
					require.Zero(t, repo.writes)
					return
				}
				wantStatus := http.StatusOK
				if method == http.MethodPost {
					wantStatus = http.StatusCreated
				}
				require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
				require.Equal(t, 1, repo.writes)
				require.Equal(t, mode, repo.agent.Config.AnswerReviewMode)
			})
		}
	}
}

func TestAgentAnswerReviewModeUnrelatedPatchPreservesSelectionAndScope(t *testing.T) {
	for _, mode := range []string{"", "standard", "strict"} {
		repo := &agentAvatarRepository{agent: patchTestAgent()}
		repo.agent.Config.AnswerReviewMode = mode
		// Real editor inputs have already passed GetAgent's legacy defaults.
		repo.agent.EnsureDefaults()
		before := repo.agent.Config
		response := servePatch(t, repo, `{"name":"New title"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, before, repo.agent.Config, "renaming must not alter review mode, model, permissions or source selection")
	}
}
