package agent

import (
	"bytes"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

// Numeric-warning presentation cannot invent an attachment. Parse actual
// Markdown images, including reference-style images, instead of searching for
// resource-looking text in prose, code examples or ordinary links.
func (e *AgentEngine) numericUnverifiedImagesCurrent(candidate string, state *types.AgentState) bool {
	if e == nil || state == nil {
		return false
	}
	images, valid := markdownImageResources(candidate)
	if !valid || len(images) == 0 {
		return valid
	}
	verified := e.numericUnverifiedRenderedImages(state)
	// Reuse the native read verifier: returned pixels, their digest, fetched
	// chunk, document identity and exact catalog/read locator must all agree.
	// Geometry-only reads and catalog entries never prove image delivery.
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.ExecutionName() != "read_document" || call.Result == nil || !call.Result.Success {
				continue
			}
			call.Name, call.Args, call.Target = call.ExecutionName(), call.ExecutionArgs(), nil
			views, _ := documentImageConsistencySummaries(&types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}})
			for _, view := range views {
				content := reviewAsMap(view.Content)
				reference, _ := content["reference"].(string)
				if content["original_pixels_verified"] == true {
					verified[reference] = true
				}
			}
		}
	}
	for reference := range images {
		if !verified[reference] {
			return false
		}
	}
	return true
}

func markdownImageResources(candidate string) (map[string]bool, bool) {
	images := map[string]bool{}
	source := []byte(candidate)
	document := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Linkify)).Parser().Parse(text.NewReader(source))
	valid := true
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		// Native HTML has independent src/srcset/SVG semantics. This fallback
		// accepts the bound Markdown image protocol only; reject actual HTML
		// image elements without touching citation tags or code examples.
		var rawHTML []byte
		switch raw := node.(type) {
		case *ast.RawHTML:
			rawHTML = raw.Segments.Value(source)
		case *ast.HTMLBlock:
			rawHTML = raw.Lines().Value(source)
			if raw.HasClosure() {
				rawHTML = append(rawHTML, raw.ClosureLine.Value(source)...)
			}
		}
		if numericUnverifiedHTMLHasImage(rawHTML) {
			valid = false
			return ast.WalkStop, nil
		}
		if image, ok := node.(*ast.Image); entering && ok {
			reference := string(image.Destination)
			if _, registered := types.ParseResourcePath(reference); !registered {
				valid = false
				return ast.WalkStop, nil
			}
			images[reference] = true
		}
		return ast.WalkContinue, nil
	})
	return images, valid
}

func numericUnverifiedHTMLHasImage(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	tokens := html.NewTokenizer(bytes.NewReader(raw))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			if bytes.Equal(name, []byte("img")) || bytes.Equal(name, []byte("image")) {
				return true
			}
		}
	}
}

func (e *AgentEngine) numericUnverifiedRenderedImages(state *types.AgentState) map[string]bool {
	verified := map[string]bool{}
	taskConversation := e.turnReview.Conversation
	if e.turnReview.TaskConversationCaptured {
		taskConversation = e.turnReview.TaskConversation
	}
	for _, result := range e.currentRenderedCharts(e.turnReview.Query, taskConversation, state) {
		verified[result.handle] = true
	}
	return verified
}

type currentRenderedChart struct {
	handle string
	data   map[string]any
}

func (e *AgentEngine) currentRenderedCharts(query string, conversation []chat.Message, state *types.AgentState) []currentRenderedChart {
	if e.toolRegistry == nil {
		return nil
	}
	registered, err := e.toolRegistry.GetTool(renderChartToolName)
	tool, ok := registered.(*renderChartTool)
	if err != nil || !ok {
		return nil
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	taskHash := e.chartTaskHash(query, conversation)
	var verified []currentRenderedChart
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.ExecutionName() != renderChartToolName || call.Result == nil || !call.Result.Success ||
				call.Result.Data["kind"] != "data_chart" || call.Result.Data["status"] != "ok" {
				continue
			}
			var delivery chart.Delivery
			raw, err := json.Marshal(call.Result.Data["artifact"])
			if err != nil || json.Unmarshal(raw, &delivery) != nil {
				continue
			}
			receipt, exists := tool.renderReceipts[delivery.Handle]
			if !exists || receipt.CallID != call.ID || receipt.TaskHash != taskHash ||
				receipt.DataHash != reviewDigest(call.Result.Data) || receipt.Output != call.Result.Output ||
				!chartReceiptSourceCurrent(call, tool, state) {
				continue
			}
			verified = append(verified, currentRenderedChart{handle: delivery.Handle, data: call.Result.Data})
		}
	}
	return verified
}
