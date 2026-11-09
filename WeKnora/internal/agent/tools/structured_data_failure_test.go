package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type structuredFailureRoundTrip func(*http.Request) (*http.Response, error)

func (f structuredFailureRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type structuredFailureTimeout struct{}

func (structuredFailureTimeout) Error() string   { return "synthetic private transport timeout" }
func (structuredFailureTimeout) Timeout() bool   { return true }
func (structuredFailureTimeout) Temporary() bool { return false }

type structuredFailureReader struct{ err error }

func (r structuredFailureReader) Read([]byte) (int, error) { return 0, r.err }
func (structuredFailureReader) Close() error               { return nil }

func structuredFailureTool(transport structuredFailureRoundTrip, scopes int) *StructuredDataTool {
	tool := &StructuredDataTool{endpoint: "http://structured.test", key: []byte(strings.Repeat("a", 48)), client: &http.Client{Transport: transport}}
	targets := types.SearchTargets{}
	for index := range scopes {
		targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-" + string(rune('a'+index)), TenantID: 7})
	}
	return tool.WithSearchTargets(targets)
}

func structuredFailureResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestStructuredMultiScopeDeadlineKeepsCompletedCatalogAndUnqueriedScopes(t *testing.T) {
	ctx, cancel := context.WithTimeout(structuredScopeContext(), 100*time.Millisecond)
	defer cancel()
	seen := []string{}
	tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
		kb := decodeStructuredScopeIdentity(t, request)["knowledge_base_id"].(string)
		seen = append(seen, kb)
		if kb == "kb-a" {
			return structuredFailureResponse(request, http.StatusOK, `{"datasets":[{"dataset_id":"actual-source","is_latest_for_source":true}],"pdf_tables":[],"literal_fields":[{"dataset_id":"actual-source","metric":"Printed header"}]}`), nil
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	}, 3)
	result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Find the requested source"}}`))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, result.Success)
	require.Equal(t, []string{"kb-a", "kb-b"}, seen)
	require.Equal(t, "partial", result.Data["status"])
	require.Equal(t, "partial", result.Data["dataset_catalog_status"])
	require.Equal(t, "request_timeout", result.Data["interruption_code"])
	require.Equal(t, false, result.Data["catalog_complete"])
	require.Equal(t, true, result.Data["execution_started"])
	require.Equal(t, false, result.Data["source_data_read"])
	require.Equal(t, true, result.Data["catalog_data_read"])
	require.Equal(t, 1, result.Data["queried_knowledge_base_count"])
	require.Equal(t, 1, result.Data["unavailable_knowledge_base_count"])
	require.Equal(t, 1, result.Data["not_queried_knowledge_base_count"])
	rows := structuredRecallRows(result.Data["knowledge_base_catalogs"])
	require.Equal(t, "actual-source", structuredRecallRows(rows[0]["datasets"])[0]["dataset_id"])
	require.Equal(t, "queried", rows[0]["catalog_status"])
	require.Equal(t, "unavailable", rows[1]["catalog_status"])
	require.Equal(t, "request_timeout", rows[1]["code"])
	require.Equal(t, "not_queried", rows[2]["catalog_status"])
	require.NotContains(t, rows[1], "datasets")
	require.NotContains(t, rows[2], "pdf_tables")
	require.Len(t, result.Data["knowledge_bases"], 3, "authorization remains navigation, never a substitute for actual catalog reads")
	require.Len(t, result.Data["literal_fields"], 1)
	require.Contains(t, result.Output, "Printed header")
}

func TestStructuredMultiScopeCancellationNeverReturnsSuccessfulBaseDirectory(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before request", false: "during request"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(structuredScopeContext())
			defer cancel()
			if before {
				cancel()
			}
			calls := 0
			tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
				calls++
				cancel()
				return nil, request.Context().Err()
			}, 2)
			result, err := tool.Execute(ctx, json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Current source directory"}}`))
			require.ErrorIs(t, err, context.Canceled)
			require.False(t, result.Success)
			require.NotEmpty(t, result.Error)
			require.Equal(t, false, result.Data["source_data_read"])
			require.Equal(t, false, result.Data["catalog_data_read"])
			require.Equal(t, false, result.Data["catalog_complete"])
			require.Equal(t, "request_cancelled", result.Data["interruption_code"])
			require.Len(t, result.Data["knowledge_bases"], 2)
			rows := structuredRecallRows(result.Data["knowledge_base_catalogs"])
			if before {
				require.Zero(t, calls)
				require.Equal(t, false, result.Data["execution_started"])
				require.Equal(t, "not_queried", result.Data["dataset_catalog_status"])
				require.Equal(t, "not_queried", rows[0]["catalog_status"])
			} else {
				require.Equal(t, 1, calls)
				require.Equal(t, true, result.Data["execution_started"])
				require.Equal(t, "unavailable", result.Data["dataset_catalog_status"])
				require.Equal(t, "unavailable", rows[0]["catalog_status"])
			}
			require.Equal(t, "not_queried", rows[1]["catalog_status"])
		})
	}
}

