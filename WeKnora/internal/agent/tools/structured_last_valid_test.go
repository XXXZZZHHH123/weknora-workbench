package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type structuredLastValidFixture struct {
	Arguments    map[string]any  `json:"arguments"`
	Legacy       json.RawMessage `json:"legacy"`
	WithEndpoint json.RawMessage `json:"with_endpoint"`
}

func loadStructuredLastValidFixtures(t *testing.T) map[string]structuredLastValidFixture {
	t.Helper()
	// Both opt-in and legacy responses were captured from the Python signed
	// HTTP operation using one synthetic field and a real zero observation.
	var fixtures map[string]structuredLastValidFixture
	data, err := os.ReadFile("testdata/structured_last_valid_results.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixtures))
	return fixtures
}

func fixtureCoverage(data map[string]any, operation string) map[string]any {
	if operation == "analyze_table" {
		data = data["inputs"].(map[string]any)["measure"].(map[string]any)
	}
	return data["coverage"].(map[string]any)
}

func TestStructuredLastValidNegotiationAndLegacyResults(t *testing.T) {
	fixtures := loadStructuredLastValidFixtures(t)
	for _, operation := range []string{"query_series", "analyze_table"} {
		for _, gateway := range []string{ToolStructuredData, "structured_" + operation} {
			for _, scenario := range []struct {
				name, setting                              string
				oldSchema, request, endpoint, wire, notice bool
			}{
				{name: "opt-in", request: true, endpoint: true, wire: true},
				{name: "legacy body", request: true, wire: true, notice: true},
				{name: "legacy schema", request: true, oldSchema: true, notice: true},
				{name: "disabled", setting: " OFF ", request: true},
				{name: "omitted"},
			} {
				t.Run(gateway+"/"+operation+"/"+scenario.name, func(t *testing.T) {
					t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", scenario.setting)
					fixture := fixtures[operation]
					data := describedStructuredReadTools(t)
					if scenario.oldSchema {
						for _, item := range data["tools"].([]any) {
							descriptor := item.(map[string]any)
							if descriptor["name"] == operation {
								delete(descriptor["inputSchema"].(map[string]any)["properties"].(map[string]any), structuredLastValidArgument)
							}
						}
					}
					calls := 0
					base := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
						calls++
						require.Equal(t, "/api/tools/read", request.URL.Path)
						var body struct {
							Operation string
							Arguments map[string]any
						}
						require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
						require.Equal(t, operation, body.Operation)
						flag, present := body.Arguments[structuredLastValidArgument]
						require.Equal(t, scenario.wire, present)
						if present {
							require.Equal(t, true, flag)
						}
						if scenario.endpoint {
							return structuredFailureResponse(request, http.StatusOK, string(fixture.WithEndpoint)), nil
						}
						return structuredFailureResponse(request, http.StatusOK, string(fixture.Legacy)), nil
					}, 1)
					base.BaseTool = NewBaseTool(ToolStructuredData, "", nil)
					registry := NewToolRegistry()
					registry.RegisterTool(base)
					manifest, err := registry.InstallStructuredManifest(data)
					require.NoError(t, err)
					require.True(t, manifest.Success)
					tool, err := registry.GetTool("structured_" + operation)
					require.NoError(t, err)
					if scenario.oldSchema || scenario.setting != "" {
						require.NotContains(t, string(tool.Parameters()), `"include_last_valid"`)
						require.NotContains(t, tool.Description(), "include_last_valid=true")
					} else {
						require.Contains(t, string(tool.Parameters()), `"include_last_valid"`)
						require.Contains(t, tool.Description(), "include_last_valid=true")
					}

					arguments := map[string]any{}
					for key, value := range fixture.Arguments {
						arguments[key] = value
					}
					if scenario.request {
						arguments[structuredLastValidArgument] = true
					}
					var input any = arguments
					if gateway == ToolStructuredData {
						input = map[string]any{"operation": operation, "arguments": arguments}
					}
					raw, err := json.Marshal(input)
					require.NoError(t, err)
					result, err := registry.ExecuteTool(structuredScopeContext(), gateway, raw)
					require.NoError(t, err)
					require.True(t, result.Success, result.Error)
					require.Equal(t, 1, calls)
					contract, err := structuredresult.ValidatePayload(result.Data)
					require.NoError(t, err)
					require.NotNil(t, contract)
					view := structuredOperationOutputObject(result.Output)
					require.NotNil(t, view)
					visibleContract, err := structuredresult.ValidatePayload(view)
					require.NoError(t, err)
					require.Equal(t, contract.ResultHash, visibleContract.ResultHash)
					if scenario.notice {
						require.Contains(t, view["last_valid_notice"], "本次未取得")
					} else {
						require.NotContains(t, view, "last_valid_notice")
					}
					require.NotContains(t, result.Data, "last_valid_notice")
					if scenario.endpoint {
						last := fixtureCoverage(view, operation)["last_valid"].(map[string]any)
						require.Equal(t, "before_range", last["direction"])
						value, err := last["rows"].([]any)[0].(map[string]any)["value"].(json.Number).Float64()
						require.NoError(t, err)
						require.Zero(t, value)
					}
				})
			}
		}
	}
}

