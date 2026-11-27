package session

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/storageurl"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ContinueStream godoc
// @Summary      继续流式响应
// @Description  继续获取正在进行的流式响应
// @Tags         问答
// @Accept       json
// @Produce      text/event-stream
// @Param        session_id     path      string  true   "会话ID"
// @Param        message_id     query     string  true   "消息ID"
// @Param        resume_pending_id query  string  false  "等待此审批恢复后的事件流"
// @Param        resource_urls  query     string  false  "文件引用形式，public 返回可加载直链"  Enums(handle, public)  default(handle)
// @Success      200            {string}  string  "SSE 事件流"
// @Failure      404         {object}  errors.AppError         "会话或消息不存在"
// @Failure      409         {object}  errors.AppError         "审批恢复后的事件流尚未就绪"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sessions/continue-stream/{session_id} [get]
func (h *Handler) ContinueStream(c *gin.Context) {
	ctx := c.Request.Context()

	logger.Info(ctx, "Start continuing stream response processing")

	// Get session ID from URL parameter
	sessionID := secutils.SanitizeForLog(c.Param("session_id"))
	if sessionID == "" {
		logger.Error(ctx, "Session ID is empty")
		c.Error(errors.NewBadRequestError(errors.ErrInvalidSessionID.Error()))
		return
	}

	// Get message ID from query parameter
	messageID := secutils.SanitizeForLog(c.Query("message_id"))
	if messageID == "" {
		logger.Error(ctx, "Message ID is empty")
		c.Error(errors.NewBadRequestError("Missing message ID"))
		return
	}

	logger.Infof(ctx, "Continuing stream, session ID: %s, message ID: %s", sessionID, messageID)

	// Resolve before any SSE header is written so an invalid resource_urls value
	// is still reportable as a normal 400 JSON error.
	resourceRewriter, err := h.resolveStreamRewriter(c)
	if err != nil {
		logger.Warnf(ctx, "Rejected resource URL mode: %v", err)
		_ = c.Error(err)
		return
	}

	// Verify that the session exists and belongs to this tenant
	if _, err := h.sessionService.GetSession(ctx, sessionID); err != nil {
		if stderrors.Is(err, errors.ErrSessionNotFound) {
			logger.Warnf(ctx, "Session not found, ID: %s", sessionID)
			c.Error(errors.NewNotFoundError(err.Error()))
		} else {
			logger.ErrorWithFields(ctx, err, nil)
			c.Error(errors.NewInternalServerError(err.Error()))
		}
		return
	}

	// Get the incomplete message
	message, err := h.messageService.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		if stderrors.Is(err, errors.ErrSessionNotFound) {
			// PR #1309 plumbed user-scope into messageService.GetMessage's
			// session existence check; non-owner / wrong-user lookups now
			// surface as ErrSessionNotFound. Map to 404 so clients can tell
			// "wrong URL" from a real 5xx instead of seeing a generic 500.
			logger.Warnf(ctx, "Session not found, ID: %s", sessionID)
			c.Error(errors.NewNotFoundError(err.Error()))
			return
		}
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			// The message_id doesn't exist (e.g. a wrong / non-persisted id, or an
			// expired replay buffer). That is a client error, not a server fault:
			// return 404 so callers read resource.not_found (a permanent condition
			// they must not retry) instead of a retryable 5xx. Mirrors the
			// ErrSessionNotFound branch above and the kb/doc/chunk not-found fix.
			logger.Warnf(ctx, "Message not found, session ID: %s, message ID: %s", sessionID, messageID)
			c.Error(errors.NewNotFoundError(err.Error()))
			return
		}
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}

	if message == nil {
		logger.Warnf(ctx, "Incomplete message not found, session ID: %s, message ID: %s", sessionID, messageID)
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   "Incomplete message not found",
		})
		return
	}

	// Get initial events from stream (offset 0)
	events, currentOffset, err := h.streamManager.GetEvents(ctx, sessionID, messageID, 0)
	publicMessage := types.PublicAgentMessage(message)
	publication := agentReplayPublication{message: message, public: publicMessage}
	if message.IsCompleted && message.RunState == types.MessageRunStateIncomplete {
		if err != nil {
			logger.Warnf(ctx, "Replaying saved incomplete status without stream cache: %v", err)
		}
		setSSEHeaders(c)
		for _, evt := range incompleteReplayEvents(events, message) {
			emitStreamEvent(ctx, c, evt, message.RequestID, resourceRewriter)
		}
		return
	}
	if message.IsCompleted && message.RunState == types.MessageRunStateEvidencePartial && (err != nil ||
		!slices.ContainsFunc(events, func(evt interfaces.StreamEvent) bool {
			return evt.Type == types.ResponseTypeComplete && evt.Data["run_state"] == types.MessageRunStateEvidencePartial
		})) {
		setSSEHeaders(c)
		for _, evt := range coalesceReplayEvents(events) {
			switch evt.Type {
			case types.ResponseTypeAnswer, types.ResponseTypeError, types.ResponseTypeComplete:
				continue
			}
			emitStreamEvent(ctx, c, evt, message.RequestID, resourceRewriter)
		}
		for _, evt := range savedPartialAnswerEvents(publicMessage) {
			emitStreamEvent(ctx, c, evt, message.RequestID, resourceRewriter)
		}
		return
	}
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(errors.NewInternalServerError(fmt.Sprintf("Failed to get stream data: %s", err.Error())))
		return
	}

	if len(events) == 0 {
		logger.Warnf(ctx, "No events found in stream, session ID: %s, message ID: %s", sessionID, messageID)
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   "No stream events found",
		})
		return
	}

	// A scheduler may not have reset the previous segment yet. Never replay
	// its terminal event as if the just-approved continuation had completed.
	if pendingID := c.Query("resume_pending_id"); pendingID != "" && !message.IsCompleted {
		ready := false
		for _, evt := range events {
			if (evt.Type == types.ResponseTypeAgentQuery && evt.Data["resume_pending_id"] == pendingID) ||
				(evt.Type == types.ResponseTypeRunResumed && evt.Data["pending_id"] == pendingID) {
				ready = true
				break
			}
		}
		if !ready {
			c.JSON(http.StatusConflict, gin.H{"success": false, "error": "The resumed stream is not ready yet"})
			return
		}
	}

	logger.Infof(
		ctx, "Preparing to replay %d events and continue streaming, session ID: %s, message ID: %s",
		len(events), sessionID, messageID,
	)

	// Set headers for SSE
	setSSEHeaders(c)

	// Check if stream is already completed
	streamCompleted := false
	for _, evt := range events {
		if evt.Type == "complete" {
			streamCompleted = true
			break
		}
	}

	// Replay existing events, a segment's worth of chunks per frame
	replay := coalesceReplayEvents(events)
	logger.Debugf(ctx, "Replaying %d existing events as %d frames", len(events), len(replay))
	emitReplay := func(evt interfaces.StreamEvent) {
		evt = interruptedReplayEvent(ctx, evt, message)
		if publication.savedLegacyAnswer() {
			if evt.Type == types.ResponseTypeAnswer {
				return
			}
			if evt.Type == types.ResponseTypeComplete && evt.Data["suspended"] == nil && message.RunState != types.MessageRunStateInterrupted {
				answerID := message.ID + "-saved-answer"
				emitStreamEvent(ctx, c, interfaces.StreamEvent{ID: answerID, Type: types.ResponseTypeAnswer,
					Content: publicMessage.Content, Done: true, Timestamp: message.UpdatedAt,
					Data: map[string]any{"event_id": answerID}}, message.RequestID, resourceRewriter)
			}
		}
		emitStreamEvent(ctx, c, publication.project(evt), message.RequestID, resourceRewriter)
	}
	for _, evt := range replay {
		if evt.Type == types.ResponseTypeAnswer && (message.RunState == types.MessageRunStateInterrupted || message.RunState == types.MessageRunStateIncomplete) {
			continue // Replay the saved verified response instead of an abandoned draft.
		}
		emitReplay(evt)
	}

	// If stream is already completed, send final event and return
	if streamCompleted {
		logger.Infof(ctx, "Stream already completed, session ID: %s, message ID: %s", sessionID, messageID)
		sendCompletionEvent(c, message.RequestID)
		return
	}

	// Continue polling for new events
	logger.Debug(ctx, "Starting event update monitoring")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			logger.Debug(ctx, "Client connection closed")
			return

		case <-heartbeat.C:
			c.SSEvent("message", gin.H{"response_type": "heartbeat"})
			c.Writer.Flush()
		case <-ticker.C:
			// Get new events from current offset
			newEvents, newOffset, err := h.streamManager.GetEvents(ctx, sessionID, messageID, currentOffset)
			if err != nil {
				logger.Errorf(ctx, "Failed to get new events: %v", err)
				flushHeldStreamContent(ctx, c, message.RequestID, resourceRewriter)
				return
			}

			// Send new events
			streamCompletedNow := false
			for _, evt := range newEvents {
				// Check for completion event
				if evt.Type == "complete" {
					streamCompletedNow = true
				}

				emitReplay(evt)
			}

			// Update offset
			currentOffset = newOffset

			// If stream completed, send final event and exit
			if streamCompletedNow {
				logger.Infof(ctx, "Stream completed, session ID: %s, message ID: %s", sessionID, messageID)
				sendCompletionEvent(c, message.RequestID)
				return
			}
		}
	}
}

