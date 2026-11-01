package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionMemoryOptOutCannotBeReenabledByAnAgent(t *testing.T) {
	ctx := ApplySessionMemoryPreference(context.Background(), &Session{MemoryDisabled: true})
	enabled := true
	ctx = ApplyAgentMemoryPreference(ctx, &enabled)
	require.False(t, MemoryAllowedForAgent(ctx))
	require.True(t, SessionMemoryDisabled(ctx))
	require.True(t, MemoryAllowedForAgent(ApplySessionMemoryPreference(context.Background(), &Session{})))
}
