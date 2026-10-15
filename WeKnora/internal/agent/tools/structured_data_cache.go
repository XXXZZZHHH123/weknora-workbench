package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	structuredReadCacheEntries = 32
	structuredReadCacheBytes   = 2 * 1024 * 1024
)

var immutableStructuredDatasetID = regexp.MustCompile(`^[a-f0-9]{24}$`)

// Cache ownership is the per-turn StructuredDataTool instance, never a package
// singleton. Requests additionally partition by authenticated caller and the
// available turn identifiers. Rebinding (even to the same question) invalidates
// the generation and prevents an older in-flight query publishing into it.
type structuredReadCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[string]structuredReadCacheEntry
	order      []string
	bytes      int
	inflight   map[string]chan struct{}
}

type structuredReadCacheEntry struct {
	encoded       []byte // Frozen complete result, never a mutable returned map.
	outputFiles   []string
	size          int
	originCallID  string
	originTime    time.Time
	originElapsed int64
}

type structuredReadCacheLease struct {
	key          string
	generation   uint64
	done         chan struct{}
	originCallID string
	started      time.Time
}

func (t *StructuredDataTool) bindStructuredSourceQuestion(question string) {
	c := &t.readCache
	c.mu.Lock()
	defer c.mu.Unlock()
	t.sourceQuestion = question
	c.generation++
	c.entries, c.order, c.inflight, c.bytes = nil, nil, nil, 0
}

func (t *StructuredDataTool) structuredSourceQuestion() (string, uint64) {
	c := &t.readCache
	c.mu.Lock()
	defer c.mu.Unlock()
	return t.sourceQuestion, c.generation
}

func (t *StructuredDataTool) structuredReadCacheKey(ctx context.Context, caller types.Caller, scope structuredKnowledgeScope, operation string,
	arguments map[string]json.RawMessage, question string, generation uint64,
) (string, bool) {
	switch operation {
	case "query_series", "analyze_table", "inspect_source", "list_series", "list_charts", "compare_series", "convert_price":
	default:
		// Field resolution/confirmation/execution/evidence may depend on state.
		// Source discovery chooses latest and must observe each actual refresh.
		return "", false
	}
	var datasetID string
	if json.Unmarshal(arguments["dataset_id"], &datasetID) != nil || !immutableStructuredDatasetID.MatchString(datasetID) {
		return "", false // Never cache omitted/null/default latest or mutable aliases.
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return "", false
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // Keep SQL/limits and large numeric values without rounding.
	if decoder.Decode(&canonical) != nil {
		return "", false
	}
	requestID, _ := types.RequestIDFromContext(ctx)
	turn := map[string]string{"request_id": requestID}
	if meta, ok := ToolExecFromContext(ctx); ok {
		turn["session_id"], turn["assistant_message_id"], turn["tool_request_id"] = meta.SessionID, meta.AssistantMessageID, meta.RequestID
	}
	// Missing execution metadata is safe only through this instance/generation;
	// callers still never share entries and BindSourceQuestion clears every turn.
	encoded, err := json.Marshal(map[string]any{
		"user_id": caller.UserID, "tenant_id": caller.TenantID, "role": caller.Role,
		"knowledge_scope": scope,
		"turn":            turn, "generation": generation, "source_question": question,
		"operation": operation, "arguments": canonical,
	})
	if err != nil {
		return "", false
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), true
}

func (t *StructuredDataTool) beginStructuredReadCache(ctx context.Context, caller types.Caller, scope structuredKnowledgeScope, operation string,
	arguments map[string]json.RawMessage, question string, generation uint64,
) (structuredReadCacheLease, *types.ToolResult, error) {
	key, allowed := t.structuredReadCacheKey(ctx, caller, scope, operation, arguments, question, generation)
	if !allowed {
		return structuredReadCacheLease{}, nil, nil
	}
	c := &t.readCache
	for {
		if err := ctx.Err(); err != nil {
			return structuredReadCacheLease{}, nil, err
		}
		c.mu.Lock()
		if c.generation != generation {
			c.mu.Unlock()
			return structuredReadCacheLease{}, nil, nil
		}
		if entry, exists := c.entries[key]; exists {
			c.mu.Unlock()
			return structuredReadCacheLease{}, cloneStructuredCacheHit(entry), nil
		}
		if done, exists := c.inflight[key]; exists {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return structuredReadCacheLease{}, nil, ctx.Err()
			case <-done:
				// Only success is reusable. If the first request failed, retry as
				// a real request instead of caching/propagating its transient error.
				continue
			}
		}
		if len(c.inflight) >= structuredReadCacheEntries {
			c.mu.Unlock()
			return structuredReadCacheLease{}, nil, nil
		}
		if c.inflight == nil {
			c.inflight = make(map[string]chan struct{})
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()
		callID := ""
		if meta, ok := ToolExecFromContext(ctx); ok {
			callID = meta.ToolCallID
		}
		return structuredReadCacheLease{key: key, generation: generation, done: done,
			originCallID: callID, started: time.Now()}, nil, nil
	}
}

func cloneStructuredCacheHit(entry structuredReadCacheEntry) *types.ToolResult {
	var result types.ToolResult
	// Entries are marshalled by this process; no shared map/array escapes.
	if json.Unmarshal(entry.encoded, &result) != nil {
		return nil
	}
	if entry.outputFiles != nil {
		result.OutputFiles = append([]string{}, entry.outputFiles...)
	}
	if result.Data == nil {
		result.Data = map[string]any{}
	}
	result.Data["_execution_cache"] = map[string]any{
		"cache_hit": true, "original_execution_ms": entry.originElapsed,
		"origin_tool_call_id":  entry.originCallID,
		"original_executed_at": entry.originTime.UTC().Format(time.RFC3339Nano),
	}
	return &result
}

func (t *StructuredDataTool) finishStructuredReadCache(lease structuredReadCacheLease, result *types.ToolResult) {
	if lease.done == nil {
		return
	}
	c := &t.readCache
	c.mu.Lock()
	defer c.mu.Unlock()
	defer close(lease.done)
	if c.generation != lease.generation {
		return
	}
	delete(c.inflight, lease.key)
	if result == nil || !result.Success || result.Error != "" {
		return
	}
	status, _ := result.Data["status"].(string)
	if status != "" && status != "ok" && status != "no_data" {
		return // Clarification is not a completed numeric query.
	}
	if _, exists := result.Data["_execution_cache"]; exists {
		return // Never overwrite a future backend field with runtime metadata.
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	files := []string(nil)
	if result.OutputFiles != nil {
		files = append([]string{}, result.OutputFiles...)
	}
	size := len(encoded) + len(lease.key) + len(lease.originCallID) + 128
	for _, file := range files {
		size += len(file)
	}
	if size > structuredReadCacheBytes {
		return
	}
	for len(c.order) > 0 && (len(c.entries) >= structuredReadCacheEntries || c.bytes+size > structuredReadCacheBytes) {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.entries[old].size
		delete(c.entries, old)
	}
	if c.entries == nil {
		c.entries = make(map[string]structuredReadCacheEntry)
	}
	c.entries[lease.key] = structuredReadCacheEntry{encoded: encoded, outputFiles: files, size: size,
		originCallID: lease.originCallID, originTime: lease.started, originElapsed: time.Since(lease.started).Milliseconds()}
	c.order = append(c.order, lease.key)
	c.bytes += size
}
