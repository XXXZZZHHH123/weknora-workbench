package agent

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	toolProgressReplanRounds = 3
	toolProgressStopRounds   = 6
	maxToolProgressKeys      = 4096

	sourceProgressStalledFallback = "本轮读取和纠错未取得新证据，已停止重复尝试，尚未完成回答。"
	toolProgressReplanNotice      = "Runtime diagnostic: recent tool rounds have returned only failures or already-observed source content. Reassess the COMPLETE human request, current source scope, actual tool schemas and returned diagnostics. Produce a corrected evidence/reading plan before continuing. Do not repeat reads merely by changing query wording or add optional calculations. A genuinely new authorized read or corrected operation remains available. If the required evidence cannot be obtained, state the actual bounded gap using only verified results; do not claim the task is complete or weaken source, permission, unit or numeric checks."
)

// toolProgressGuard is turn-local execution accounting, not an intent or
// completeness classifier. Only actual source content/metadata and technical
// tool outcomes participate; a model's claim of progress never resets it.
type toolProgressGuard struct {
	seen      map[[sha256.Size]byte]struct{}
	stale     int
	warned    bool
	saturated bool
}

type toolProgressObservation struct {
	NewEvidence int
	StaleRounds int
	Replan      bool
	Stop        bool
}

func (g *toolProgressGuard) observe(calls []types.ToolCall) toolProgressObservation {
	var out toolProgressObservation
	if len(calls) == 0 || g.saturated {
		return out
	}
	if g.seen == nil {
		g.seen = make(map[[sha256.Size]byte]struct{})
	}
	otherProgress, sourceAttempt := false, false
	for _, call := range calls {
		tracked := toolProgressOperation(call.ExecutionName())
		sourceAttempt = sourceAttempt || tracked
		if call.Result == nil || !call.Result.Success {
			continue
		}
		keys, sourceOperation := toolProgressEvidence(call)
		if !sourceOperation {
			// Workspace writes, interaction, external jobs and unknown tools can
			// make progress without returning document evidence. Do not classify
			// them as stalled from a source-only observation.
			otherProgress = true
			continue
		}
		for _, key := range keys {
			if _, exists := g.seen[key]; exists {
				continue
			}
			if len(g.seen) >= maxToolProgressKeys {
				// Accounting is bounded. Reaching its capacity is not evidence of
				// a stall in a legitimately large task.
				g.saturated = true
				g.stale, g.warned = 0, false
				return out
			}
			g.seen[key] = struct{}{}
			out.NewEvidence++
		}
	}
	if !sourceAttempt && !otherProgress {
		// Failures of external jobs, writes or other workflows remain under
		// their own retry guards; they are not evidence of a document stall.
		return out
	}
	if out.NewEvidence > 0 || otherProgress {
		g.stale, g.warned = 0, false
		return out
	}
	g.stale++
	out.StaleRounds = g.stale
	if g.stale >= toolProgressReplanRounds && !g.warned {
		g.warned, out.Replan = true, true
	}
	out.Stop = g.stale >= toolProgressStopRounds
	return out
}

func toolProgressOperation(name string) bool {
	switch name {
	case "read_document", "search_knowledge", "bind_document_scope", "list_documents", expandTaskToolsName,
		"calculate_document_table", "calculate_source_values", "analyze_document_tables":
		return true
	default:
		return false
	}
}