type agentReplayPublication struct {
	message, public *types.Message
	streams         map[string]*types.AgentAnswerStream
	agent           bool
}

func (p *agentReplayPublication) savedLegacyAnswer() bool {
	return p.message.IsCompleted && p.message.Content != p.public.Content
}

func (p *agentReplayPublication) project(evt interfaces.StreamEvent) interfaces.StreamEvent {
	message, publicMessage := p.message, p.public
	p.agent = p.agent || evt.Type == types.ResponseTypeAgentQuery
	if !types.VerifiedPublicationEnabled() || (!p.agent && message.AgentID == "" && len(message.AgentSteps) == 0 && len(message.EvidenceReferences) == 0) {
		return evt
	}
	switch evt.Type {
	case types.ResponseTypeAnswer:
		if evt.Content != "" && evt.Content == message.Content {
			evt.Content = publicMessage.Content
			break
		}
		if p.streams == nil {
			p.streams = make(map[string]*types.AgentAnswerStream)
		}
		stream := p.streams[evt.ID]
		if stream == nil || evt.Data["replace"] == true {
			stream = &types.AgentAnswerStream{}
			p.streams[evt.ID] = stream
		}
		evt.Content = stream.Feed(evt.Content)
		if evt.Done {
			evt.Content += stream.Finish()
			delete(p.streams, evt.ID)
		}
	case types.ResponseTypeComplete:
		if types.HasAgentAnswerMarkup(evt.Content) {
			evt.Content = types.StripAgentAnswerMarkup(evt.Content)
		}
		if content, _ := evt.Data["final_content"].(string); (p.savedLegacyAnswer() && evt.Data["suspended"] == nil) || types.HasAgentAnswerMarkup(content) {
			evt.Data = copyEventData(evt.Data, 0)
			if p.savedLegacyAnswer() && evt.Data["suspended"] == nil || content == message.Content {
				evt.Data["final_content"] = publicMessage.Content
				if publicMessage.EvidenceReferences != nil {
					evt.Data["evidence_references"] = publicEventEvidenceReferences(publicMessage.EvidenceReferences)
				}
			} else {
				evt.Data["final_content"] = types.StripAgentAnswerMarkup(content)
			}
		}
		if content, _ := evt.Data["final_answer"].(string); types.HasAgentAnswerMarkup(content) {
			evt.Data = copyEventData(evt.Data, 0)
			evt.Data["final_answer"] = types.StripAgentAnswerMarkup(content)
		}
	}
	return evt
}

