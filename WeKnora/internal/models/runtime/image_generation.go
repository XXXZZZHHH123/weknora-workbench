package runtime

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/models"
	"github.com/Tencent/WeKnora/internal/models/internal/configcopy"
	"github.com/Tencent/WeKnora/internal/types"
)

// ValidateImageGenerationParameters checks protocol parameters without guessing
// a model's capabilities from its name or making a paid generation request.
func ValidateImageGenerationParameters(extra map[string]string) error {
	if value := strings.TrimSpace(extra["image_size"]); value != "" && value != "auto" {
		parts := strings.Split(value, "x")
		if len(parts) != 2 {
			return fmt.Errorf("image_size must be auto or WIDTHxHEIGHT")
		}
		w, e1 := strconv.Atoi(parts[0])
		h, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil || w < 64 || h < 64 || w > 8192 || h > 8192 || int64(w)*int64(h) > 32<<20 {
			return fmt.Errorf("image_size must have sides from 64 to 8192 and at most 32 megapixels")
		}
	}
	if value := strings.TrimSpace(extra["image_quality"]); len(value) > 40 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("image_quality is invalid")
	}
	if value := strings.TrimSpace(extra["image_response_format"]); value != "" && value != "b64_json" && value != "url" {
		return fmt.Errorf("image_response_format must be b64_json or url")
	}
	if value := strings.TrimSpace(extra["image_timeout_seconds"]); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 30 || seconds > 600 {
			return fmt.Errorf("image_timeout_seconds must be between 30 and 600")
		}
	}
	return nil
}

func resolveImageGeneration(ref Ref, vendor *Provider, spec models.ModelSpec, cataloged bool, baseURL string) (*Resolved, error) {
	if !vendor.SupportsType(types.ModelTypeImageGeneration) {
		return nil, fmt.Errorf("%s does not offer image generation in this build", vendor.Name)
	}
	if strings.TrimSpace(ref.Model) == "" {
		return nil, fmt.Errorf("image generation model name is required")
	}
	// Image generation uses its own Images protocol, not the chat protocol.
	if o := ref.Override; o != nil && (o.API != "" || o.Reasoning != nil || len(o.Input) > 0 || o.ContextWindow != 0 || o.MaxOutputTokens != 0 || len(o.ThinkingLevels) > 0 || len(o.Compat) > 0) {
		return nil, fmt.Errorf("chat spec overrides do not apply to image generation")
	}
	if strings.TrimSpace(ref.Extra[models.ExtraAPI]) != "" {
		return nil, fmt.Errorf("chat API overrides do not apply to image generation")
	}
	if err := ValidateImageGenerationParameters(ref.Extra); err != nil {
		return nil, err
	}
	out := &Resolved{Vendor: configcopy.Clone(vendor.Definition), Spec: spec, Cataloged: cataloged, BaseURL: baseURL, RemoteModel: ref.Model}
	if remote := strings.TrimSpace(ref.Extra[models.ExtraRemoteModelName]); remote != "" {
		out.RemoteModel = remote
	}
	return out, nil
}
