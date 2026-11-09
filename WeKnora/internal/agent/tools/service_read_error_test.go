package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceReadFailurePreservesOnlyBoundedDeclaredDiagnostics(t *testing.T) {
	for _, body := range []string{
		`{"detail":{"code":"source_changed","message":"Original version changed.","authorization":"private-marker","rows":[[12345]]}}`,
		`{"error":{"code":"source_changed","message":"Original version changed.","stack":"private-marker"}}`,
	} {
		result := readServiceFailure("trusted source", http.StatusConflict, strings.NewReader(body))
		require.False(t, result.Success)
		require.Equal(t, "source_changed", result.Data["reason_code"])
		require.Equal(t, http.StatusConflict, result.Data["http_status"])
		require.Equal(t, "Original version changed.", result.Data["detail"])
		require.NotContains(t, result.Output, "private-marker")
		require.NotContains(t, result.Output, "12345")
	}
	for _, test := range []struct{ body, status string }{
		{`<html>private-marker</html>`, "invalid_json"},
		{strings.Repeat("x", serviceReadErrorLimit+1), "too_large"},
		{`{"detail":[{"private":"private-marker"}]}`, "omitted"},
	} {
		result := readServiceFailure("trusted source", http.StatusConflict, strings.NewReader(test.body))
		require.Equal(t, test.status, result.Data["detail_status"])
		require.Equal(t, "http_409", result.Data["reason_code"])
		require.NotContains(t, result.Output, "private-marker")
	}
	result := serviceReadFailure("trusted source", http.StatusConflict, []byte(`{"detail":"partial-must-not-forward"}`), errors.New("read failed"))
	require.NotContains(t, result.Output, "partial-must-not-forward")
	result = readServiceFailure("trusted source", http.StatusForbidden, strings.NewReader(`{"detail":"Current source permission expired."}`))
	require.Equal(t, http.StatusForbidden, result.Data["http_status"])
	require.Contains(t, result.Error, "permission expired")
}

func TestStructuredReadConflictPreservesSpecificReasonWithoutManifestRefreshOrReplay(t *testing.T) {
	calls := 0
	tool := nativeReadTestTool(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"The original source snapshot failed verification; no old version was read."}`))
	})
	result, err := tool.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, http.StatusConflict, result.Data["http_status"])
	require.Equal(t, "http_409", result.Data["reason_code"])
	require.Contains(t, result.Error, "snapshot failed verification")
	require.NotContains(t, result.Error, "refresh=true", "a source conflict is not necessarily a protocol manifest conflict")
	require.Equal(t, 1, calls)
}