func toolProgressEvidence(call types.ToolCall) ([][sha256.Size]byte, bool) {
	name := call.ExecutionName()
	switch name {
	case "read_document", "search_knowledge":
		var keys [][sha256.Size]byte
		for _, field := range []string{"chunks", "results"} {
			encoded, err := json.Marshal(call.Result.Data[field])
			if err != nil {
				continue
			}
			var rows []map[string]json.RawMessage
			if json.Unmarshal(encoded, &rows) != nil {
				continue
			}
			for _, row := range rows {
				var id string
				if json.Unmarshal(row["chunk_id"], &id) != nil || id == "" {
					continue
				}
				// Query text, call IDs, result formatting and page cursors do not
				// turn the same original body into new evidence. Real segments,
				// revisions and returned image observations remain distinct.
				body := map[string]json.RawMessage{}
				for _, key := range []string{"chunk_id", "knowledge_id", "content", "content_offset", "content_revision", "images"} {
					if value, ok := row[key]; ok {
						body[key] = value
					}
				}
				value, _ := json.Marshal(body)
				keys = append(keys, sha256.Sum256(value))
			}
		}
		// A vision-capable model may receive fresh original pixels/geometry
		// without any new text or a separate image-description model call.
		// Count only actual successful image reads, not catalog suggestions.
		imageReads, _ := json.Marshal(call.Result.Data["image_evidence"])
		var images []map[string]json.RawMessage
		_ = json.Unmarshal(imageReads, &images)
		readReferences := map[string]bool{}
		for _, image := range images {
			var success bool
			if json.Unmarshal(image["read_success"], &success) != nil || !success {
				continue
			}
			var reference string
			_ = json.Unmarshal(image["reference"], &reference)
			if reference != "" {
				readReferences[reference] = true
			}
			// A position in this response's image array is transport metadata.
			// Reading the same pixels in another batch order is not new evidence.
			identity := map[string]json.RawMessage{}
			for _, key := range []string{"reference", "knowledge_id", "chunk_id", "page", "version", "sha256", "mime_type"} {
				if value, exists := image[key]; exists {
					identity[key] = value
				}
			}
			value, _ := json.Marshal(identity)
			keys = append(keys, sha256.Sum256(value))
		}
		geometryJSON, _ := json.Marshal(call.Result.Data["image_geometry_evidence"])
		var geometries []map[string]json.RawMessage
		_ = json.Unmarshal(geometryJSON, &geometries)
		for _, geometry := range geometries {
			var reference string
			_ = json.Unmarshal(geometry["reference"], &reference)
			if !readReferences[reference] {
				// Explicit geometry does not request pixels. Reuse its existing
				// source/catalog/chunk binding checks before counting an independently
				// successful geometry read; labels or a failure alone add no progress.
				if key, valid := geometryOnlyReadProgressKey(call.Result.Data, geometry); valid {
					keys = append(keys, key)
				}
				continue
			}
			value, _ := json.Marshal(geometry)
			keys = append(keys, sha256.Sum256(value))
		}
		if descriptions := call.Result.ImageDescriptions; len(descriptions) > 0 && len(readReferences) > 0 {
			value, _ := json.Marshal(struct {
				Document any
				Text     []string
			}{call.Result.Data["knowledge_id"], descriptions})
			keys = append(keys, sha256.Sum256(value))
		}
		keys = append(keys, documentReadProgressRanges(call.Result.Data)...)
		return keys, true
	case "bind_document_scope", "list_documents":
		return documentBindingProgressKeys(call.Result.Data), true
	case expandTaskToolsName:
		// Exposing a genuinely new capability can repair a plan. Relisting
		// the same directory with another explanation cannot hide a stall.
		var keys [][sha256.Size]byte
		for _, field := range []string{"available_tools", "tools"} {
			encoded, _ := json.Marshal(call.Result.Data[field])
			var definitions []json.RawMessage
			if json.Unmarshal(encoded, &definitions) == nil {
				for _, definition := range definitions {
					keys = append(keys, sha256.Sum256(definition))
				}
			}
		}
		return keys, true
	case "calculate_document_table", "calculate_source_values", "analyze_document_tables":
		if status, _ := call.Result.Data["status"].(string); status == "needs_source" || status == "error" {
			return nil, true
		}
		encoded, err := json.Marshal(call.Result.Data)
		if err != nil || len(call.Result.Data) == 0 {
			return nil, true
		}
		var data any
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		if decoder.Decode(&data) != nil {
			return nil, true
		}
		stripToolProgressTransport(data)
		value, err := json.Marshal(struct {
			Tool string `json:"tool"`
			Data any    `json:"data"`
		}{name, data})
		if err != nil {
			return nil, true
		}
		return [][sha256.Size]byte{sha256.Sum256(value)}, true
	default:
		return nil, false
	}
}

func geometryOnlyReadProgressKey(data map[string]any, geometry map[string]json.RawMessage) ([sha256.Size]byte, bool) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	var canonical map[string]any
	if json.Unmarshal(encoded, &canonical) != nil {
		return [sha256.Size]byte{}, false
	}
	encoded, err = json.Marshal(geometry)
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	var figure map[string]any
	if json.Unmarshal(encoded, &figure) != nil {
		return [sha256.Size]byte{}, false
	}
	projection := map[string]any{"original_pixels_verified": false, "pixel_status": "not_requested"}
	for _, key := range []string{"reference", "knowledge_id", "chunk_id", "page", "source_part"} {
		projection[key] = figure[key]
	}
	if !reviewGeometryProjectionBoundToSource(canonical, figure, projection) {
		return [sha256.Size]byte{}, false
	}
	// Catalog labels and navigation instructions are not new original geometry.
	projection["evidence"] = figure["evidence"]
	encoded, err = json.Marshal(projection)
	return sha256.Sum256(encoded), err == nil
}

