package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStructuredCapabilityDefaultBootstrapsOnceWithoutReadingSource(t *testing.T) {
	for _, arguments := range []string{`{}`, `{"refresh":false}`} {
		t.Run(arguments, func(t *testing.T) {
			var calls atomic.Int64
			base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var input map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
				require.Equal(t, "describe_tools", input["operation"], "capability discovery must not read a source directory or data")
				require.NotEmpty(t, input["arguments"].(map[string]any)["operations"])
				require.NoError(t, json.NewEncoder(w).Encode(structuredProtocolDirectory("a")))
			})
			registry := NewToolRegistry()
			registry.RegisterTool(base)
			require.True(t, registry.PrepareStructuredProtocol())
			_, err := registry.ExecuteTool(context.Background(), ToolStructuredCapabilities, json.RawMessage(arguments))
			require.Error(t, err, "an unauthenticated caller cannot negotiate schemas")
			require.Zero(t, calls.Load())
			for attempt := 0; attempt < 2; attempt++ {
				result, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(arguments))
				require.NoError(t, err)
				require.True(t, result.Success, result.Error)
				require.Equal(t, "ok", result.Data["status"])
				require.Equal(t, false, result.Data["source_data_read"])
				require.Len(t, result.Data["tools"], 4)
				require.Equal(t, int64(1), calls.Load(), "only the first capability inspection negotiates; warm inspection is local")
			}
		})
	}
}

func TestStructuredCapabilityDefaultRestoresCacheAndPreservesVersionChangeProtection(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var input map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		if input["operation"] == "describe_tools" {
			require.NoError(t, json.NewEncoder(w).Encode(structuredProtocolDirectory("b")))
			return
		}
		require.Equal(t, "query_pdf_table", input["operation"])
		if request.Header.Get("X-Structured-Manifest-SHA256") != strings.Repeat("b", 64) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"detail":{"code":"manifest_changed"}}`)
			return
		}
		w.Header().Set("X-Structured-Manifest-SHA256", strings.Repeat("b", 64))
		_, _ = io.WriteString(w, `{"status":"ok","rows":[[1]]}`)
	})
	previous := NewToolRegistry()
	previous.RegisterTool(base)
	installed, err := previous.InstallStructuredManifest(structuredProtocolDirectory("a"))
	require.NoError(t, err)
	require.True(t, installed.Success)

	// A later engine has a separate registry/adapter but the same authenticated
	// service identity. The first inspection must use its cached schema even
	// when no structured-source prefetch was selected before planning.
	registry := NewToolRegistry()
	registry.RegisterTool(NewStructuredDataTool())
	registry.PrepareStructuredProtocol()
	_, err = registry.ExecuteTool(context.Background(), ToolStructuredCapabilities, json.RawMessage(`{}`))
	require.Error(t, err, "a warm cache must not bypass caller authentication")
	_, err = registry.GetTool("structured_query_pdf_table")
	require.Error(t, err)
	result, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, calls.Load(), "restoring service schemas requires no source or protocol request")
	oldQuery, err := registry.GetTool("structured_query_pdf_table")
	require.NoError(t, err)
	arguments := json.RawMessage(`{"table_id":"table-a","sql":"SELECT amount FROM data"}`)
	stale, err := registry.ExecuteTool(structuredScopeContext(), oldQuery.Name(), arguments)
	require.NoError(t, err)
	require.False(t, stale.Success)
	require.Equal(t, "manifest_changed", stale.Data["code"])
	require.Equal(t, int64(1), calls.Load(), "a cached older schema cannot silently retry against a changed service")
	refreshed, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{"refresh":true}`))
	require.NoError(t, err)
	require.True(t, refreshed.Success)
	require.Equal(t, int64(2), calls.Load())
	rejected, err := oldQuery.Execute(structuredScopeContext(), arguments)
	require.NoError(t, err)
	require.False(t, rejected.Success)
	require.Equal(t, "manifest_changed", rejected.Data["code"])
	require.Equal(t, int64(2), calls.Load(), "refresh invalidates the old captured adapter before any data request")
	current, err := registry.ExecuteTool(structuredScopeContext(), oldQuery.Name(), arguments)
	require.NoError(t, err)
	require.True(t, current.Success, current.Error)
	require.Equal(t, strings.Repeat("b", 64), current.Data["_structured_execution"].(StructuredOperationDescriptor).ManifestSHA256)
}

func TestStructuredCapabilityDefaultFailureNeedsExplicitRetryAndIsNotCached(t *testing.T) {
	for _, invalidSchema := range []bool{false, true} {
		name := "transport_failure"
		if invalidSchema {
			name = "invalid_manifest"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			var ready atomic.Bool
			base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var input map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
				require.Equal(t, "describe_tools", input["operation"])
				if !ready.Load() {
					if invalidSchema {
						require.NoError(t, json.NewEncoder(w).Encode(structuredProtocolDirectory("invalid")))
					} else {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(structuredProtocolDirectory("c")))
			})
			registry := NewToolRegistry()
			registry.RegisterTool(base)
			registry.PrepareStructuredProtocol()
			first, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.False(t, first.Success)
			lastCode := "manifest_unavailable"
			if invalidSchema {
				lastCode = "manifest_invalid"
			}
			require.Equal(t, lastCode, first.Data["code"])
			again, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.False(t, again.Success, "unavailable capabilities must not be reported as a successful empty directory")
			require.Equal(t, lastCode, again.Data["last_error_code"])
			require.Equal(t, false, again.Data["source_data_read"])
			require.Contains(t, again.Data["next_step"], "refresh=true")
			require.Equal(t, int64(1), calls.Load(), "viewing a failed snapshot does not trigger an implicit retry loop")
			ready.Store(true)
			later := NewToolRegistry()
			later.RegisterTool(NewStructuredDataTool())
			later.PrepareStructuredProtocol()
			recovered, err := later.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.True(t, recovered.Success, "a failed negotiation must not poison another registry's first request")
			require.Equal(t, int64(2), calls.Load())
			refreshed, err := registry.ExecuteTool(structuredScopeContext(), ToolStructuredCapabilities, json.RawMessage(`{"refresh":true}`))
			require.NoError(t, err)
			require.True(t, refreshed.Success)
			require.NotContains(t, refreshed.Data, "last_error_code")
			require.Equal(t, int64(3), calls.Load())
		})
	}
}