func savedPartialAnswerEvents(message *types.Message) []interfaces.StreamEvent {
	answerID := message.ID + "-partial-answer"
	completeData := map[string]any{
		"run_state": message.RunState, "code": message.RunReason, "final_content": message.Content,
		"knowledge_references": message.KnowledgeReferences,
		"evidence_references":  publicEventEvidenceReferences(message.EvidenceReferences),
	}
	if len(message.Artifacts) > 0 {
		completeData["artifacts"] = publicArtifactViews(message.Artifacts)
	}
	return []interfaces.StreamEvent{
		{ID: answerID, Type: types.ResponseTypeAnswer, Content: message.Content, Done: true, Timestamp: message.UpdatedAt,
			Data: map[string]any{"event_id": answerID}},
		{ID: message.ID + "-partial-complete", Type: types.ResponseTypeComplete, Done: true, Data: completeData, Timestamp: message.UpdatedAt, Usage: message.Usage},
	}
}

// The saved terminal status is authoritative if Redis still has an optimistic
// draft, a stale completion, or no events from this run.
func incompleteReplayEvents(events []interfaces.StreamEvent, message *types.Message) []interfaces.StreamEvent {
	var replay []interfaces.StreamEvent
	complete := interfaces.StreamEvent{ID: message.ID + "-incomplete-complete", Type: types.ResponseTypeComplete, Timestamp: message.UpdatedAt}
	code := types.MessageRunStateIncomplete
	for _, evt := range coalesceReplayEvents(events) {
		if evt.Data["run_state"] == types.MessageRunStateIncomplete {
			if received, ok := evt.Data["code"].(string); ok && received != "" {
				code = received
			}
		}
		switch evt.Type {
		case types.ResponseTypeAnswer, types.ResponseTypeError:
			continue
		case types.ResponseTypeComplete:
			complete = evt
		default:
			replay = append(replay, evt)
		}
	}
	errorData := map[string]any{
		"run_state": types.MessageRunStateIncomplete, "success": false, "error": message.Content, "code": code,
	}
	failure := interfaces.StreamEvent{
		ID: message.ID + "-incomplete-error", Type: types.ResponseTypeError, Content: message.Content,
		Done: true, Data: errorData, Timestamp: message.UpdatedAt,
	}
	data := copyEventData(complete.Data, len(errorData)+1)
	for key, value := range errorData {
		data[key] = value
	}
	data["final_content"] = ""
	delete(data, "final_answer")
	complete.Content, complete.Done, complete.Data = "", true, data
	return append(replay, failure, complete)
}