func TestStructuredLastValidRejectsTamperingAndRechecksCurrentScope(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	for operation, fixture := range loadStructuredLastValidFixtures(t) {
		t.Run(operation, func(t *testing.T) {
			ctx := types.WithResearchAuthorization(structuredScopeContext(), "Bearer current-grant")
			tampered := structuredOperationOutputObject(string(fixture.WithEndpoint))
			coverage := fixtureCoverage(tampered, operation)
			coverage["last_valid"].(map[string]any)["rows"].([]any)[0].(map[string]any)["value"] = 999
			tamperedBody, err := json.Marshal(tampered)
			require.NoError(t, err)
			delete(coverage, "last_valid")
			removedBody, err := json.Marshal(tampered)
			require.NoError(t, err)
			delete(tampered, "result_contract")
			noContract, err := json.Marshal(tampered)
			require.NoError(t, err)
			responses := []struct {
				status int
				body   string
			}{
				{http.StatusOK, string(fixture.WithEndpoint)},
				{http.StatusForbidden, `{"detail":"知识库读取授权已失效"}`},
				{http.StatusOK, string(tamperedBody)},
				{http.StatusOK, string(removedBody)},
				{http.StatusOK, string(noContract)},
				{http.StatusOK, `{"status":"ok","rows":[]}`},
				{http.StatusOK, `{"status":"needs_clarification","kind":"structured_source_binding"}`},
				{http.StatusOK, strings.Repeat("{}", 512*1024)},
			}
			base := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
				require.NotEmpty(t, responses)
				require.Equal(t, "Bearer current-grant", request.Header.Get("Authorization"))
				current := responses[0]
				responses = responses[1:]
				return structuredFailureResponse(request, current.status, current.body), nil
			}, 1)
			tool := operationToolByName(t, NewStructuredReadOperationTools(base, describedStructuredReadTools(t)), "structured_"+operation)
			fixture.Arguments[structuredLastValidArgument] = true
			args, err := json.Marshal(fixture.Arguments)
			require.NoError(t, err)
			run := func() *types.ToolResult {
				result, err := tool.Execute(ctx, args)
				require.NoError(t, err)
				return result
			}
			require.True(t, run().Success)
			revoked := run()
			require.False(t, revoked.Success)
			require.Equal(t, 403, revoked.Data["http_status"])
			require.NotContains(t, revoked.Output, "last_valid_notice")
			modified := run()
			require.False(t, modified.Success)
			require.Equal(t, "structured_last_valid_unverified", modified.Data["code"])
			require.NotContains(t, modified.Output, `"value":999`)
			for range 2 {
				missing := run()
				require.False(t, missing.Success)
				require.Equal(t, "structured_last_valid_unverified", missing.Data["code"])
				require.NotContains(t, missing.Output, "last_valid_notice")
			}
			legacy := run()
			require.True(t, legacy.Success)
			require.Contains(t, legacy.Output, `"last_valid_notice"`)
			unresolved := run()
			require.Equal(t, "needs_clarification", unresolved.Data["status"])
			require.NotContains(t, unresolved.Output, "last_valid_notice")
			truncated := run()
			require.False(t, truncated.Success)
			require.Equal(t, "structured_result_output_truncated", truncated.Data["code"])
			require.Empty(t, responses)
		})
	}
}

