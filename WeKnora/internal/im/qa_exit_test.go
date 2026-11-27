package im

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type exitSessionService struct {
	interfaces.SessionService
	run func(context.Context, *event.EventBus) error
}

func (s *exitSessionService) AgentQA(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
	return s.run(ctx, bus)
}

func (s *exitSessionService) KnowledgeQA(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
	return s.run(ctx, bus)
}

type qaExitResult struct {
	answer string
	err    error
}

type imOutcomeMessageService struct {
	fullOutputMessageService
	saved       []types.Message
	interrupted *types.Message
}

func (s *imOutcomeMessageService) GetMessage(context.Context, string, string) (*types.Message, error) {
	return s.interrupted, nil
}

func (s *imOutcomeMessageService) UpdateMessage(_ context.Context, message *types.Message) error {
	s.saved = append(s.saved, *message)
	return nil
}

func TestIMTerminalStatusPersistsAndReachesAdapter(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "")
	const notice = "The named source was checked. Its original value remains unverified."
	const draft = "Unchecked draft."
	const verified = "The checked value is 42."
	const savedInterruption = "The checked value is 42. Verification of the second source was interrupted."
	const privateReceipt = "private-im-source-receipt"
	partial := event.AgentCompleteData{FinalAnswer: verified, Incomplete: &event.AgentIncompleteData{
		Reason: "endpoint_evidence_unverified", Notice: "Internal notice", PartialAnswer: true,
	}, EvidenceRefs: []interface{}{
		types.EvidenceReference{ID: "im-reading", Kind: types.EvidenceKindRawCell,
			CandidateHash: types.EvidenceCandidateHash(verified), Manifest: json.RawMessage(`{"private":"` + privateReceipt + `"}`)},
		map[string]any{"id": "untyped-navigation", "manifest": privateReceipt},
	}}
	for _, mode := range []string{"stream", "full_output", "plain"} {
		for _, scenario := range []struct {
			name, reply, saved, state string
			complete                  event.AgentCompleteData
			err                       error
			legacy                    bool
		}{
			{name: "incomplete", reply: notice, saved: notice, state: types.MessageRunStateIncomplete,
				complete: event.AgentCompleteData{Incomplete: &event.AgentIncompleteData{Reason: "endpoint_evidence_unverified", Notice: notice}}},
			{name: "interrupted", reply: imCancelledFallback, state: types.MessageRunStateInterrupted, err: context.Canceled},
			{name: "persisted_interruption", reply: savedInterruption, state: types.MessageRunStateInterrupted, err: types.ErrMessageRunInterrupted},
			{name: "success", reply: verified, saved: verified, complete: event.AgentCompleteData{FinalAnswer: verified}},
			{name: "partial_with_answer", reply: verified, saved: verified, state: types.MessageRunStateEvidencePartial,
				complete: partial},
			{name: "partial_flag_off", reply: partial.Incomplete.Notice, saved: partial.Incomplete.Notice,
				state: types.MessageRunStateIncomplete, complete: partial, legacy: true},
		} {
			t.Run(mode+"/"+scenario.name, func(t *testing.T) {
				if scenario.legacy {
					t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "false")
				}
				messageService := &imOutcomeMessageService{}
				if scenario.name == "persisted_interruption" {
					messageService.interrupted = &types.Message{ID: "assistant-message", SessionID: "session",
						RunState: types.MessageRunStateInterrupted, IsCompleted: true, Content: savedInterruption}
				}
				sessionService := &exitSessionService{run: func(ctx context.Context, bus *event.EventBus) error {
					content := draft
					if scenario.name == "success" {
						content = verified
					}
					if err := bus.Emit(ctx, event.Event{
						Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: content, Done: scenario.name == "success"},
					}); err != nil {
						return err
					}
					if scenario.err != nil {
						return scenario.err
					}
					if scenario.name == "incomplete" {
						if err := bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: "Earlier retry failed"}}); err != nil {
							return err
						}
					}
					return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: scenario.complete})
				}}
				service := &Service{messageService: messageService, sessionService: sessionService, streamManager: &fullOutputStreamManager{}}
				adapter := &fullOutputAdapter{order: &fullOutputOrder{}}
				msg := &IncomingMessage{Platform: PlatformFeishu, UserID: "reader", Content: "question"}
				session := &types.Session{ID: "session"}
				ctx := t.Context()
				agent := streamExitAgent()
				switch mode {
				case "stream":
					require.NoError(t, service.handleMessageStream(ctx, msg, session, agent, nil, nil, nil, adapter, adapter, "reader", nil))
				case "full_output":
					require.NoError(t, service.handleMessageFullOutput(ctx, msg, session, agent, nil, nil, nil, adapter, adapter, "reader", nil))
				case "plain":
					require.NoError(t, service.fallbackNonStream(ctx, msg, session, agent, nil, nil, nil, adapter, "reader", nil))
				}
				if mode == "plain" {
					require.Equal(t, scenario.reply, adapter.plainContent)
				} else {
					require.Equal(t, scenario.reply, adapter.finalContent)
				}
				if messageService.interrupted != nil {
					require.Empty(t, messageService.saved)
					require.Equal(t, savedInterruption, messageService.interrupted.Content)
					return
				}
				require.Len(t, messageService.saved, 1)
				require.True(t, messageService.saved[0].IsCompleted)
				require.Equal(t, scenario.state, messageService.saved[0].RunState)
				require.Equal(t, scenario.saved, messageService.saved[0].Content)
				if scenario.state == types.MessageRunStateEvidencePartial {
					message := messageService.saved[0]
					require.Equal(t, scenario.complete.Incomplete.Reason, message.RunReason)
					require.Equal(t, scenario.complete.Incomplete.Reason, message.ExecutionContext.EvidencePartialReason)
					require.Len(t, message.EvidenceReferences, 1)
					require.Equal(t, "im-reading", message.EvidenceReferences[0].ID)
					require.Equal(t, types.EvidenceCandidateHash(message.Content), message.EvidenceReferences[0].CandidateHash)
					public, err := json.Marshal(message)
					require.NoError(t, err)
					require.NotContains(t, string(public), privateReceipt)
					require.NotContains(t, string(public), "untyped-navigation")
				} else {
					require.Empty(t, messageService.saved[0].ExecutionContext.EvidencePartialReason)
					require.Empty(t, messageService.saved[0].EvidenceReferences)
				}
			})
		}
	}
}

