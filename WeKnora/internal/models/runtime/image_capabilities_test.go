package runtime_test

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/providers"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestLegacyGenericImageCapabilitiesUseExactCatalogEndpoint(t *testing.T) {
	rt := modelruntime.New()
	rt.Register(&providers.Definition{
		ID: "fixture-vision", API: api.APIOpenAICompletions,
		DefaultBaseURLs: map[types.ModelType]string{types.ModelTypeKnowledgeQA: "https://vision.example.test/v1"},
		ModelTypes:      []types.ModelType{types.ModelTypeKnowledgeQA},
	}, models.ModelSpec{ID: "diagram-model", Input: []string{"text", "image"}}, models.ModelSpec{ID: "text-model", Input: []string{"text"}})
	for _, tc := range []struct {
		name, endpoint, provider, model string
		override                        *types.ModelSpecOverride
		want                            bool
	}{
		{"legacy generic", "https://vision.example.test/v1", "generic", "diagram-model", nil, true},
		{"default HTTPS port", "https://VISION.example.test:443/v1/", "generic", "diagram-model", nil, true},
		{"suffix spoof", "https://vision.example.test.attacker.test/v1", "generic", "diagram-model", nil, false},
		{"query spoof", "https://attacker.test/v1?host=vision.example.test", "generic", "diagram-model", nil, false},
		{"userinfo", "https://vision.example.test@attacker.test/v1", "generic", "diagram-model", nil, false},
		{"HTTP downgrade", "http://vision.example.test/v1", "generic", "diagram-model", nil, false},
		{"different port", "https://vision.example.test:8443/v1", "generic", "diagram-model", nil, false},
		{"different protocol root", "https://vision.example.test/anthropic", "generic", "diagram-model", nil, false},
		{"unknown model", "https://vision.example.test/v1", "generic", "unknown", nil, false},
		{"text model", "https://vision.example.test/v1", "generic", "text-model", nil, false},
		{"explicit other provider", "https://vision.example.test/v1", "openai", "diagram-model", nil, false},
		{"explicit input restriction", "https://vision.example.test/v1", "generic", "diagram-model", &types.ModelSpecOverride{Input: []string{"text"}}, false},
		{"explicit image metadata", "https://private.example.test/v1", "generic", "private", &types.ModelSpecOverride{Input: []string{"text", "image"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := modelruntime.Ref{Provider: tc.provider, Model: tc.model, BaseURL: tc.endpoint, ModelType: types.ModelTypeKnowledgeQA, Override: tc.override}
			before, err := rt.Resolve(ref)
			require.NoError(t, err)
			require.Equal(t, tc.want, rt.ChatAcceptsImages(ref))
			after, err := rt.Resolve(ref)
			require.NoError(t, err)
			require.Equal(t, before.Vendor.ID, after.Vendor.ID)
			// Vendor definitions contain constructor functions, which cannot be
			// DeepEqual; compare the immutable wire resolution separately.
			before.Vendor, after.Vendor = nil, nil
			require.Equal(t, before, after, "capability discovery must not change provider, endpoint or wire settings")
		})
	}
}