// Presentation identities bind a particular read execution for integrity;
// they do not make identical source cells a new execution achievement. Keep
// real source versions, coordinates, values and failures, including children
// of a table-analysis batch, while excluding transport IDs and model advice.
func stripToolProgressTransport(value any) {
	switch node := value.(type) {
	case map[string]any:
		for _, key := range []string{"view_ref", "view_digest", "source_fingerprint", "source_ref", "view_notice", "notice", "model_advice", "advice_notice"} {
			delete(node, key)
		}
		if node["kind"] == "source_calculation_catalog" {
			if entries, ok := node["refs"].([]any); ok {
				for _, raw := range entries {
					if entry, ok := raw.(map[string]any); ok {
						delete(entry, "ref")
					}
				}
			}
		}
		for _, child := range node {
			stripToolProgressTransport(child)
		}
	case []any:
		for _, child := range node {
			stripToolProgressTransport(child)
		}
	}
}

// A successful empty search still advances an actually scanned source range.
// Query wording is deliberately absent: another query on the identical range
// must not indefinitely reset the execution budget without new evidence.
func documentReadProgressRanges(data map[string]any) [][sha256.Size]byte {
	var keys [][sha256.Size]byte
	document, _ := data["knowledge_id"].(string)
	if document == "" {
		return nil
	}
	observe := func(raw any) {
		value, err := json.Marshal(raw)
		if err != nil {
			return
		}
		var scope map[string]json.RawMessage
		if json.Unmarshal(value, &scope) != nil {
			return
		}
		projection := map[string]json.RawMessage{}
		for _, key := range []string{"page", "offset_space", "offset", "next_offset", "page_chunks", "page_complete", "content_offset", "content_complete", "next_content_offset"} {
			if value, exists := scope[key]; exists {
				projection[key] = value
			}
		}
		var queryScope map[string]json.RawMessage
		if json.Unmarshal(scope["query_scope"], &queryScope) == nil {
			for _, key := range []string{"physical_page", "offset", "scan_complete", "page_scan_complete", "document_scan_complete"} {
				if value, exists := queryScope[key]; exists {
					projection["query_scope_"+key] = value
				}
			}
		}
		if len(projection) == 0 {
			return
		}
		value, _ = json.Marshal(struct {
			Document string
			Range    map[string]json.RawMessage
		}{document, projection})
		keys = append(keys, sha256.Sum256(value))
	}
	if raw, exists := data["page_results"]; exists {
		value, _ := json.Marshal(raw)
		var pages []map[string]any
		if json.Unmarshal(value, &pages) == nil {
			for _, page := range pages {
				if page["success"] == true {
					observe(page)
				}
			}
		}
	} else {
		observe(data)
	}
	return keys
}

// Model navigation hints and selection prose are not new source metadata.
// Only the actual authorized document identity/version and frozen user page
// set participate, so rebinding the same document cannot mask a read loop.
func documentBindingProgressKeys(data map[string]any) [][sha256.Size]byte {
	value, _ := json.Marshal(data["documents"])
	var documents []map[string]json.RawMessage
	if json.Unmarshal(value, &documents) != nil {
		return nil
	}
	var keys [][sha256.Size]byte
	for _, document := range documents {
		projection := map[string]json.RawMessage{}
		for _, key := range []string{"knowledge_id", "knowledge_base_id", "file_name", "title", "file_type", "parse_status", "metadata"} {
			if value, exists := document[key]; exists {
				projection[key] = value
			}
		}
		var constraint map[string]json.RawMessage
		if json.Unmarshal(document["user_page_constraint"], &constraint) == nil {
			projection["user_pages"] = constraint["pages"]
		}
		if len(projection) > 0 {
			value, err := json.Marshal(projection)
			if err == nil {
				keys = append(keys, sha256.Sum256(value))
			}
		}
	}
	return keys
}

// Only server-supplied user history and the current request are eligible to
// witness a user page constraint. Assistant/tool/reviewer text is never one.
func documentScopeUserTexts(query string, messages []chat.Message) []string {
	var texts []string
	for _, message := range messages {
		if message.Role == "user" && message.Kind != chat.MessageKindReviewEvidence && strings.TrimSpace(message.Content) != "" {
			texts = append(texts, message.Content)
		}
	}
	if strings.TrimSpace(query) != "" {
		texts = append(texts, query)
	}
	return texts
}

func (e *AgentEngine) resetDocumentTurnProgress(guards *loopGuards, query string) {
	guards.toolProgress = toolProgressGuard{}
	if e.config != nil && e.config.WorkbenchDefault && e.toolRegistry != nil {
		agenttools.PrepareDocumentScope(e.toolRegistry, documentScopeUserTexts(query, e.reviewConversation)...)
	}
}
