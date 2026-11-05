package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func structuredProtocolDirectory(version string) map[string]any {
	return map[string]any{"protocol_version": 2, "manifest_sha256": strings.Repeat(version, 64), "tools": []any{
		map[string]any{"name": "list_datasets", "description": "Read actual authorized dataset metadata", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"question": map[string]any{"type": "string"}}}},
		map[string]any{"name": "query_pdf_table", "description": "Query the selected actual table", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []any{"table_id", "sql"}, "properties": map[string]any{"table_id": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}}}},
		map[string]any{"name": "resolve_fields", "description": "Resolve actual fields without inventing confirmation", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []any{"question"}, "properties": map[string]any{"question": map[string]any{"type": "string"}}}},
		map[string]any{"name": "confirm_fields", "description": "Confirm only the actual human selection", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []any{"resolution_id"}, "properties": map[string]any{"resolution_id": map[string]any{"type": "string"}}}},
	}}
}

func TestStructuredProtocolPublishesFlatDirectoryPinsCallsAndRejectsOldGeneration(t *testing.T) {
	var calls atomic.Int64
	var generation atomic.Int64
	generation.Store(1)
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		version := "a"
		if generation.Load() == 2 {
			version = "b"
		}
		if request["operation"] == "describe_tools" {
			require.NotEmpty(t, request["arguments"].(map[string]any)["operations"])
			w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat(version, 64))
			require.NoError(t, json.NewEncoder(w).Encode(structuredProtocolDirectory(version)))
			return
		}
		require.Equal(t, "query_pdf_table", request["operation"])
		require.Equal(t, strings.Repeat(version, 64), r.Header.Get("X-Structured-Manifest-SHA256"))
		w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat(version, 64))
		_, _ = io.WriteString(w, `{"status":"ok","columns":["amount"],"rows":[[9007199254740993]]}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-b", TenantID: 7}})
	r := NewToolRegistry()
	r.RegisterTool(base)
	require.True(t, r.PrepareStructuredProtocol())
	names := []string{}
	for _, definition := range r.GetModelFunctionDefinitions() {
		names = append(names, definition.Name)
	}
	require.Equal(t, []string{ToolStructuredCapabilities}, names, "the recovery entry exists before schemas, without the nested gateway")
	ctx := structuredScopeContext()
	capabilities, err := r.ExecuteTool(ctx, ToolStructuredCapabilities, json.RawMessage(`{"refresh":true}`))
	require.NoError(t, err)
	require.True(t, capabilities.Success, capabilities.Error)
	require.Equal(t, int64(1), calls.Load())
	for _, name := range []string{"structured_list_datasets", "structured_query_pdf_table", "structured_resolve_fields", "structured_confirm_fields"} {
		_, err := r.GetTool(name)
		require.NoError(t, err)
	}
	directory, _ := r.GetTool("structured_list_datasets")
	query, _ := r.GetTool("structured_query_pdf_table")
	var directorySchema, querySchema map[string]any
	require.NoError(t, json.Unmarshal(directory.Parameters(), &directorySchema))
	require.NoError(t, json.Unmarshal(query.Parameters(), &querySchema))
	directoryRequired, _ := directorySchema["required"].([]any)
	require.NotContains(t, directoryRequired, "knowledge_base_id", "authorized multi-library directory must remain discoverable")
	require.Contains(t, querySchema["required"], "knowledge_base_id")
	require.True(t, r.CanRunStructuredCallConcurrently(query.Name(), `{"knowledge_base_id":"kb-a","table_id":"table-a","sql":"SELECT amount FROM data"}`))
	require.False(t, r.CanRunStructuredCallConcurrently("structured_confirm_fields", `{"knowledge_base_id":"kb-a","resolution_id":"pending"}`))
	require.False(t, r.CanRunStructuredCallConcurrently(ToolStructuredCapabilities, `{"refresh":true}`))
	invalid, err := r.ExecuteTool(ctx, query.Name(), json.RawMessage(`{"arguments":{"table_id":"table-a","sql":"SELECT amount FROM data"}}`))
	require.NoError(t, err)
	require.False(t, invalid.Success)
	require.Equal(t, "parameter_validation_failed", invalid.Data["code"])
	require.Equal(t, false, invalid.Data["execution_started"])
	require.Equal(t, int64(1), calls.Load(), "nested/invalid arguments never visit the data service")
	args := json.RawMessage(`{"knowledge_base_id":"kb-a","table_id":"table-a","sql":"SELECT amount FROM data"}`)
	first, err := r.ExecuteTool(ctx, query.Name(), args)
	require.NoError(t, err)
	require.True(t, first.Success, first.Error)
	require.Equal(t, json.Number("9007199254740993"), first.Data["rows"].([]any)[0].([]any)[0], "source precision must survive the HTTP adapter")
	firstDescriptor := first.Data["_structured_execution"].(StructuredOperationDescriptor)
	require.Equal(t, strings.Repeat("a", 64), firstDescriptor.ManifestSHA256)
	generation.Store(2)
	updated, err := r.ExecuteTool(ctx, ToolStructuredCapabilities, json.RawMessage(`{"refresh":true}`))
	require.NoError(t, err)
	require.True(t, updated.Success, updated.Error)
	before := calls.Load()
	stale, err := query.Execute(ctx, args)
	require.NoError(t, err)
	require.False(t, stale.Success)
	require.Equal(t, "manifest_changed", stale.Data["code"])
	require.Equal(t, before, calls.Load(), "a captured old adapter cannot execute after refresh")
	current, err := r.ExecuteTool(ctx, query.Name(), args)
	require.NoError(t, err)
	require.True(t, current.Success, current.Error)
	require.Equal(t, strings.Repeat("b", 64), current.Data["_structured_execution"].(StructuredOperationDescriptor).ManifestSHA256)
	require.Equal(t, strings.Repeat("a", 64), firstDescriptor.ManifestSHA256, "old execution audit stays immutable")
}

func TestStructuredProtocolRequiresServerPinAcknowledgmentAndDoesNotRetryConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			var calls atomic.Int64
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if conflict {
					w.WriteHeader(409)
					_, _ = io.WriteString(w, `{"detail":{"code":"manifest_changed"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"status":"ok","rows":[["1"]]}`)
			})
			r := NewToolRegistry()
			r.RegisterTool(base)
			installed, err := r.InstallStructuredManifest(structuredProtocolDirectory("c"))
			require.NoError(t, err)
			require.True(t, installed.Success)
			result, err := r.ExecuteTool(structuredScopeContext(), "structured_query_pdf_table", json.RawMessage(`{"table_id":"table-a","sql":"SELECT amount FROM data"}`))
			require.NoError(t, err)
			require.False(t, result.Success)
			want := "manifest_execution_unverified"
			if conflict {
				want = "manifest_changed"
			}
			require.Equal(t, want, result.Data["code"])
			require.Equal(t, int64(1), calls.Load(), "protocol/HTTP failures do not transport-retry")
		})
	}
}

func TestStructuredProtocolFirstWinsAndUnversionedSchemasDoNotSurviveNewTurn(t *testing.T) {
	base := NewStructuredDataTool()
	r := NewToolRegistry()
	r.RegisterTool(base)
	collision := &mockTool{name: "structured_query_pdf_table", parameters: json.RawMessage(`{"type":"object"}`)}
	r.RegisterTool(collision)
	directory := structuredProtocolDirectory("d")
	delete(directory, "manifest_sha256")
	delete(directory, "protocol_version")
	installed, err := r.InstallStructuredManifest(directory)
	require.NoError(t, err)
	require.True(t, installed.Success)
	retained, _ := r.GetTool(collision.Name())
	require.Same(t, collision, retained)
	_, _, recognized := r.StructuredInvocation(collision.Name(), json.RawMessage(`{}`))
	require.False(t, recognized, "an integration cannot obtain native scope/effect by borrowing its name")
	require.False(t, r.CanRunStructuredCallConcurrently(collision.Name(), `{}`))
	_, hit := r.BeginStructuredProtocolTurn(structuredScopeContext())
	require.False(t, hit, "an unversioned service cannot supply shared cached schemas")
	_, err = r.GetTool("structured_confirm_fields")
	require.Error(t, err, "a failed new-turn negotiation cannot retain a stale state tool")
	retained, _ = r.GetTool(collision.Name())
	require.Same(t, collision, retained)
	_, err = r.GetTool(ToolStructuredCapabilities)
	require.NoError(t, err, "recovery survives schema invalidation")
}

func TestStructuredCapabilityRefreshFailureHasBoundedRecoveryAndNoDataRead(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "describe_tools", input["operation"])
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	r.PrepareStructuredProtocol()
	for index := 0; index < 5; index++ {
		result, err := r.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{"refresh":true}`))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, false, result.Data["execution_started"])
	}
	require.Equal(t, int64(3), calls.Load())
	missing, err := r.ExecuteTool(structuredScopeContext(), "invented_data_directory", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, missing.Success)
	require.Equal(t, "tool_not_available", missing.Data["code"])
	require.Contains(t, missing.Data["available_tools"], ToolStructuredCapabilities)
}

