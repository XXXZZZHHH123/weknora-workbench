package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
)

// acceptedSourceReviewFixture creates a protocol-valid receipt only for tests
// that explicitly opt into the historical mock-accept behavior. It binds each
// audit row to a scalar actually exposed in this invocation's successful
// source_tool_results; it does not simulate semantic entailment.
func acceptedSourceReviewFixture(messages []chat.Message) (string, error) {
	var request map[string]any
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		if json.Unmarshal([]byte(message.Content), &request) == nil && request["candidate_answer"] != nil {
			break
		}
		request = nil
	}
	if request == nil {
		return "", fmt.Errorf("accepted source-review fixture requires the actual review request")
	}
	candidate, _ := request["candidate_answer"].(string)
	claim := strings.TrimSpace(candidate)
	if claim == "" {
		return "", fmt.Errorf("accepted source-review fixture requires a candidate claim")
	}
	if runes := []rune(claim); len(runes) > 120 {
		claim = string(runes[:120])
	}
	var witness reviewSourceWitness
	results, _ := request["source_tool_results"].([]any)
	for _, raw := range results {
		entry, _ := raw.(map[string]any)
		id, _ := entry["evidence_id"].(string)
		content, _ := entry["content"].(map[string]any)
		if id == "" || content == nil || content["success"] != true {
			continue
		}
		data, ok := content["data"]
		if !ok {
			continue
		}
		if pointer, quote, ok := firstReceiptScalar(data, "/data"); ok {
			witness = reviewSourceWitness{EvidenceID: id, JSONPointer: pointer, Quote: quote, Kind: "source"}
			break
		}
	}
	if witness.EvidenceID == "" {
		for _, raw := range results {
			entry, _ := raw.(map[string]any)
			id, _ := entry["evidence_id"].(string)
			content, _ := entry["content"].(map[string]any)
			if id == "" || content == nil {
				continue
			}
			if content["success"] == true {
				for _, key := range []string{"output", "execution_scope"} {
					if value, ok := content[key]; ok {
						if pointer, quote, ok := firstReceiptScalar(value, "/"+key); ok {
							witness = reviewSourceWitness{EvidenceID: id, JSONPointer: pointer, Quote: quote, Kind: "execution"}
							break
						}
					}
				}
			} else if content["success"] == false {
				witness = reviewSourceWitness{EvidenceID: id, JSONPointer: "/success", Quote: "false", Kind: "execution"}
			}
			if witness.EvidenceID != "" {
				break
			}
		}
	}
	if witness.EvidenceID == "" {
		return "", fmt.Errorf("accepted source-review fixture found no exposed source or execution witness")
	}
	type fixtureAudit struct {
		Claim              string                `json:"claim"`
		CandidateStatement string                `json:"candidate_statement"`
		Fidelity           string                `json:"candidate_fidelity"`
		Basis              string                `json:"basis"`
		Assessment         string                `json:"assessment"`
		Evidence           []reviewSourceWitness `json:"evidence"`
	}
	basis := "source"
	assessment := "test fixture: preserve the prior mock's accepted verdict"
	if witness.Kind == "execution" {
		basis = "limitation"
		assessment = "test fixture limitation: witness records execution status only and makes no source-fact claim"
	}
	audit := []fixtureAudit{{Claim: claim, CandidateStatement: claim, Fidelity: reviewFidelityAsWritten, Basis: basis, Assessment: assessment, Evidence: []reviewSourceWitness{witness}}}
	encoded, err := json.Marshal(map[string]any{"complete": true, "assessment": "Test fixture preserves the prior mock acceptance decision.", "audit": audit})
	return string(encoded), err
}

func firstReceiptScalar(value any, pointer string) (string, string, bool) {
	switch item := value.(type) {
	case string:
		if item != "" {
			return pointer, item, true
		}
	case json.Number, bool, float64, int, int64:
		encoded, err := json.Marshal(item)
		if err == nil {
			return pointer, string(encoded), true
		}
	case []any:
		for index, child := range item {
			if p, quote, ok := firstReceiptScalar(child, pointer+"/"+strconv.Itoa(index)); ok {
				return p, quote, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		// Stable traversal keeps generated fixtures deterministic.
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, key := range keys {
			if p, quote, ok := firstReceiptScalar(item[key], pointer+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(key)); ok {
				return p, quote, true
			}
		}
	}
	return "", "", false
}
