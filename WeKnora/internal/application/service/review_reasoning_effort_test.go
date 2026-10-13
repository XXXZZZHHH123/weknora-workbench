package service

import (
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewReasoningUsesLeastSupportedLevelOnlyForAlwaysOnModel(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"glm-5.3", "minimal"}, {"deepseek-v4.1-flash", "off"}, {"qwen3.8-flash", "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &types.Model{Name: tc.name, Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceRemote, Parameters: types.ModelParameters{Provider: "aliyun"}}
			require.Equal(t, tc.want, reviewReasoningEffort(model))
		})
	}
	require.Equal(t, "off", reviewReasoningEffort(nil))
}
