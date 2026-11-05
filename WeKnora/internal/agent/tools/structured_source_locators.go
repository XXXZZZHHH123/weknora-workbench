package tools

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// StructuredSourceLocators projects identities, not old observations. Whole-KB
// grants are checked before any historical metadata is shown; document/tag
// restrictions cannot be widened to the separate structured store. A locator
// grants no current evidence or permission and must be read again normally.
func (r *ToolRegistry) StructuredSourceLocators(ctx context.Context, messages []chat.Message) map[string]any {
	rows := []map[string]any{}
	view := map[string]any{"locators": rows, "view_complete": true, "notice": "历史成功结果的定位候选，不是本轮事实或引用资格。由模型根据完整任务选择，再按当前权限和版本读取；不存在或省略不能证明来源无数据。"}
	base := r.structuredBase()
	caller := types.CallerFromContext(ctx)
	if base == nil || caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() || !base.scopeConfigured || base.scopeError != nil {
		return view
	}
	type invocation struct {
		name string
		args map[string]any
	}
	calls := map[string]invocation{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			var args map[string]any
			if len(call.Function.Arguments) <= 16384 && json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
				calls[call.ID] = invocation{call.Function.Name, args}
			}
		}
	}
	seen := map[string]bool{}
	scanned := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		scanned++
		if scanned > 96 || len(rows) >= 16 {
			view["view_complete"] = false
			break
		}
		call, known := calls[message.ToolCallID]
		if !known || len(message.Content) > 192*1024 {
			continue
		}
		probe := types.ToolCall{Name: call.name, Args: call.args}
		operation, genuine := StructuredOperationFromCall(probe)
		if !genuine || (operation != "query_pdf_table" && operation != "read_pdf_table" && operation != "query_series" && operation != "analyze_table" && operation != "inspect_source") {
			continue
		}
		var data map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.Content))
		decoder.UseNumber()
		if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF || data == nil {
			continue
		}
		if success, exists := data["success"]; exists {
			if success != true {
				continue
			}
			inner, _ := data["data"].(map[string]any)
			if inner == nil {
				continue
			}
			data = inner
		}
		status, _ := data["status"].(string)
		if status != "ok" && status != "reviewed" && status != "auto_accepted" {
			continue
		}
		source, _ := data["source"].(map[string]any)
		scope, _ := data["source_context"].(map[string]any)
		kb, _ := scope["knowledge_base_id"].(string)
		if kb == "" {
			kb, _ = source["knowledge_base_id"].(string)
		}
		if kb == "" {
			kb, _ = call.args["knowledge_base_id"].(string)
		}
		if kb == "" {
			continue
		}
		var granted *structuredKnowledgeScope
		for i := range base.knowledgeScopes {
			if base.knowledgeScopes[i].KnowledgeBaseID == kb {
				granted = &base.knowledgeScopes[i]
				break
			}
		}
		if granted == nil || base.authorizeStructuredAPIKeyScope(ctx, *granted, false) != nil {
			continue
		}
		if selected, _ := call.args["knowledge_base_id"].(string); selected != "" && selected != kb {
			continue
		}
		locator := map[string]any{"origin_tool_call_id": message.ToolCallID, "knowledge_base_id": kb, "operation": operation, "current_evidence": false}
		for _, key := range []string{"table_id", "dataset_id", "record_sha256", "bundle_sha256", "plan_sha256", "result_id"} {
			if value, ok := data[key].(string); ok && value != "" && len(value) <= 256 {
				locator[key] = value
			}
		}
		if provenance, ok := data["provenance"].(map[string]any); ok {
			for _, key := range []string{"record_sha256", "bundle_sha256", "plan_sha256"} {
				if value, ok := provenance[key].(string); ok && len(value) <= 256 {
					locator[key] = value
				}
			}
		}
		for _, key := range []string{"knowledge_id", "sha256", "file", "file_name", "sheet"} {
			if value, ok := source[key].(string); ok && value != "" && len(value) <= 256 {
				locator["source_"+key] = value
			}
		}
		if locator["table_id"] == nil && locator["dataset_id"] == nil {
			continue
		}
		identity := structuredDigest([]any{kb, locator["table_id"], locator["dataset_id"], locator["record_sha256"], locator["source_sha256"]})
		if seen[identity] {
			continue
		}
		seen[identity] = true
		rows = append(rows, locator)
	}
	view["locators"] = rows
	return view
}
