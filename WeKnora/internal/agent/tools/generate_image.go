package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type GenerateImageTool struct {
	BaseTool
	service interfaces.ImageGenerationService
	modelID string
}

func NewGenerateImageTool(service interfaces.ImageGenerationService, modelID string) *GenerateImageTool {
	return &GenerateImageTool{BaseTool: NewBaseTool(ToolGenerateImage,
		"Generate one new image from a detailed text description using the configured image generation model. "+
			"Describe the requested composition, style and visible text in prompt. This creates a generated illustration, not source evidence. "+
			"The result contains a saved image resource that can be shown with Markdown ![description](resource://handle). "+
			"Only claim successful delivery when the tool returns a saved artifact. At most one provider submission is allowed per assistant reply; another image requires a new user request. Do not repeat failed or interrupted generation automatically.",
		json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"Description of the image to generate, including composition, style and any visible text","minLength":1,"maxLength":32000},"size":{"type":"string","description":"Optional image dimensions supported by the configured provider, e.g. WIDTHxHEIGHT; omit for provider default","maxLength":32},"quality":{"type":"string","description":"Optional quality supported by the configured provider; omit for provider default","maxLength":32}},"required":["prompt"],"additionalProperties":false}`)),
		service: service, modelID: modelID}
}

// Image generation has a separate bounded budget from text-model calls.
func (*GenerateImageTool) ExecutionTimeout() time.Duration { return 11 * time.Minute }

func (t *GenerateImageTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	fail := func(err error) (*types.ToolResult, error) {
		return &types.ToolResult{Success: false, Error: err.Error(), Data: map[string]interface{}{"display_type": "image_generation", "status": "failed"}}, nil
	}
	if t.service == nil || t.modelID == "" {
		return fail(fmt.Errorf("image generation is not configured"))
	}
	var input struct{ Prompt, Size, Quality string }
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return fail(fmt.Errorf("invalid image generation arguments"))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fail(fmt.Errorf("invalid image generation arguments"))
	}
	meta, ok := ToolExecFromContext(ctx)
	if !ok || meta.SessionID == "" || meta.AssistantMessageID == "" || meta.ToolCallID == "" {
		return fail(fmt.Errorf("image generation requires an authenticated tool execution"))
	}
	delivery, err := t.service.Generate(ctx, types.ImageGenerationRequest{ModelID: t.modelID,
		SessionID: meta.SessionID, MessageID: meta.AssistantMessageID, ToolCallID: meta.ToolCallID,
		Prompt: input.Prompt, Size: input.Size, Quality: input.Quality})
	if err != nil {
		return fail(err)
	}
	if delivery == nil || len(delivery.Artifacts) == 0 {
		return fail(fmt.Errorf("image generation returned no saved artifact"))
	}
	encoded, _ := json.Marshal(delivery)
	var data map[string]interface{}
	_ = json.Unmarshal(encoded, &data)
	data["display_type"], data["status"] = "image_generation", "completed"
	return &types.ToolResult{Success: true, Data: data,
		Output: fmt.Sprintf("Generated and saved one image (%dx%d). Generated content is not source-document evidence. Display the saved image with ![Generated image](%s).", delivery.Width, delivery.Height, delivery.Artifacts[0].Handle)}, nil
}
