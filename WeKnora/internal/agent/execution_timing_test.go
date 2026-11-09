package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCompletionDurationIncludesPreparationBeforeLoop(t *testing.T) {
	engine := newTestEngine(t, &mockChat{responses: []mockResponse{chatAnswer("A complete response.")}})
	var completion event.AgentCompleteData
	engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		completion = evt.Data.(event.AgentCompleteData)
		return nil
	})
	// Model the interval already spent preparing a turn without sleeping.
	ctx := context.WithValue(t.Context(), agentExecutionStartKey{}, time.Now().Add(-time.Second))
	_, err := engine.executeLoop(ctx, &types.AgentState{}, "Respond briefly.", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.GreaterOrEqual(t, completion.TotalDurationMs, int64(1000), "completion must include preparation, not just the tool loop")
}

func TestResumedExecutionDoesNotInheritSuspensionWait(t *testing.T) {
	before := time.Now()
	old := context.WithValue(t.Context(), agentExecutionStartKey{}, before.Add(-time.Hour))
	started := agentExecutionStart(withAgentExecutionStart(old))
	require.False(t, started.Before(before), "a resumed active interval must not count the suspended interval")
	require.False(t, started.After(time.Now()))
}
