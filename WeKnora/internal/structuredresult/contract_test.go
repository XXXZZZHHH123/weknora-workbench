package structuredresult

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/pdf_sql_v2.json")
	require.NoError(t, err)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value map[string]any
	require.NoError(t, d.Decode(&value))
	return value
}

func resign(t *testing.T, data map[string]any) {
	t.Helper()
	contract := data["result_contract"].(map[string]any)
	delete(contract, "result_hash")
	payload := map[string]any{}
	for _, raw := range contract["hash_input_keys"].([]any) {
		key := raw.(string)
		payload[key] = data[key]
	}
	digest, err := TypedSHA256(map[string]any{"payload": payload, "contract": contract})
	require.NoError(t, err)
	contract["result_hash"] = digest
}

func TestTypedCanonicalCrossLanguageGolden(t *testing.T) {
	data := map[string]any{"n": json.Number("9007199254740993"), "s": "中<\u2028", "values": []any{nil, true, json.Number("1.25"), json.Number("-0.0")}}
	digest, err := TypedSHA256(data)
	require.NoError(t, err)
	require.Equal(t, "f6c198398de7d26f6531c811699342cf24cbf92904fbd4f7ed9b9649645d17a1", digest)
	for _, pair := range [][2]any{{json.Number("1.0"), json.Number("1e0")}, {json.Number("-0.0"), json.Number("0")}} {
		a, e := TypedSHA256(pair[0])
		require.NoError(t, e)
		b, e := TypedSHA256(pair[1])
		require.NoError(t, e)
		require.Equal(t, a, b)
	}
	a, e := TypedSHA256(json.Number("9007199254740993"))
	require.NoError(t, e)
	b, e := TypedSHA256(json.Number("9007199254740992"))
	require.NoError(t, e)
	require.NotEqual(t, a, b)
	a, e = TypedSHA256("1")
	require.NoError(t, e)
	b, e = TypedSHA256(1)
	require.NoError(t, e)
	require.NotEqual(t, a, b)
}

func TestV2ContractPythonExecutionFixtureAndLegacy(t *testing.T) {
	data := fixture(t)
	contract, err := ValidatePayload(data)
	require.NoError(t, err)
	require.NotNil(t, contract)
	require.Equal(t, "kg", contract.Columns[1].Unit.Symbol)
	require.Equal(t, "reviewed_plan_declaration", contract.Columns[1].Unit.EvidenceLevel)
	require.Equal(t, "dependency_only", contract.Capabilities.Level)
	require.False(t, contract.Capabilities.CellLineageVerified)
	require.Equal(t, "doc-fixture", contract.SourceSnapshot.KnowledgeID)
	// Trusted transport annotations are not a change to result values/versions.
	data["kind"] = "structured_analysis"
	data["source_kind"] = "pdf_table"
	data["result_status"] = "ok"
	data["source_context"] = map[string]any{"knowledge_base_id": "kb-fixture"}
	_, err = ValidatePayload(data)
	require.NoError(t, err)
	delete(data, "result_contract")
	legacy, err := Parse(data)
	require.NoError(t, err)
	require.Nil(t, legacy)
}

func TestValidatePayloadPreservesTypedTransportAndSerializedEvidence(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			data := fixture(t)
			// Transport-owned values intentionally use nonalphabetical field
			// order and a raw JSON object. They are not contract-bound source data.
			type transport struct {
				Operation  string          `json:"operation"`
				Generation uint64          `json:"generation"`
				Parameters json.RawMessage `json:"parameters"`
			}
			witness := transport{Operation: "read", Generation: 1, Parameters: json.RawMessage(`{"z":1,"a":2}`)}
			data["_transport"] = witness
			if !valid {
				data["result_contract"].(map[string]any)["result_hash"] = strings.Repeat("0", 64)
			}
			before, err := json.Marshal(data)
			require.NoError(t, err)
			_, err = ValidatePayload(data)
			if valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			after, err := json.Marshal(data)
			require.NoError(t, err)
			require.True(t, bytes.Equal(before, after), "verification must preserve exact evidence bytes on success and failure")
			require.Equal(t, witness, data["_transport"], "verification must preserve runtime-owned Go types and RawMessage")
		})
	}
}

