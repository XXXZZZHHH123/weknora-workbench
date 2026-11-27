package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type incompleteReplayMessageService struct {
	interfaces.MessageService
	message *types.Message
}

func (s *incompleteReplayMessageService) GetMessage(context.Context, string, string) (*types.Message, error) {
	return s.message, nil
}

func TestContinueStreamReplaysSavedIncompleteStatusWithoutAnAnswer(t *testing.T) {
	const notice = "The selected table was found. The requested period remains unverified."
	const stale = "The previous draft cannot certify the requested value."
	for _, scenario := range []struct {
		name   string
		cached []interfaces.StreamEvent
		code   string
	}{
		{name: "no_cache", code: types.MessageRunStateIncomplete},
		{name: "cached_draft_without_complete", code: types.MessageRunStateIncomplete,
			cached: []interfaces.StreamEvent{{ID: "draft", Type: types.ResponseTypeAnswer, Content: stale}}},
		{name: "cached_complete_without_answer", code: types.MessageRunStateIncomplete,
			cached: []interfaces.StreamEvent{{ID: "done", Type: types.ResponseTypeComplete, Data: map[string]any{"success": true}}}},
		{name: "cached_draft_and_complete", code: "endpoint_evidence_unverified",
			cached: []interfaces.StreamEvent{
				{ID: "draft", Type: types.ResponseTypeAnswer, Content: stale},
				{ID: "error", Type: types.ResponseTypeError, Content: "obsolete status", Data: map[string]any{"run_state": types.MessageRunStateIncomplete, "code": "endpoint_evidence_unverified"}},
				{ID: "done", Type: types.ResponseTypeComplete, Content: stale, Data: map[string]any{"success": true, "final_content": stale}},
			}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			message := &types.Message{ID: "m", SessionID: "s", RequestID: "r", IsCompleted: true, RunState: types.MessageRunStateIncomplete, Content: notice}
			handler := &Handler{sessionService: &stubSessionService{}, messageService: &incompleteReplayMessageService{message: message},
				streamManager: &stubStreamManager{events: scenario.cached}}
			router := gin.New()
			router.GET("/replay/:session_id", handler.ContinueStream)
			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/replay/s?message_id=m", nil))
			require.Equal(t, http.StatusOK, writer.Code)
			require.NotContains(t, writer.Body.String(), stale)
			var frames []types.StreamResponse
			for _, line := range strings.Split(writer.Body.String(), "\n") {
				if encoded, ok := strings.CutPrefix(line, "data:"); ok {
					var frame types.StreamResponse
					require.NoError(t, json.Unmarshal([]byte(encoded), &frame))
					frames = append(frames, frame)
				}
			}
			require.Len(t, frames, 2)
			require.Equal(t, types.ResponseTypeError, frames[0].ResponseType)
			require.Equal(t, types.ResponseTypeComplete, frames[1].ResponseType)
			for _, frame := range frames {
				require.True(t, frame.Done)
				require.Equal(t, false, frame.Data["success"])
				require.Equal(t, types.MessageRunStateIncomplete, frame.Data["run_state"])
				require.Equal(t, notice, frame.Data["error"])
				require.Equal(t, scenario.code, frame.Data["code"])
			}
			require.Empty(t, frames[1].Content)
			require.Equal(t, "", frames[1].Data["final_content"])
			if scenario.name == "cached_draft_and_complete" {
				require.Equal(t, stale, scenario.cached[2].Data["final_content"])
				require.Equal(t, true, scenario.cached[2].Data["success"])
			}
		})
	}
}