// Exercise both real consumers; for runQA, render errors as fallbackNonStream does.
func startQAExitHandler(
	ctx context.Context, stream, agent bool, run func(context.Context, *event.EventBus) error,
) <-chan qaExitResult {
	service := &Service{
		sessionService: &exitSessionService{run: run}, messageService: &fullOutputMessageService{},
		streamManager: &fullOutputStreamManager{},
	}
	var customAgent *types.CustomAgent
	if agent {
		customAgent = &types.CustomAgent{Config: types.CustomAgentConfig{AgentMode: types.AgentModeSmartReasoning}}
	}
	result := make(chan qaExitResult, 1)
	go func() {
		session := &types.Session{ID: "session"}
		if stream {
			sender := &recordingStreamSender{}
			err := service.handleMessageStream(ctx,
				&IncomingMessage{Platform: PlatformFeishu, UserID: "user", Content: "question"},
				session, customAgent, nil, nil, nil, sender, nil, "user", nil)
			_, final, ended := sender.snapshot()
			if err == nil && !ended {
				err = errors.New("handler returned without ending stream")
			}
			result <- qaExitResult{answer: final, err: err}
			return
		}
		answer, err := service.runQA(ctx, session, "question", customAgent, nil, nil, nil, "user", nil)
		if err != nil {
			answer = imQAFailureReply(err)
		}
		result <- qaExitResult{answer: answer, err: err}
	}()
	return result
}

func checkQAExitResult(t *testing.T, result <-chan qaExitResult, stream, failed bool, want string) {
	t.Helper()
	// Wait for all runnable goroutines before inspecting the result. This exposes
	// premature completion without scheduler-dependent sleeps or real-time delays.
	synctest.Wait()
	select {
	case got := <-result:
		wantErr := failed && !stream
		if (got.err != nil) != wantErr || got.answer != want {
			t.Fatalf("answer=%q err=%v; want answer=%q error=%v", got.answer, got.err, want, wantErr)
		}
	default:
		t.Fatal("QA consumer did not return")
	}
}