func interruptedReplayEvent(ctx context.Context, evt interfaces.StreamEvent, message *types.Message) interfaces.StreamEvent {
	if evt.Type != types.ResponseTypeError && evt.Type != types.ResponseTypeComplete || evt.Data["run_state"] == types.MessageRunStateIncomplete {
		return evt
	}
	deadline, legacy := agent.LegacyInterruptedAnswer(evt.Content)
	if !legacy {
		if content, _ := evt.Data["final_content"].(string); content != "" {
			deadline, legacy = agent.LegacyInterruptedAnswer(content)
		}
	}
	if !legacy {
		return evt
	}
	content := ""
	if message != nil && message.RunState == types.MessageRunStateInterrupted {
		if _, old := agent.LegacyInterruptedAnswer(message.Content); !old {
			content = message.Content
		}
	}
	if content == "" {
		content = agent.RenderInterruptedEvidence(ctx, nil, nil, deadline, agent.InterruptedProgressFromMessage(message))
	}
	code := "agent_run_interrupted"
	if deadline {
		code = "agent_run_deadline_exceeded"
	}
	data := make(map[string]any, len(evt.Data)+4)
	for key, value := range evt.Data {
		data[key] = value
	}
	data["error"], data["final_content"], data["code"] = content, content, code
	data["run_state"], data["success"] = types.MessageRunStateInterrupted, false
	evt.Content, evt.Data = content, data
	return evt
}

