package agent

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// Verification uses durable executed results as well as the model transcript.
// A lossy display view must not erase provenance. Drafts are never promoted to
// evidence here; callers exclude the review envelope from transcript groups.
func answerNumericEvidence(answer string, steps []types.AgentStep, groups ...[]chat.Message) []chat.Message {
	var evidence []chat.Message
	for _, group := range groups {
		evidence = append(evidence, group...)
	}
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result != nil && call.Result.Success {
				evidence = append(evidence, chat.Message{Role: "tool", Name: call.Name, Content: call.Result.Output})
			}
		}
	}
	if values := scopedCitedDocumentNumbers(answer, steps); len(values) > 0 {
		raw, _ := json.Marshal(map[string]any{"cited_document_values": values})
		evidence = append(evidence, chat.Message{Role: "tool", Content: string(raw)})
	}
	return evidence
}