func TestStructuredCapabilityRefreshWaitsForCapturedRead(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat("e", 64))
		_, _ = io.WriteString(w, `{"status":"ok","rows":[["1"]]}`)
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	_, err := r.InstallStructuredManifest(structuredProtocolDirectory("e"))
	require.NoError(t, err)
	query, _ := r.GetTool("structured_query_pdf_table")
	go func() {
		defer close(returned)
		_, _ = query.Execute(structuredScopeContext(), json.RawMessage(`{"table_id":"a","sql":"SELECT amount FROM data"}`))
	}()
	<-entered
	refreshed := make(chan struct{})
	go func() { defer close(refreshed); _, _ = r.InstallStructuredManifest(structuredProtocolDirectory("f")) }()
	select {
	case <-refreshed:
		t.Fatal("schema changed beneath an in-flight read")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-returned
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("manifest refresh did not finish after read barrier")
	}
}

func TestStructuredSourceLocatorsFilterScopeBeforeExposingHistory(t *testing.T) {
	base := NewStructuredDataTool().WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "allowed", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "document-only", TenantID: 7, KnowledgeIDs: []string{"one-doc"}},
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	var history []chat.Message
	for _, kb := range []string{"allowed", "revoked", "document-only"} {
		id := kb + "-call"
		history = append(history, chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: id, Function: chat.FunctionCall{Name: ToolStructuredData, Arguments: fmt.Sprintf(`{"operation":"query_pdf_table","knowledge_base_id":%q,"arguments":{"table_id":%q}}`, kb, kb+"-table")}}}},
			chat.Message{Role: "tool", Name: ToolStructuredData, ToolCallID: id, Content: fmt.Sprintf(`{"status":"ok","table_id":%q,"source":{"knowledge_base_id":%q,"file_name":%q,"sha256":"original"},"provenance":{"record_sha256":"record","bundle_sha256":"bundle","plan_sha256":"plan"},"rows":[["do-not-copy-values"]]}`, kb+"-table", kb, kb+"-private-file")})
	}
	view := r.StructuredSourceLocators(structuredScopeContext(), history)
	rows := view["locators"].([]map[string]any)
	require.Len(t, rows, 1)
	require.Equal(t, "allowed-table", rows[0]["table_id"])
	require.Equal(t, false, rows[0]["current_evidence"])
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	for _, forbidden := range []string{"revoked", "document-only", "do-not-copy-values"} {
		require.NotContains(t, string(encoded), forbidden)
	}
	for index := range history {
		if history[index].Role == "tool" {
			history[index].Role = "user"
		}
	}
	require.Empty(t, r.StructuredSourceLocators(structuredScopeContext(), history)["locators"], "pasted tool JSON cannot create a source locator")
	unauthenticated := r.StructuredSourceLocators(context.Background(), history)
	require.Empty(t, unauthenticated["locators"])
}

