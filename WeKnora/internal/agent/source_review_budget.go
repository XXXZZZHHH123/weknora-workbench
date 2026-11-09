package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

const (
	// These limits apply to one immutable candidate/evidence review, including
	// reader continuations and malformed-verdict retries. They never approve a
	// candidate and do not replenish when the model returns invalid JSON.
	sourceReviewTotalTimeout          = 60 * time.Second
	sourceReviewTotalCompletionTokens = 12288
	sourceReviewReasoningTokenReserve = 2048
	sourceReviewMaxModelCalls         = answerReviewAttempts + reviewEvidenceReadLimit + 1
)

var errSourceReviewBudget = errors.New("answer review generation budget exhausted")

type reviewGenerationBudgetKey struct{}

type reviewGenerationBudget struct {
	purpose         string
	completionLimit int
	completionUsed  int
	calls           int
}

func reviewGenerationFromContext(ctx context.Context) *reviewGenerationBudget {
	budget, _ := ctx.Value(reviewGenerationBudgetKey{}).(*reviewGenerationBudget)
	return budget
}

func newReviewGenerationContext(ctx context.Context, sources bool) (context.Context, *reviewGenerationBudget, context.CancelFunc) {
	if existing := reviewGenerationFromContext(ctx); existing != nil {
		return ctx, existing, func() {}
	}
	timeout, purpose := 20*time.Second, "answer_completeness"
	if sources {
		timeout, purpose = sourceReviewTotalTimeout, "source_review"
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	budget := &reviewGenerationBudget{purpose: purpose, completionLimit: sourceReviewTotalCompletionTokens}
	return context.WithValue(bounded, reviewGenerationBudgetKey{}, budget), budget, cancel
}

func (budget *reviewGenerationBudget) remaining() int {
	return max(0, budget.completionLimit-budget.completionUsed)
}

func (budget *reviewGenerationBudget) prepare(options *chat.ChatOptions) (*chat.ChatOptions, error) {
	if budget.calls >= sourceReviewMaxModelCalls || budget.remaining() == 0 {
		return nil, errSourceReviewBudget
	}
	copy := *options
	if copy.MaxCompletionTokens <= 0 || copy.MaxCompletionTokens > budget.remaining() {
		copy.MaxCompletionTokens = budget.remaining()
	}
	// Keep the configured reasoning effort. A supported explicit thinking-token
	// budget cannot exceed the same remaining completion allowance.
	if copy.ThinkingBudgetTokens > copy.MaxCompletionTokens {
		copy.ThinkingBudgetTokens = copy.MaxCompletionTokens
	}
	budget.calls++
	return &copy, nil
}

func (budget *reviewGenerationBudget) consume(result *streamLLMResult) error {
	if result == nil {
		return nil
	}
	used := 0
	if result.Usage != nil {
		used = result.Usage.CompletionTokens
	}
	if used <= 0 {
		// Providers can omit usage. Charge a conservative byte count for actual
		// generated content/reasoning/arguments rather than granting free reads
		// or resetting the budget. The fixed call/deadline bounds still apply.
		used = len(result.Content) + len(result.ReasoningContent)
		for _, call := range result.ToolCalls {
			used += len(call.Function.Arguments)
		}
		used = max(1, used)
	}
	budget.completionUsed += used
	if budget.completionUsed > budget.completionLimit {
		return fmt.Errorf("%w: provider output exceeded the shared completion allowance", errSourceReviewBudget)
	}
	return nil
}