func TestStructuredLastValidSignsNegotiatedAPIKeyScope(t *testing.T) {
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	type capturedRead struct {
		identity  map[string]any
		arguments map[string]any
	}
	var reads []capturedRead
	base := structuredFailureTool(func(request *http.Request) (*http.Response, error) {
		var body struct{ Arguments map[string]any }
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		reads = append(reads, capturedRead{decodeStructuredScopeIdentity(t, request), body.Arguments})
		response := structuredFailureResponse(request, http.StatusOK, `{"status":"ok","rows":[]}`)
		response.Header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return response, nil
	}, 1)
	base.endpoint = "http://structured-machine-grant.test"
	base.BaseTool = NewBaseTool(ToolStructuredData, "", nil)
	registry := NewToolRegistry()
	registry.RegisterTool(base)
	install := func(versioned, endpoint bool) {
		manifest := describedStructuredReadTools(t)
		if versioned {
			manifest["manifest_sha256"] = strings.Repeat("a", 64)
			manifest["protocol_version"] = 2
		}
		if !endpoint {
			for _, item := range manifest["tools"].([]any) {
				definition := item.(map[string]any)
				if definition["name"] == "query_series" {
					delete(definition["inputSchema"].(map[string]any)["properties"].(map[string]any), structuredLastValidArgument)
				}
			}
		}
		result, err := registry.InstallStructuredManifest(manifest)
		require.NoError(t, err)
		require.True(t, result.Success)
	}
	ctx := structuredScopeContext()
	key := types.TenantAPIKeyScope{KeyID: 8, ScopeType: types.APIKeyScopeTenant,
		KnowledgeBaseIDs: types.StringArray{"kb-b", "kb-a"}, Capabilities: types.StringArray{"retrieve", "chat"}}
	run := func(caller context.Context, enabled bool) capturedRead {
		args := map[string]any{"series_id": "measure", "dataset_id": "aaaaaaaaaaaaaaaaaaaaaaaa"}
		if enabled {
			args[structuredLastValidArgument] = true
		}
		encoded, err := json.Marshal(args)
		require.NoError(t, err)
		previous := len(reads)
		result, err := registry.ExecuteTool(caller, "structured_query_series", encoded)
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.Len(t, reads, previous+1)
		return reads[previous]
	}

	install(true, true)
	const privateScope = "api_key_scope_sha256"
	machine := types.WithTenantAPIKeyScope(ctx, key)
	first := run(machine, true)
	digest := first.identity[privateScope]
	require.Regexp(t, `^[a-f0-9]{64}$`, digest)
	require.Equal(t, "api_key", first.identity["principal_type"])
	require.Equal(t, true, first.arguments[structuredLastValidArgument])
	require.NotContains(t, first.arguments, privateScope)
	require.NotContains(t, first.identity, "key_id")

	equivalent := key
	equivalent.Name = "updated name"
	equivalent.KnowledgeBaseIDs = types.StringArray{"kb-a", " kb-b", "kb-a"}
	equivalent.Capabilities = types.StringArray{" CHAT ", "retrieve", "chat"}
	require.Equal(t, digest, run(types.WithTenantAPIKeyScope(ctx, equivalent), true).identity[privateScope])
	other, narrowedKBs, narrowedCapabilities, fullAccess, platform, noID := key, key, key, key, key, key
	other.KeyID++
	narrowedKBs.KnowledgeBaseIDs = types.StringArray{"kb-a"}
	narrowedCapabilities.Capabilities = types.StringArray{"retrieve"}
	fullAccess.FullAccess = true
	platform.ScopeType = types.APIKeyScopePlatform
	noID.KeyID = 0
	for _, changed := range []types.TenantAPIKeyScope{other, narrowedKBs, narrowedCapabilities, fullAccess} {
		changedDigest := run(types.WithTenantAPIKeyScope(ctx, changed), true).identity[privateScope]
		require.Regexp(t, `^[a-f0-9]{64}$`, changedDigest)
		require.NotEqual(t, digest, changedDigest)
	}
	require.NotEqual(t, digest, structuredAPIKeyScopeDigest(platform.Normalize()))
	require.NotContains(t, run(types.WithTenantAPIKeyScope(ctx, noID), true).identity, privateScope)
	require.NotContains(t, run(machine, false).identity, privateScope)
	revoked := key
	revoked.KnowledgeBaseIDs = types.StringArray{"kb-b"}
	beforeRevocation := len(reads)
	denied, err := registry.ExecuteTool(types.WithTenantAPIKeyScope(ctx, revoked), "structured_query_series",
		json.RawMessage(`{"series_id":"measure","include_last_valid":true}`))
	require.Error(t, err)
	require.Equal(t, "scope_not_authorized", denied.Data["code"])
	require.Len(t, reads, beforeRevocation)
	user := run(types.WithResearchAuthorization(ctx, "Bearer test-human"), true)
	require.Contains(t, user.identity, "authorization_sha256")
	require.NotContains(t, user.identity, privateScope)

	install(false, true)
	unversioned := run(machine, true)
	require.Equal(t, true, unversioned.arguments[structuredLastValidArgument])
	require.NotContains(t, unversioned.identity, privateScope)
	install(true, false)
	legacy := run(machine, true)
	require.NotContains(t, legacy.arguments, structuredLastValidArgument)
	require.NotContains(t, legacy.identity, privateScope)

	install(true, true)
	base.WithSearchTargets(nil)
	for _, unscopedKey := range []types.TenantAPIKeyScope{fullAccess, {KeyID: 9, FullAccess: true}} {
		unscoped := run(types.WithTenantAPIKeyScope(ctx, unscopedKey), true)
		require.NotContains(t, unscoped.identity, privateScope)
		require.NotContains(t, unscoped.identity, "principal_type")
	}

	base.WithSearchTargets(types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-a", TenantID: 7}})
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
	disabled := run(machine, true)
	require.NotContains(t, disabled.arguments, structuredLastValidArgument)
	require.NotContains(t, disabled.identity, privateScope)
}