func TestStructuredConfirmationIsConsumedOnceButNewHumanTurnCanContinue(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "confirm_fields", input["operation"])
		_, _ = io.WriteString(w, `{"kind":"structured_field_resolution","status":"needs_clarification","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}})
	base.BindSourceQuestion("first actual human reply")
	args := json.RawMessage(`{"operation":"confirm_fields","arguments":{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","note":"first actual human reply","selections":null}}`)
	first, err := base.Execute(structuredScopeContext(), args)
	require.NoError(t, err)
	require.True(t, first.Success)
	require.Contains(t, first.Output, "resolution_binding", "runtime scope survives original-argument history replay")
	require.Contains(t, first.Output, "kb-a")
	repeated, err := base.Execute(structuredScopeContext(), args)
	require.NoError(t, err)
	require.False(t, repeated.Success)
	require.Equal(t, "confirmation_already_attempted", repeated.Data["code"])
	require.Equal(t, int64(1), calls.Load())
	base.BindSourceQuestion("a later real human clarification or refusal")
	continued, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"confirm_fields","arguments":{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","note":"a later real human clarification or refusal","selections":null}}`))
	require.NoError(t, err)
	require.True(t, continued.Success)
	require.Equal(t, int64(2), calls.Load(), "a new human turn retains its opportunity to clarify or reject")
}

func TestStructuredConfirmationEarlierTurnCannotReleaseCurrentAttempt(t *testing.T) {
	base := nativeReadTestTool(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("consumption bookkeeping must not perform transport")
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	caller := types.CallerFromContext(structuredScopeContext())
	arguments := map[string]json.RawMessage{"resolution_id": json.RawMessage(`"aaaaaaaaaaaaaaaaaaaaaaaa"`)}
	first, allowed := base.beginStructuredConfirmationAttempt(caller, structuredKnowledgeScope{}, arguments)
	require.True(t, allowed)
	r.BeginStructuredHumanTurn()
	_, allowed = base.beginStructuredConfirmationAttempt(caller, structuredKnowledgeScope{}, arguments)
	require.True(t, allowed)
	base.releaseUnexecutedStructuredConfirmation(first)
	_, allowed = base.beginStructuredConfirmationAttempt(caller, structuredKnowledgeScope{}, arguments)
	require.False(t, allowed, "completion of the old turn cannot remove the later human turn's consumption fence")
}

func TestStructuredConfirmationSchemaRejectionDoesNotConsumeAnExecution(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"detail":"invalid argument"}`)
			return
		}
		_, _ = io.WriteString(w, `{"kind":"structured_field_resolution","status":"resolved","resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa"}`)
	})
	base.BindSourceQuestion("actual reply")
	args := json.RawMessage(`{"operation":"confirm_fields","arguments":{"resolution_id":"aaaaaaaaaaaaaaaaaaaaaaaa","note":"actual reply"}}`)
	first, err := base.Execute(structuredScopeContext(), args)
	require.NoError(t, err)
	require.False(t, first.Success)
	corrected, err := base.Execute(structuredScopeContext(), args)
	require.NoError(t, err)
	require.True(t, corrected.Success)
	repeated, err := base.Execute(structuredScopeContext(), args)
	require.NoError(t, err)
	require.False(t, repeated.Success)
	require.Equal(t, int64(2), calls.Load(), "only the explicitly rejected pre-execution validation can release a confirmation attempt")
}
