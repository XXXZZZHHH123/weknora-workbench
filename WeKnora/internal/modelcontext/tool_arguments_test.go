package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const argumentNormalizationTestSchema = `{"type":"object","required":["entries"],"properties":{"entries":{"type":"array","items":{"type":"object","required":["chunk_id","value"],"properties":{"chunk_id":{"type":"string"},"value":{"type":"string","x-weknora-exact-decimal-string":true},"quote":{"type":"string"}}}},"metadata":{"type":"object"},"literal":{"type":"string"}}}`

func TestNormalizeToolArgumentsUnwrapsDeclaredContainersWithoutChangingEvidence(t *testing.T) {
	raw := `{"entries":"[{\"chunk_id\":\"chunk://1\",\"value\":9007199254740993.125000,\"quote\":\"value 9007199254740993.125000 kg\\nkeep \\\"quote\\\"\"}]","literal":"{\"chunk_id\":\"chunk://1\"}","metadata":"{\"source_version\":123456789012345678901234567890}"}`
	got, err := NormalizeToolArguments(raw, json.RawMessage(argumentNormalizationTestSchema))
	require.NoError(t, err)
	var value map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(got), &value))
	require.Contains(t, string(value["entries"]), `"value":"9007199254740993.125000"`)
	require.Contains(t, string(value["entries"]), `"chunk_id":"chunk://1"`, "normalization cannot grant identities")
	require.Contains(t, string(value["entries"]), `kg\nkeep \"quote\"`)
	require.Equal(t, `"{\"chunk_id\":\"chunk://1\"}"`, string(value["literal"]), "JSON-looking source text stays literal")
	require.Contains(t, string(value["metadata"]), `123456789012345678901234567890`)
}

func TestNormalizeToolArgumentsRejectsAmbiguousOrIncompletePayloadsWithPaths(t *testing.T) {
	for _, test := range []struct{ name, raw, path, code string }{
		{"truncated nested container", `{"entries":"[{\"chunk_id\":\"chunk://1\""}`, "/entries", "invalid_container_json"},
		{"trailing nested prose", `{"entries":"[] use another source"}`, "/entries", "invalid_container_json"},
		{"second wrapper", `{"entries":"\"[]\""}`, "/entries", "argument_type"},
		{"wrong container", `{"entries":"{}"}`, "/entries", "argument_type"},
		{"mixed row types", `{"entries":[{"chunk_id":"chunk://1","value":"1"},"source prose"]}`, "/entries/1", "invalid_container_json"},
		{"missing selection", `{"entries":[{"value":"1"}]}`, "/entries/0/chunk_id", "required_argument"},
		{"duplicate identity", `{"entries":[{"chunk_id":"chunk://1","chunk_id":"chunk://2","value":"1"}]}`, "/entries/0/chunk_id", "duplicate_argument"},
		{"nested duplicate identity", `{"entries":"[{\"chunk_id\":\"chunk://1\",\"chunk_id\":\"chunk://2\",\"value\":\"1\"}]"}`, "/entries", "invalid_container_json"},
		{"exponent is not decimal lexeme", `{"entries":[{"chunk_id":"chunk://1","value":1e3}]}`, "/entries/0/value", "argument_type"},
		{"multiple root values", `{"entries":[]} {"entries":[]}`, "", "invalid_json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeToolArguments(test.raw, json.RawMessage(argumentNormalizationTestSchema))
			require.Equal(t, test.raw, got, "an invalid plan must not return a partially normalized call")
			var argumentError *ToolArgumentError
			require.ErrorAs(t, err, &argumentError)
			require.Equal(t, test.path, argumentError.Path)
			require.Equal(t, test.code, argumentError.Code)
		})
	}
}

func TestNormalizeToolArgumentsRequiresUnambiguousSchemaOptIn(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"value":{"type":"string"}}}`,
		`{"type":"object","properties":{"value":{"type":["array","string"],"items":{"type":"object"}}}}`,
		`{"type":"object","properties":{"value":{}}}`,
	} {
		raw := `{"value":"[{\"chunk_id\":\"chunk://1\"}]"}`
		got, err := NormalizeToolArguments(raw, json.RawMessage(schema))
		require.NoError(t, err)
		require.Equal(t, raw, got)
	}
	_, err := NormalizeToolArguments(`{"value":9007199254740993}`, json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`))
	require.Error(t, err, "a numeric ID or text field cannot silently become a string")
	raw := `{"entries":[{"chunk_id":"chunk://1","value":"000123.4500"}]}`
	got, err := NormalizeToolArguments(raw, json.RawMessage(argumentNormalizationTestSchema))
	require.NoError(t, err)
	require.Equal(t, raw, got, "already typed fields preserve their exact bytes")
}

