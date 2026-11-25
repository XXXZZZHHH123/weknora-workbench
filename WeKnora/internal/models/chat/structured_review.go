package chat

import (
	"encoding/json"
	"maps"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/models"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/providers"
	"github.com/Tencent/WeKnora/internal/types"
)

// StructuredReviewConfig selects the DashScope protocol for an Excel review
// without changing the saved model or its use by other callers.
func StructuredReviewConfig(config *ChatConfig) *ChatConfig {
	if config.Source != types.ModelSourceRemote || !strings.EqualFold(strings.TrimSpace(config.Provider), providers.GenericID) {
		return config
	}
	endpoint, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return config
	}
	switch strings.ToLower(endpoint.Hostname()) {
	case "dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "dashscope-us.aliyuncs.com":
		reviewConfig := *config
		reviewConfig.Provider = "aliyun"
		// A generic row can also carry its old wire settings explicitly.
		reviewConfig.ExtraConfig = maps.Clone(config.ExtraConfig)
		delete(reviewConfig.ExtraConfig, models.ExtraThinkingControl)
		if config.Spec != nil {
			spec := *config.Spec
			spec.Compat = maps.Clone(spec.Compat)
			delete(spec.Compat, "max_tokens_field")
			delete(spec.Compat, "thinking_format")
			reviewConfig.Spec = &spec
		}
		return &reviewConfig
	default:
		return config
	}
}

// StructuredReviewOptions uses the same provider resolution as the client.
// The two booleans only describe a known, selectable off mode.
func StructuredReviewOptions(config *ChatConfig, options *ChatOptions, jsonObject bool) (canDisable, disabled bool) {
	if options.Thinking == nil {
		off := false
		options.Thinking = &off
	}
	if config.Source == types.ModelSourceLocal {
		if jsonObject {
			options.Format = json.RawMessage(`"json"`)
		}
		return false, false
	}
	resolved, err := Resolve(StructuredReviewConfig(config))
	if err != nil {
		return false, false
	}
	caps := resolved.Capabilities()
	canDisable = caps.ThinkingFormat != "" && caps.ThinkingFormat != string(api.ThinkingFormatNone) &&
		resolved.ThinkingLevels.Supports(api.ReasoningOff)
	disabled = canDisable && (!*options.Thinking ||
		(resolved.API == api.APIOpenAICompletions && resolved.OpenAICompletions.ThinkingDisableOnNonStream))
	if jsonObject && supportReviewJSONObject(resolved.Vendor.ID, resolved.RemoteModel, canDisable && !disabled) {
		options.Format = json.RawMessage(`{"type":"object"}`)
	}
	options.DisableImageFallback = true
	return canDisable, disabled
}

func supportReviewJSONObject(provider, model string, thinking bool) bool {
	if provider != "aliyun" {
		return true // The protocol adapter also checks its resolved capabilities.
	}
	// DashScope supports JSON mode on a subset of its models, some only with
	// thinking disabled. These are provider model IDs, independent of the input.
	model = strings.ToLower(model)
	for _, prefix := range []string{"qwen3.7-", "qwen3.8-", "qwen4-max", "deepseek-v4-pro", "deepseek-v4.1-flash", "deepseek-v4-flash-0731"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	if thinking {
		return false
	}
	for _, prefix := range []string{"qwen3-", "qwen3.5-", "qwen3.6-", "qwen-max", "qwen-plus", "qwen-flash", "qwen-turbo", "qwen-long", "qwen2.5-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}
