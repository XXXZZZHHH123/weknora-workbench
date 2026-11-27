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
const partialReading = "Register.xlsx records 42 in B15. The report value remains unverified."
const partialReason = "endpoint_evidence_unverified"
const privateReceipt = "private-consumer-receipt"

func emitConsumerCompletion(ctx context.Context, req *types.QARequest, bus *event.EventBus, outcome string) error {
	data := event.AgentCompleteData{MessageID: req.AssistantMessageID, FinalAnswer: completedReading}
	if outcome != "complete" {
		if err := bus.Emit(ctx, event.Event{
			Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "Unchecked draft."},
		}); err != nil {
			return err
		}
		data.FinalAnswer = ""
		data.Incomplete = &event.AgentIncompleteData{Reason: partialReason, Notice: completionNotice}
		if outcome == "partial" || outcome == "partial flag off" {
			data.FinalAnswer = partialReading
			data.Incomplete.PartialAnswer = true
			data.EvidenceRefs = []interface{}{types.EvidenceReference{
				ID: "reading", Kind: types.EvidenceKindRawCell, CandidateHash: types.EvidenceCandidateHash(partialReading),
				Manifest: json.RawMessage(`{"private":"` + privateReceipt + `"}`),
			}}
		}
	}
	return complete(ctx, bus, data)
}

func requireConsumerPartialMessage(t *testing.T, message *types.Message) {
	t.Helper()
	require.Equal(t, partialReading, message.Content)
	require.Equal(t, types.MessageRunStateEvidencePartial, message.RunState)
	require.Equal(t, partialReason, message.RunReason)
	require.Equal(t, partialReason, message.ExecutionContext.EvidencePartialReason)
	require.Len(t, message.EvidenceReferences, 1)
	require.Equal(t, types.EvidenceCandidateHash(message.Content), message.EvidenceReferences[0].CandidateHash)
	require.Contains(t, string(message.EvidenceReferences[0].Manifest), privateReceipt)
	public, err := json.Marshal(message)
	require.NoError(t, err)
	require.NotContains(t, string(public), privateReceipt)
	require.NotContains(t, string(public), "Unchecked draft.")
	require.NotContains(t, string(public), "<evidence")
}

func TestAutomationTypedCompletionPersistsFailureAndNotifies(t *testing.T) {
	for _, outcome := range []string{"incomplete", "complete", "partial", "partial flag off"} {
		t.Run(outcome, func(t *testing.T) {
			if outcome == "partial" {
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
			} else if outcome == "partial flag off" {
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
			}
			f := newAutomationFixture(t)
			a := f.create(t, types.AutomationPatch{NotifyOnSuccess: ptr(true)})
			f.sessions.run = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
				return emitConsumerCompletion(ctx, req, bus, outcome)
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
			switch outcome {
			case "incomplete", "partial flag off":
				require.Equal(t, types.AutomationRunFailed, run.Status)
				require.Equal(t, completionNotice, run.Error)
				require.Equal(t, types.MessageRunStateIncomplete, message.RunState)
				require.Empty(t, message.ExecutionContext.EvidencePartialReason)
				require.Equal(t, completionNotice, message.Content)
				require.Equal(t, types.NotificationAutomationFailed, notifications[0].Kind)
				require.Equal(t, completionNotice, notifications[0].Body)
			case "partial":
				requireConsumerPartialMessage(t, message)
				require.Equal(t, types.AutomationRunFailed, run.Status)
				require.Equal(t, partialReason, run.Error)
				require.Equal(t, partialReading, run.Answer)
				require.Equal(t, types.NotificationAutomationFailed, notifications[0].Kind)
				require.Equal(t, partialReason, notifications[0].Body)
			default:
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
	for _, outcome := range []string{"incomplete", "complete", "partial", "partial flag off"} {
		t.Run(outcome, func(t *testing.T) {
			if outcome == "partial" {
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
			} else if outcome == "partial flag off" {
				t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
			}
			s, _, messages, ctx := collaborationService(t)
			scope := collaboration.Scope{TenantID: 7, UserID: "u", SessionID: "parent", ID: "answer"}
			require.NoError(t, s.Runtime.Store.Ensure(ctx, scope))
			config, err := s.resolve(ctx, "a", types.AssistantAccessReadOnly)
			require.NoError(t, err)
			s.Bind(&collaborationSessions{run: func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
				return emitConsumerCompletion(ctx, req, bus, outcome)
			}})
			_, err = s.Runtime.Spawn(ctx, scope, "spawn", "child", "a", config, "Check the selected source.")
			require.NoError(t, err)
			s.Runtime.Poll = 5 * time.Millisecond
			startCollaborationTestRuntime(ctx, t, s.Runtime)
			wantKind := "succeeded"
			if outcome != "complete" {
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
			switch outcome {
			case "incomplete", "partial flag off":
				require.Equal(t, completionNotice, payload.Result.Error)
				require.NotContains(t, mail.Content, "Unchecked draft.")
				require.Equal(t, completionNotice, message.Content)
				require.Equal(t, types.MessageRunStateIncomplete, message.RunState)
				require.Empty(t, message.ExecutionContext.EvidencePartialReason)
			case "partial":
				requireConsumerPartialMessage(t, message)
				require.Equal(t, partialReading, payload.Result.Answer)
				require.Equal(t, partialReason, payload.Result.Error)
				for _, private := range []string{privateReceipt, completionNotice, "Unchecked draft.", "<evidence"} {
					require.NotContains(t, mail.Content, private)
				}
			default:
				require.Empty(t, payload.Result.Error)
				require.Equal(t, completedReading, payload.Result.Answer)
				require.Equal(t, completedReading, message.Content)
				require.Empty(t, message.RunState)
			}
		})
	}
}