func TestStructuredMultiScopeFailureAndUnknownPayloadDoNotClaimEmptyData(t *testing.T) {
	for _, allFailed := range []bool{true, false} {
		t.Run(map[bool]string{true: "all failed", false: "partial"}[allFailed], func(t *testing.T) {
			calls := 0
			tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
				calls++
				switch decodeStructuredScopeIdentity(t, request)["knowledge_base_id"] {
				case "kb-a":
					return structuredFailureResponse(request, http.StatusServiceUnavailable, `private provider body`), nil
				case "kb-b":
					if !allFailed {
						return structuredFailureResponse(request, http.StatusOK, `{"datasets":[],"pdf_tables":[]}`), nil
					}
				}
				return structuredFailureResponse(request, http.StatusOK, `{"status":"ok","unrelated":[]}`), nil
			}, 3)
			result, err := tool.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Current catalog"}}`))
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Equal(t, 3, calls, "HTTP and unknown-payload failures do not replay transport")
			require.Equal(t, false, result.Data["catalog_complete"])
			require.NotContains(t, result.Output, "private provider body")
			rows := structuredRecallRows(result.Data["knowledge_base_catalogs"])
			for _, index := range []int{0, 2} {
				row := rows[index]
				require.Equal(t, "unavailable", row["catalog_status"])
				require.NotContains(t, row, "datasets")
				require.NotContains(t, row, "pdf_table_count")
			}
			if allFailed {
				require.Equal(t, "unavailable", result.Data["dataset_catalog_status"])
				require.Equal(t, false, result.Data["source_data_read"])
				require.Equal(t, false, result.Data["catalog_data_read"])
			} else {
				require.Equal(t, "partial", result.Data["dataset_catalog_status"])
				require.Equal(t, false, result.Data["source_data_read"])
				require.Equal(t, true, result.Data["catalog_data_read"])
				require.Equal(t, "queried", rows[1]["catalog_status"])
				require.Empty(t, rows[1]["datasets"], "a successful actual empty response remains distinct from unavailable")
			}
		})
	}
}

func TestStructuredMultiScopeResponseTimeoutDoesNotReplayOrHideOtherCatalogs(t *testing.T) {
	calls := 0
	tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
		calls++
		if decodeStructuredScopeIdentity(t, request)["knowledge_base_id"] == "kb-a" {
			return nil, structuredFailureTimeout{}
		}
		return structuredFailureResponse(request, http.StatusOK, `{"datasets":[],"pdf_tables":[]}`), nil
	}, 2)
	result, err := tool.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"Current catalog"}}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 2, calls)
	rows := structuredRecallRows(result.Data["knowledge_base_catalogs"])
	require.Equal(t, "request_timeout", rows[0]["code"])
	require.Equal(t, "queried", rows[1]["catalog_status"])
	require.Equal(t, "partial", result.Data["dataset_catalog_status"])
}

func TestStructuredTransportRetriesOnlyReadConnectionFailures(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		operation string
		failure   error
		bodyError bool
		wantCalls int
		wantOK    bool
	}{
		{"read dial failure", "list_datasets", &net.OpError{Op: "dial", Err: errors.New("synthetic connection refused")}, false, 2, true},
		{"read response timeout", "list_datasets", structuredFailureTimeout{}, false, 1, false},
		{"read deadline", "list_datasets", context.DeadlineExceeded, false, 1, false},
		{"read interrupted connection", "list_datasets", &net.OpError{Op: "read", Err: errors.New("synthetic reset after submission")}, false, 1, false},
		{"read body timeout", "list_datasets", context.DeadlineExceeded, true, 1, false},
		{"resolution dial failure", "resolve_fields", &net.OpError{Op: "dial", Err: errors.New("synthetic connection refused")}, false, 1, false},
		{"confirmation dial failure", "confirm_fields", &net.OpError{Op: "dial", Err: errors.New("synthetic connection refused")}, false, 1, false},
		{"execution dial failure", "execute_field_plan", &net.OpError{Op: "dial", Err: errors.New("synthetic connection refused")}, false, 1, false},
		{"evidence dial failure", "get_result_evidence", &net.OpError{Op: "dial", Err: errors.New("synthetic connection refused")}, false, 1, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if scenario.bodyError {
						response := structuredFailureResponse(request, http.StatusOK, "")
						response.Body = structuredFailureReader{err: scenario.failure}
						return response, nil
					}
					return nil, scenario.failure
				}
				return structuredFailureResponse(request, http.StatusOK, `{"datasets":[],"pdf_tables":[]}`), nil
			}, 1)
			args, err := json.Marshal(map[string]any{"operation": scenario.operation, "arguments": map[string]string{"resolution_id": "actual-resolution"}})
			require.NoError(t, err)
			result, err := tool.Execute(structuredScopeContext(), args)
			require.Equal(t, scenario.wantCalls, calls)
			if scenario.wantOK {
				require.NoError(t, err)
				require.True(t, result.Success)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.NotContains(t, err.Error(), "synthetic")
				if scenario.bodyError || scenario.name == "read response timeout" || scenario.name == "read deadline" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
			}
			if scenario.operation == "confirm_fields" {
				result, err = tool.Execute(structuredScopeContext(), args)
				require.NoError(t, err)
				require.False(t, result.Success)
				require.Equal(t, "confirmation_already_attempted", result.Data["code"])
				require.Equal(t, 1, calls, "a transport failure cannot erase the human confirmation attempt")
			}
		})
	}
}