func TestDeclaredV2CannotSilentlyDowngradeOrLoseBinding(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"null":              func(d map[string]any) { d["result_contract"] = nil },
		"version":           func(d map[string]any) { d["result_contract"].(map[string]any)["schema_version"] = 3 },
		"actual_value":      func(d map[string]any) { d["rows"].([]any)[0].([]any)[1] = "999" },
		"source_version":    func(d map[string]any) { d["provenance"].(map[string]any)["plan_sha256"] = strings.Repeat("b", 64) },
		"scope":             func(d map[string]any) { d["source_context"] = map[string]any{"knowledge_base_id": "other-kb"} },
		"missing_bound_key": func(d map[string]any) { delete(d, "sql") },
		"unit": func(d map[string]any) {
			d["result_contract"].(map[string]any)["column_metadata"].([]any)[1].(map[string]any)["unit"].(map[string]any)["symbol"] = "m"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := fixture(t)
			mutate(data)
			_, err := ValidatePayload(data)
			require.Error(t, err)
		})
	}
}

func TestRehashedInvalidSemanticClaimsAreStillRejected(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"cell_lineage": func(d map[string]any) {
			d["result_contract"].(map[string]any)["evidence_capabilities"].(map[string]any)["cell_lineage_verified"] = true
		},
		"aggregate_members": func(d map[string]any) {
			d["result_contract"].(map[string]any)["evidence_capabilities"].(map[string]any)["aggregate_membership_verified"] = true
		},
		"wrong_unit_scope": func(d map[string]any) {
			d["result_contract"].(map[string]any)["column_metadata"].([]any)[1].(map[string]any)["unit"].(map[string]any)["binding_scope"] = "same_page"
		},
		"wrong_annotation": func(d map[string]any) {
			d["result_contract"].(map[string]any)["column_metadata"].([]any)[1].(map[string]any)["unit"].(map[string]any)["evidence"].([]any)[0].(map[string]any)["source_sha256"] = strings.Repeat("c", 64)
		},
		"false_lineage_type": func(d map[string]any) {
			d["result_contract"].(map[string]any)["evidence_capabilities"].(map[string]any)["level"] = "source_cells"
		},
		"wrong_snapshot": func(d map[string]any) {
			d["result_contract"].(map[string]any)["source_snapshot"].(map[string]any)["versions"].(map[string]any)["plan_sha256"] = strings.Repeat("c", 64)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := fixture(t)
			mutate(data)
			resign(t, data)
			_, err := ValidatePayload(data)
			require.Error(t, err)
		})
	}
}

func TestRehashedV2MissingFalseOrEmptyRequiredFieldsIsInvalid(t *testing.T) {
	for _, field := range []string{"cell_lineage_verified", "aggregate_membership_verified", "source_cells_available", "column_dependencies_verified"} {
		t.Run(field, func(t *testing.T) {
			data := fixture(t)
			delete(data["result_contract"].(map[string]any)["evidence_capabilities"].(map[string]any), field)
			resign(t, data)
			_, err := ValidatePayload(data)
			require.ErrorContains(t, err, "required_metadata")
		})
	}
	for _, field := range []string{"index", "source_columns", "evidence"} {
		t.Run(field, func(t *testing.T) {
			data := fixture(t)
			column := data["result_contract"].(map[string]any)["column_metadata"].([]any)[0].(map[string]any)
			switch field {
			case "index":
				delete(column, field)
			case "source_columns":
				delete(column["dependency"].(map[string]any), field)
			case "evidence":
				delete(column["unit"].(map[string]any), field)
			}
			resign(t, data)
			_, err := ValidatePayload(data)
			require.ErrorContains(t, err, "required_metadata")
		})
	}
}
