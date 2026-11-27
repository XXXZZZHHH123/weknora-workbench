package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/collaboration"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const completionNotice = "The selected field was identified. The requested value remains unverified."
const completedReading = "Verified source reading."

func emitConsumerCompletion(ctx context.Context, req *types.QARequest, bus *event.EventBus, incomplete bool) error {
	data := event.AgentCompleteData{MessageID: req.AssistantMessageID, FinalAnswer: completedReading}
	if incomplete {
		if err := bus.Emit(ctx, event.Event{
			Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "Unchecked draft."},
		}); err != nil {
			return err
		}
		data.FinalAnswer = ""
		data.Incomplete = &event.AgentIncompleteData{Reason: "endpoint_evidence_unverified", Notice: completionNotice}
	}
	return complete(ctx, bus, data)
}

func TestAutomationTypedCompletionPersistsFailureAndNotifies(t *testing.T) {
	for _, outcome := range []string{"incomplete", "complete"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAutomationFixture(t)
			a := f.create(t, types.AutomationPatch{NotifyOnSuccess: ptr(true)})
			f.sessions.run = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
				return emitConsumerCompletion(ctx, req, bus, outcome == "incomplete")
			}
			queued, err := f.svc.RunNow(f.ctx, a.ID)
			require.NoError(t, err)
			run := f.settleRun(t, queued.ID)
			message, err := f.messages.GetMessage(f.ctx, run.SessionID, run.AssistantMessageID)
			require.NoError(t, err)
			require.True(t, message.IsCompleted)
			var notifications []types.Notification
			require.NoError(t, f.db.Where("source_id = ?", a.ID).Find(&notifications).Error)
			require.Len(t, notifications, 1)
			if outcome == "incomplete" {
				require.Equal(t, types.AutomationRunFailed, run.Status)
				require.Equal(t, completionNotice, run.Error)
				require.Equal(t, types.MessageRunStateIncomplete, message.RunState)
				require.Equal(t, completionNotice, message.Content)
				require.Equal(t, types.NotificationAutomationFailed, notifications[0].Kind)
				require.Equal(t, completionNotice, notifications[0].Body)
			} else {
				require.Equal(t, types.AutomationRunSucceeded, run.Status)
				require.Equal(t, completedReading, run.Answer)
				require.Empty(t, message.RunState)
				require.Equal(t, completedReading, message.Content)
				require.Equal(t, types.NotificationAutomationSucceeded, notifications[0].Kind)
				require.Equal(t, completedReading, notifications[0].Body)
			}
		})
	}
}

func TestCollaborationTypedCompletionPersistsFailureAndPublishesMailbox(t *testing.T) {
	for _, outcome := range []string{"incomplete", "complete"} {
		t.Run(outcome, func(t *testing.T) {
			s, _, messages, ctx := collaborationService(t)
			scope := collaboration.Scope{TenantID: 7, UserID: "u", SessionID: "parent", ID: "answer"}
			require.NoError(t, s.Runtime.Store.Ensure(ctx, scope))
			config, err := s.resolve(ctx, "a", types.AssistantAccessReadOnly)
			require.NoError(t, err)
			s.Bind(&collaborationSessions{run: func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
				return emitConsumerCompletion(ctx, req, bus, outcome == "incomplete")
			}})
			_, err = s.Runtime.Spawn(ctx, scope, "spawn", "child", "a", config, "Check the selected source.")
			require.NoError(t, err)
			s.Runtime.Poll = 5 * time.Millisecond
			startCollaborationTestRuntime(ctx, t, s.Runtime)
			wantKind := "succeeded"
			if outcome == "incomplete" {
				wantKind = "failed"
			}
			var finished *collaboration.Group
			require.Eventually(t, func() bool {
				group, err := s.Runtime.Store.Read(ctx, scope)
				if err != nil || len(group.Events) == 0 || group.Members[0].Runs[0].State != wantKind {
					return false
				}
				finished = group
				return true
			}, 3*time.Second, 5*time.Millisecond)
			mail := finished.Events[len(finished.Events)-1]
			require.Equal(t, wantKind, mail.Kind)
			var payload struct {
				Result    collaboration.Result `json:"result"`
				SessionID string               `json:"session_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(mail.Content), &payload))
			message, err := messages.GetMessage(ctx, payload.SessionID, payload.Result.MessageID)
			require.NoError(t, err)
			require.True(t, message.IsCompleted)
			if outcome == "incomplete" {
				require.Equal(t, completionNotice, payload.Result.Error)
				require.NotContains(t, mail.Content, "Unchecked draft.")
				require.Equal(t, completionNotice, message.Content)
				require.Equal(t, types.MessageRunStateIncomplete, message.RunState)
			} else {
				require.Empty(t, payload.Result.Error)
				require.Equal(t, completedReading, payload.Result.Answer)
				require.Equal(t, completedReading, message.Content)
				require.Empty(t, message.RunState)
			}
		})
	}
}
