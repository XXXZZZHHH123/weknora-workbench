// Package memorycontext builds text-only authorized conversation context for
// memory relevance decisions. It never retrieves or chooses memories.
package memorycontext

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	PageSize = 200
	MaxRows  = 5000
)

type PageReader func(context.Context, string, int, int) ([]*types.Message, error)

// SessionReadContext restores the caller's workspace and owner lookup. A
// shared agent's model/retrieval tenant cannot widen conversation access.
func SessionReadContext(ctx context.Context) (context.Context, error) {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		return nil, errors.New("memory conversation requires an authenticated workspace")
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, caller.TenantID)
	ctx = context.WithValue(ctx, types.SessionTenantIDContextKey, nil)
	if types.SessionOwnerIDFromContext(ctx) == "" {
		return nil, errors.New("memory conversation requires an authenticated owner")
	}
	return ctx, nil
}

// LoadCompleted reads the whole conversation preceding this turn through an authorized
// reader. I/O, identity or resource-bound failures must be marked incomplete,
// never presented as a complete newest-N view. Tool payloads, rendered source
// prompts and image bytes are excluded by projecting only original text.
func LoadCompleted(ctx context.Context, reader PageReader, sessionID, currentAssistantID, currentUserID string) ([]interfaces.MemoryConversationMessage, error) {
	if reader == nil || sessionID == "" {
		return nil, errors.New("authorized memory conversation reader is unavailable")
	}
	var rows []*types.Message
	seen := make(map[string]bool)
	complete := false
	for page := 1; page <= MaxRows/PageSize+1; page++ {
		batch, err := reader(ctx, sessionID, page, PageSize)
		if err != nil {
			return nil, errors.New("authorized memory conversation could not be read completely")
		}
		for _, message := range batch {
			if message == nil || message.SessionID != sessionID || message.ID == "" {
				return nil, errors.New("memory conversation contains an invalid session identity")
			}
			if seen[message.ID] {
				return nil, errors.New("memory conversation pagination repeated a message")
			}
			if len(rows) > 0 {
				previous := rows[len(rows)-1]
				if message.CreatedAt.Before(previous.CreatedAt) || (message.CreatedAt.Equal(previous.CreatedAt) && message.ID <= previous.ID) {
					return nil, errors.New("memory conversation pagination has an unstable order")
				}
			}
			seen[message.ID] = true
			rows = append(rows, message)
			if len(rows) > MaxRows {
				return nil, errors.New("memory conversation exceeds the complete-history row budget")
			}
		}
		if len(batch) < PageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, errors.New("memory conversation exceeds the complete-history page budget")
	}
	// The persisted current-turn rows provide a fixed cutoff, so a concurrent
	// later request cannot leak into this turn's memory relevance decision.
	// Request IDs exclude same-turn expansions, but are not required on old
	// messages: cancelled replies and legacy turns still carry user intent.
	if currentAssistantID == "" && currentUserID == "" {
		return nil, errors.New("memory conversation has no current-turn boundary")
	}
	cutoff := len(rows)
	foundAssistant, foundUser := currentAssistantID == "", currentUserID == ""
	currentRequests := make(map[string]bool)
	for i, message := range rows {
		isAssistant := message.ID == currentAssistantID && currentAssistantID != ""
		isUser := message.ID == currentUserID && currentUserID != ""
		if !isAssistant && !isUser {
			continue
		}
		if (isAssistant && message.Role != "assistant") || (isUser && message.Role != "user") {
			return nil, errors.New("memory conversation has an invalid current-turn boundary")
		}
		foundAssistant = foundAssistant || isAssistant
		foundUser = foundUser || isUser
		if i < cutoff {
			cutoff = i
		}
		if message.RequestID != "" {
			currentRequests[message.RequestID] = true
		}
	}
	if !foundAssistant || !foundUser {
		return nil, errors.New("memory conversation current-turn boundary is unavailable")
	}
	conversation := make([]interfaces.MemoryConversationMessage, 0, len(rows))
	latestSummary := ""
	for _, message := range rows[:cutoff] {
		if message.RequestID != "" && currentRequests[message.RequestID] {
			continue
		}
		if message.Role != "user" && (message.Role != "assistant" || !message.IsCompleted) {
			continue
		}
		if message.Role == "assistant" && message.ContextCheckpoint != nil && strings.TrimSpace(message.ContextCheckpoint.Summary) != "" {
			latestSummary = message.ContextCheckpoint.Summary
		}
		if strings.TrimSpace(message.Content) != "" {
			conversation = append(conversation, interfaces.MemoryConversationMessage{Role: message.Role, Content: message.Content})
		}
	}
	// An existing summary supplements the complete original turns; it never
	// replaces a prefix that might have omitted intermediate turns.
	if latestSummary != "" {
		conversation = append([]interfaces.MemoryConversationMessage{{Role: "summary", Content: latestSummary}}, conversation...)
	}
	return conversation, nil
}