// StopSession godoc
// @Summary      停止生成
// @Description  停止当前正在进行的生成任务
// @Tags         问答
// @Accept       json
// @Produce      json
// @Param        session_id  path      string              true  "会话ID"
// @Param        request     body      StopSessionRequest  true  "停止请求"
// @Success      200         {object}  map[string]interface{}  "停止成功"
// @Failure      404         {object}  errors.AppError         "会话或消息不存在"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /sessions/{session_id}/stop [post]
func (h *Handler) StopSession(c *gin.Context) {
	ctx := logger.CloneContext(c.Request.Context())
	sessionID := secutils.SanitizeForLog(c.Param("session_id"))

	if sessionID == "" {
		c.JSON(400, gin.H{"error": "Session ID is required"})
		return
	}

	// Parse request body to get message_id
	var req StopSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"session_id": sessionID,
		})
		c.JSON(400, gin.H{"error": "message_id is required"})
		return
	}

	assistantMessageID := secutils.SanitizeForLog(req.MessageID)
	logger.Infof(ctx, "Stop generation request for session: %s, message: %s", sessionID, assistantMessageID)

	// Get tenant ID from context
	tenantID, exists := c.Get(types.TenantIDContextKey.String())
	if !exists {
		logger.Error(ctx, "Failed to get tenant ID")
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	tenantIDUint := tenantID.(uint64)

	// Verify message ownership and status
	message, err := h.messageService.GetMessage(ctx, sessionID, assistantMessageID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"session_id": sessionID,
			"message_id": assistantMessageID,
		})
		c.JSON(404, gin.H{"error": "Message not found"})
		return
	}

	// Verify message belongs to this session (double check)
	if message.SessionID != sessionID {
		logger.Warnf(ctx, "Message %s does not belong to session %s", assistantMessageID, sessionID)
		c.JSON(403, gin.H{"error": "Message does not belong to this session"})
		return
	}

	// Verify message belongs to the current tenant. Stopping generation mutates
	// an in-flight run, so use the strict owner scope: a tenant admin may read an
	// API-key session but must not be able to interrupt its (external) API calls.
	session, err := h.sessionService.GetOwnedSession(ctx, sessionID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"session_id": sessionID,
		})
		c.JSON(404, gin.H{"error": "Session not found"})
		return
	}

	if session.TenantID != tenantIDUint {
		logger.Warnf(ctx, "Session %s does not belong to tenant %d", sessionID, tenantIDUint)
		c.JSON(403, gin.H{"error": "Access denied"})
		return
	}

	if h.stopCollaboration != nil {
		if err := h.stopCollaboration(ctx, sessionID, assistantMessageID); err != nil {
			c.JSON(500, gin.H{"error": "Failed to stop delegated tasks"})
			return
		}
	}

	// Check if message is already completed (stopped)
	if message.IsCompleted {
		logger.Infof(ctx, "Message %s is already completed, no need to stop", assistantMessageID)
		c.JSON(200, gin.H{
			"success": true,
			"message": "Message already completed",
		})
		return
	}

	// Write stop event to StreamManager for distributed support
	stopEvent := interfaces.StreamEvent{
		ID:        fmt.Sprintf("stop-%d", time.Now().UnixNano()),
		Type:      types.ResponseType(event.EventStop),
		Content:   "",
		Done:      true,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"session_id": sessionID,
			"message_id": assistantMessageID,
			"reason":     "user_requested",
		},
	}

	if err := h.streamManager.AppendEvent(ctx, sessionID, assistantMessageID, stopEvent); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"session_id": sessionID,
			"message_id": assistantMessageID,
		})
		c.JSON(500, gin.H{"error": "Failed to write stop event"})
		return
	}

	logger.Infof(ctx, "Stop event written successfully for session: %s, message: %s", sessionID, assistantMessageID)
	h.notifyBackgroundStop(ctx, session)
	c.JSON(200, gin.H{
		"success": true,
		"message": "Generation stopped",
	})
}