func TestHandleCodecsPreserveExactNumericLexemes(t *testing.T) {
	r := NewRegistry(true)
	r.RegisterChunk(ChunkReference{ChunkID: "actual-chunk", KnowledgeID: "actual-document"})
	raw := `{"chunk_id":"chunk://1","huge":900719925474099312345678901234567890,"decimal":0.10000000000000000000100,"negative_zero":-0.0000,"exponent":1.234567890123456789e+99}`
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: "calculate_source_values", Arguments: raw}}}
	r.DecodeToolCalls(calls)
	require.Equal(t, raw, calls[0].ModelArguments)
	for _, lexeme := range []string{"900719925474099312345678901234567890", "0.10000000000000000000100", "-0.0000", "1.234567890123456789e+99"} {
		require.Contains(t, calls[0].Function.Arguments, lexeme)
	}
	require.Contains(t, calls[0].Function.Arguments, `"chunk_id":"actual-chunk"`)
	encoded := r.EncodeMessages([]chat.Message{{Role: "assistant", ToolCalls: []chat.ToolCall{{Function: chat.FunctionCall{Name: "calculate_source_values", Arguments: calls[0].Function.Arguments}}}}})
	require.Contains(t, encoded[0].ToolCalls[0].Function.Arguments, "900719925474099312345678901234567890")
	require.Contains(t, encoded[0].ToolCalls[0].Function.Arguments, "0.10000000000000000000100")
	require.False(t, jsonEquivalent(`{"value":9007199254740992}`, `{"value":9007199254740993}`), "codec audit must detect distinct precise numbers")
}

func TestBuiltInProviderSchemaOmitsPrivateAnnotationButRetainsExecutionContract(t *testing.T) {
	const schema = `{"type":"object","properties":{"inputs":{"type":"object","additionalProperties":{"type":"object","properties":{"value":{"type":"string","x-weknora-exact-decimal-string":true}},"examples":[{"x-weknora-exact-decimal-string":"literal example"}]}}},"$defs":{"typed":{"type":"string","x-weknora-exact-decimal-string":true}},"anyOf":[{"properties":{"other":{"type":"string","x-weknora-exact-decimal-string":true}}}]} `
	tools := []chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: "calculate_source_values", Parameters: json.RawMessage(schema)}}}
	for _, registry := range []*Registry{NewRegistry(true), nil} {
		projected := registry.EncodeTools(tools)
		var provider map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(projected[0].Function.Parameters, &provider))
		require.NotContains(t, string(projected[0].Function.Parameters), `"x-weknora-exact-decimal-string":true`)
		require.Contains(t, string(projected[0].Function.Parameters), `"x-weknora-exact-decimal-string":"literal example"`, "example data is never traversed as schema")
		require.Equal(t, schema, string(tools[0].Function.Parameters), "registered schema remains available to normalization")
		normalized, err := NormalizeToolArguments(`{"inputs":{"a":{"value":9007199254740993.125000}}}`, tools[0].Function.Parameters)
		require.NoError(t, err)
		require.Contains(t, normalized, `"value":"9007199254740993.125000"`)
	}
	for _, name := range []string{"mcp_external", "call_mcp_tool", "structured_query_series"} {
		foreign := []chat.Tool{{Type: "function", Function: chat.FunctionDef{Name: name, Parameters: json.RawMessage(schema)}}}
		projected := NewRegistry(true).EncodeTools(foreign)
		require.Equal(t, schema, string(projected[0].Function.Parameters), "foreign/native schemas own their vocabulary")
	}
	r := NewRegistry(true)
	r.SetStructuredToolSourceContracts(map[string]bool{"calculate_source_values": true})
	require.Equal(t, schema, string(r.EncodeTools(tools)[0].Function.Parameters), "current native registration cannot inherit built-in schema rewriting")
}
