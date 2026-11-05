package tools

import (
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
)

type structuredConfirmationAttempt struct {
	key        string
	generation uint64
}

// BeginStructuredHumanTurn starts a fresh consumption boundary even when a
// direct UI submission skips source discovery and protocol negotiation. Clear
// the prior source question too; ordinary turns bind their current conversation
// before semantic data calls. The generation keeps an old in-flight request
// from releasing a confirmation that belongs to this later human turn.
func (r *ToolRegistry) BeginStructuredHumanTurn() {
	if base := r.structuredBase(); base != nil {
		base.bindStructuredSourceQuestion("")
	}
}

// One human reply authorizes one transport attempt for each actual pending
// resolution. This is a consumption fence, not a successful-result cache. An
// uncertain response cannot make the model replay the confirmation. A new
// human turn opens a fresh boundary; ordinary guards
// still require a real previous pending result and strip model selections.
func (t *StructuredDataTool) beginStructuredConfirmationAttempt(caller types.Caller, scope structuredKnowledgeScope, arguments map[string]json.RawMessage) (structuredConfirmationAttempt, bool) {
	var resolution string
	if json.Unmarshal(arguments["resolution_id"], &resolution) != nil || resolution == "" {
		return structuredConfirmationAttempt{}, true
	}
	c := &t.readCache
	c.mu.Lock()
	defer c.mu.Unlock()
	key := structuredDigest([]any{caller.TenantID, caller.UserID, caller.Role, scope, resolution})
	if c.confirmationAttempts[key] || len(c.confirmationAttempts) >= 32 {
		return structuredConfirmationAttempt{}, false
	}
	if c.confirmationAttempts == nil {
		c.confirmationAttempts = map[string]bool{}
	}
	c.confirmationAttempts[key] = true
	return structuredConfirmationAttempt{key: key, generation: c.generation}, true
}

func (t *StructuredDataTool) releaseUnexecutedStructuredConfirmation(attempt structuredConfirmationAttempt) {
	if attempt.key == "" {
		return
	}
	c := &t.readCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation == attempt.generation {
		delete(c.confirmationAttempts, attempt.key)
	}
}