func TestIMQAAgentReturnClosesReply(t *testing.T) {
	for _, stream := range []bool{true, false} {
		mode := "runQA"
		if stream {
			mode = "stream"
		}
		for _, tc := range []struct {
			name   string
			events []event.Event
			err    error
			want   string
			failed bool
		}{
			{name: "no events", want: imNoAnswerFallback},
			{name: "complete only", want: "completed answer", events: []event.Event{
				{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "completed answer"}},
			}},
			{name: "partial answer", want: "partial answer", events: []event.Event{
				{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "partial answer"}},
			}},
			{name: "terminal answer without complete", want: "final answer", events: []event.Event{
				{
					Type: event.EventAgentFinalAnswer,
					Data: event.AgentFinalAnswerData{Content: "final answer", Done: true},
				},
			}},
			{name: "normal completion", want: "final answer", events: []event.Event{
				{
					Type: event.EventAgentFinalAnswer,
					Data: event.AgentFinalAnswerData{Content: "final answer", Done: true},
				},
				{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "final answer"}},
			}},
			{
				name: "initialization error", err: errors.New("model unavailable"), failed: true,
				want: imQAFailureReply(errors.New("QA execution error: model unavailable")),
			},
			{
				name: "error event", failed: true,
				want: imQAFailureReply(errors.New("QA pipeline error: upstream failed")),
				events: []event.Event{
					{Type: event.EventError, Data: event.ErrorData{Error: "upstream failed"}},
				},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					started := time.Now()
					result := startQAExitHandler(ctx, stream, true, func(
						ctx context.Context, bus *event.EventBus,
					) error {
						for _, evt := range tc.events {
							if err := bus.Emit(ctx, evt); err != nil {
								return err
							}
						}
						return tc.err
					})
					checkQAExitResult(t, result, stream, tc.failed, tc.want)
					if time.Since(started) >= time.Second {
						t.Fatal("reply relied on the completion wait timeout")
					}
				})
			})
		}
	}
}

func TestIMQACompleteBeforeError(t *testing.T) {
	for _, stream := range []bool{true, false} {
		mode := "runQA"
		if stream {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				releaseError := make(chan struct{})
				result := startQAExitHandler(ctx, stream, true, func(ctx context.Context, bus *event.EventBus) error {
					if err := bus.Emit(ctx, event.Event{
						Type: event.EventAgentComplete, Data: event.AgentCompleteData{},
					}); err != nil {
						return err
					}
					// executeLoop emits Complete in its defer; Execute then emits Error,
					// and AgentQA returns nil. Hold that boundary to expose early finalization.
					select {
					case <-releaseError:
					case <-ctx.Done():
					}
					_ = bus.Emit(ctx, event.Event{
						Type: event.EventError, Data: event.ErrorData{Error: "upstream failed"},
					})
					return nil
				})
				synctest.Wait()
				select {
				case got := <-result:
					t.Fatalf("Complete ended the reply before Error: %+v", got)
				default:
				}
				close(releaseError)
				want := imQAFailureReply(errors.New("QA pipeline error: upstream failed"))
				checkQAExitResult(t, result, stream, true, want)
			})
		})
	}
}

func TestIMQAWaitsForPendingEvents(t *testing.T) {
	for _, stream := range []bool{true, false} {
		mode := "runQA"
		if stream {
			mode = "stream"
		}
		for _, agent := range []bool{true, false} {
			name := "knowledge async answer"
			if agent {
				name = "agent completion after terminal answer"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					release := make(chan struct{})
					result := startQAExitHandler(ctx, stream, agent, func(
						ctx context.Context, bus *event.EventBus,
					) error {
						answer := event.Event{
							Type: event.EventAgentFinalAnswer,
							Data: event.AgentFinalAnswerData{Content: "answer", Done: true},
						}
						if agent {
							if err := bus.Emit(ctx, answer); err != nil {
								return err
							}
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
							return bus.Emit(ctx, event.Event{
								Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "answer"},
							})
						}
						go func() {
							select {
							case <-release:
								_ = bus.Emit(ctx, answer)
							case <-ctx.Done():
							}
						}()
						return nil
					})
					synctest.Wait()
					select {
					case got := <-result:
						t.Fatalf("reply ended before pending event: %+v", got)
					default:
					}
					close(release)
					checkQAExitResult(t, result, stream, false, "answer")
				})
			})
		}
	}
}

func TestIMQACancellationStopsProducer(t *testing.T) {
	for _, stream := range []bool{true, false} {
		mode := "runQA"
		if stream {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stopped := make(chan struct{})
				result := startQAExitHandler(ctx, stream, true, func(ctx context.Context, _ *event.EventBus) error {
					<-ctx.Done()
					close(stopped)
					return ctx.Err()
				})
				synctest.Wait()
				cancel()
				synctest.Wait()
				select {
				case got := <-result:
					if stream && got.err != nil {
						t.Fatal(got.err)
					}
					if !stream && !errors.Is(got.err, context.Canceled) {
						t.Fatalf("expected cancellation, got %v", got.err)
					}
				default:
					t.Fatal("cancellation did not end the reply")
				}
				select {
				case <-stopped:
				default:
					t.Fatal("QA producer did not stop")
				}
			})
		})
	}
}
