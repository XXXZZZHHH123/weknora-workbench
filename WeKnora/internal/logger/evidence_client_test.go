package logger

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestEvidenceClientNegotiationSurvivesOnlyAsDisplayCapability(t *testing.T) {
	for _, version := range []string{"", "0", "2", "true", "1"} {
		t.Run("version_"+version, func(t *testing.T) {
			ctx, cancel := context.WithCancel(types.WithEvidenceReferencesClientVersion(context.Background(), version))
			cancel()
			for _, clone := range []context.Context{CloneContext(ctx), CloneContextWithoutTrace(ctx)} {
				require.NoError(t, clone.Err())
				require.Equal(t, version == "1", types.EvidenceReferencesClientEnabled(clone))
				require.Nil(t, clone.Value(types.TenantAPIKeyScopeContextKey), "the client display header cannot create an API authorization scope")
			}
		})
	}
	require.False(t, types.EvidenceReferencesClientEnabled(nil))
	require.False(t, types.EvidenceReferencesClientEnabled(context.Background()))
}