// handleAgentEventsForSSE handles agent events for SSE streaming using an existing handler
// The handler is already subscribed to events and AgentQA is already running
// This function polls StreamManager and pushes events to SSE, allowing graceful handling of disconnections
// waitForTitle: if true, wait for title event after completion (for new sessions without title)
func (h *Handler) handleAgentEventsForSSE(
	ctx context.Context,
	c *gin.Context,
	sessionID, assistantMessageID, requestID string,
	eventBus *event.EventBus,
	waitForTitle bool,
	resourceRewriter *storageurl.StreamRewriter,
) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	lastOffset := 0
	log := logger.GetLogger(ctx)
	// Agent mode releases the sandbox turn lease, and therefore cancels ctx,
	// before the completion event is published. Polling Redis with that
	// context drops the terminal event and the client waits at the end of
	// the answer. Client disconnect is still observed on the request below.
	pollCtx := context.WithoutCancel(ctx)

	log.Infof("Starting pull-based SSE streaming for session=%s, message=%s", sessionID, assistantMessageID)

	for {
		select {
		case <-c.Request.Context().Done():
			// Connection closed, exit gracefully without panic
			log.Infof(
				"Client disconnected, stopping SSE streaming for session=%s, message=%s",
				sessionID,
				assistantMessageID,
			)
			return

		case <-heartbeat.C:
			c.SSEvent("message", gin.H{"response_type": "heartbeat"})
			c.Writer.Flush()
		case <-ticker.C:
			// Get new events from StreamManager using offset
			events, newOffset, err := h.streamManager.GetEvents(pollCtx, sessionID, assistantMessageID, lastOffset)
			if err != nil {
				log.Warnf("Failed to get events from stream: %v", err)
				continue
			}

			// Send any new events
			streamCompleted := false
			titleReceived := false
			for _, evt := range events {
				// Check for stop event
				if evt.Type == types.ResponseType(event.EventStop) {
					log.Infof("Detected stop event, triggering stop via EventBus for session=%s", sessionID)

					// Emit stop event to the EventBus to trigger context cancellation
					if eventBus != nil {
						eventBus.Emit(ctx, event.Event{
							Type:      event.EventStop,
							SessionID: sessionID,
							Data: event.StopData{
								SessionID: sessionID,
								MessageID: assistantMessageID,
								Reason:    "user_requested",
							},
						})
					}

					// Release any buffered tail first: the answer generated
					// before the stop is still the user's content, and in
					// public resource URL mode part of it may be sitting in
					// the holdback buffer.
					flushHeldStreamContent(ctx, c, requestID, resourceRewriter)

					// Send stop notification to frontend
					c.SSEvent("message", &types.StreamResponse{
						ID:           requestID,
						ResponseType: "stop",
						Content:      "Generation stopped by user",
						Done:         true,
					})
					c.Writer.Flush()
					return
				}

				// Check for completion event
				if evt.Type == "complete" {
					streamCompleted = true
				}

				// Check for title event
				if evt.Type == types.ResponseTypeSessionTitle {
					titleReceived = true
				}

				// Check if connection is still alive before writing. Build the
				// payload only after this check: in public resource URL mode
				// building consumes the chunk into the holdback buffer, so an
				// early return here would drop it.
				if c.Request.Context().Err() != nil {
					log.Info("Connection closed during event sending, stopping")
					return
				}

				emitStreamEvent(ctx, c, evt, requestID, resourceRewriter)
			}

			// Update offset
			lastOffset = newOffset

			// Check if stream is completed - wait for title event only if needed and not already received
			if streamCompleted {
				if waitForTitle && !titleReceived {
					log.Infof("Stream completed for session=%s, message=%s, waiting for title event", sessionID, assistantMessageID)
					// Wait up to 3 seconds for title event after completion
					titleTimeout := time.After(3 * time.Second)
				titleWaitLoop:
					for {
						select {
						case <-titleTimeout:
							log.Info("Title wait timeout, closing stream")
							break titleWaitLoop
						case <-c.Request.Context().Done():
							log.Info("Connection closed while waiting for title")
							return
						default:
							// Check for new events (title event)
							events, newOff, err := h.streamManager.GetEvents(c.Request.Context(), sessionID, assistantMessageID, lastOffset)
							if err != nil {
								log.Warnf("Error getting events while waiting for title: %v", err)
								break titleWaitLoop
							}
							if len(events) > 0 {
								for _, evt := range events {
									emitStreamEvent(ctx, c, evt, requestID, resourceRewriter)
									// If we got the title, we can exit
									if evt.Type == types.ResponseTypeSessionTitle {
										log.Infof("Title event received: %s", evt.Content)
										break titleWaitLoop
									}
								}
								lastOffset = newOff
							} else {
								// No events, wait a bit before checking again
								time.Sleep(100 * time.Millisecond)
							}
						}
					}
				} else {
					log.Infof("Stream completed for session=%s, message=%s", sessionID, assistantMessageID)
				}
				sendCompletionEvent(c, requestID)
				return
			}
		}
	}
}