func TestStructuredInterruptedCatalogProjectionKeepsFailureAndCanonicalEvidence(t *testing.T) {
	data := map[string]any{
		"kind": "structured_knowledge_scope_directory", "status": "partial", "code": "catalog_partial", "reason_code": "catalog_partial",
		"interruption_code": "request_cancelled", "dataset_catalog_status": "partial", "catalog_complete": false,
		"execution_started": true, "source_data_read": false, "catalog_data_read": true, "queried_knowledge_base_count": 1, "unavailable_knowledge_base_count": 1, "not_queried_knowledge_base_count": 1,
		"knowledge_base_catalogs": []map[string]any{
			{"knowledge_base_id": "kb-a", "catalog_status": "queried", "datasets": []map[string]any{{"dataset_id": "actual-source", "sheets": []string{strings.Repeat("Original sheet ", 3000)}}}},
			{"knowledge_base_id": "kb-b", "catalog_status": "unavailable", "code": "request_cancelled"},
			{"knowledge_base_id": "kb-c", "catalog_status": "not_queried"},
		},
	}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	original := &types.ToolResult{Success: false, Data: data, Output: string(encoded), Error: "Only a partial catalog was obtained"}
	ctx, cancel := context.WithCancel(WithOutputBudget(context.Background(), 1200))
	cancel()
	result := fitStructuredRecallDirectory(ctx, original)
	require.False(t, result.Success)
	require.Equal(t, original.Error, result.Error)
	require.Equal(t, original.Data, result.Data, "output fitting must preserve actual successful scope evidence for runtime review")
	require.LessOrEqual(t, utf8.RuneCountInString(result.Output), 1200)
	var visible map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Output), &visible))
	require.Equal(t, "partial", visible["status"])
	require.Equal(t, "request_cancelled", visible["interruption_code"])
	require.Equal(t, true, visible["catalog_detail_omitted"])
	require.Equal(t, false, visible["catalog_complete"])
	require.Equal(t, false, visible["source_data_read"])
	require.Equal(t, true, visible["catalog_data_read"])
	rows := structuredRecallRows(visible["knowledge_base_catalogs"])
	require.Len(t, rows, 3)
	require.Equal(t, "queried", rows[0]["catalog_status"])
	require.Equal(t, "unavailable", rows[1]["catalog_status"])
	require.Equal(t, "not_queried", rows[2]["catalog_status"])
}

func TestStructuredMultiScopeCatalogFamiliesFollowActualServiceContract(t *testing.T) {
	for _, test := range []struct {
		name, payload, scopeStatus string
		complete, catalogRead      bool
	}{
		{"optional empty PDF family omitted", `{"datasets":[]}`, "queried", true, true},
		{"both families explicitly empty", `{"datasets":[],"pdf_tables":[]}`, "queried", true, true},
		{"required Excel family missing", `{"pdf_tables":[{"table_id":"actual-pdf","status":"reviewed"}]}`, "partial", false, true},
		{"both families unknown", `{"status":"ok"}`, "unavailable", false, false},
		{"required family malformed", `{"datasets":{},"pdf_tables":[]}`, "unavailable", false, false},
		{"present optional family malformed", `{"datasets":[],"pdf_tables":null}`, "unavailable", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
				return structuredFailureResponse(request, http.StatusOK, test.payload), nil
			}, 2)
			result, err := tool.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{"question":"List current source candidates"}}`))
			require.NoError(t, err)
			require.Equal(t, test.complete, result.Success)
			require.Equal(t, test.complete, result.Data["catalog_complete"])
			require.Equal(t, "authorized_scope_requests", result.Data["catalog_completion_scope"])
			require.Equal(t, false, result.Data["source_data_read"], "navigation is not source-body or numerical evidence")
			require.Equal(t, test.catalogRead, result.Data["catalog_data_read"])
			for _, row := range structuredRecallRows(result.Data["knowledge_base_catalogs"]) {
				require.Equal(t, test.scopeStatus, row["catalog_status"])
				if test.name == "required Excel family missing" {
					require.Equal(t, "actual-pdf", structuredRecallRows(row["pdf_tables"])[0]["table_id"], "retain the actual known family as navigation")
					require.NotContains(t, row, "datasets")
					require.Equal(t, "unavailable", row["excel_catalog_status"])
				}
				if test.name == "optional empty PDF family omitted" {
					require.Equal(t, "not_listed", row["pdf_catalog_status"])
					require.NotContains(t, row, "pdf_table_count", "the optional omission never mints a statistic")
				}
			}
		})
	}
}
