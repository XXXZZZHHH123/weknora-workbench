package structuredresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type Contract struct {
	SchemaVersion  int          `json:"schema_version"`
	HashAlgorithm  string       `json:"hash_algorithm"`
	ResultID       string       `json:"result_id"`
	ResultHash     string       `json:"result_hash"`
	HashInputKeys  []string     `json:"hash_input_keys"`
	SourceSnapshot Snapshot     `json:"source_snapshot"`
	Execution      Execution    `json:"execution"`
	Columns        []Column     `json:"column_metadata"`
	Capabilities   Capabilities `json:"evidence_capabilities"`
}

type Snapshot struct {
	Kind              string            `json:"kind"`
	SourceSHA256      string            `json:"source_sha256"`
	File              string            `json:"file"`
	TableID           string            `json:"table_id,omitempty"`
	DatasetID         string            `json:"dataset_id,omitempty"`
	KnowledgeBaseID   string            `json:"knowledge_base_id,omitempty"`
	KnowledgeID       string            `json:"knowledge_id,omitempty"`
	Versions          map[string]string `json:"versions"`
	InputDigestSHA256 string            `json:"input_digest_sha256"`
	Scope             string            `json:"scope"`
}

type Execution struct {
	Kind                string `json:"kind"`
	Engine              string `json:"engine"`
	EngineVersion       string `json:"engine_version"`
	NumericSemantics    string `json:"numeric_semantics"`
	SQLSHA256           string `json:"sql_sha256,omitempty"`
	InputScope          string `json:"input_scope"`
	CellLineageVerified bool   `json:"cell_lineage_verified"`
	Operation           string `json:"operation,omitempty"`
	ColumnMetadataScope string `json:"column_metadata_scope,omitempty"`
}

type Column struct {
	Index       int        `json:"index"`
	Name        string     `json:"name"`
	DisplayName string     `json:"display_name"`
	LogicalType string     `json:"logical_type"`
	Dependency  Dependency `json:"dependency"`
	Unit        Unit       `json:"unit"`
}

type Dependency struct {
	Kind                string   `json:"kind"`
	SourceColumns       []string `json:"source_columns"`
	SourceRowDependency bool     `json:"source_row_dependency"`
	TechnicalMetadata   bool     `json:"technical_metadata"`
}

type Unit struct {
	Status        string           `json:"status"`
	Symbol        string           `json:"symbol,omitempty"`
	EvidenceLevel string           `json:"evidence_level"`
	BindingScope  string           `json:"binding_scope"`
	Evidence      []map[string]any `json:"evidence"`
	Expression    string           `json:"expression,omitempty"`
	Quantity      string           `json:"quantity,omitempty"`
	Reason        string           `json:"reason,omitempty"`
}

type Capabilities struct {
	Level                       string `json:"level"`
	ColumnDependenciesVerified  bool   `json:"column_dependencies_verified"`
	CellLineageVerified         bool   `json:"cell_lineage_verified"`
	SourceCellsAvailable        bool   `json:"source_cells_available"`
	AggregateMembershipVerified bool   `json:"aggregate_membership_verified"`
}

var hashInputKeys = strings.Fields(`columns rows sql source table_id dataset_id series
filters requested_range coverage provenance row_provenance record_sha256 bundle_sha256 plan_sha256 draft_id
inputs input_count input_complete output_complete returned_row_count output_truncated truncated offset
total_row_count record_count scope_record_count valid_count missing_count aggregate extremum_rows extremum_count rows_are
actual_first_date actual_last_date valid_first_date valid_last_date observation_coverage source_semantics source_evidence
sql_source_dependency_verified sql_column_dependencies sql_source_dependency_method notice warnings page_context
page_context_scope extraction_verified`)

// Parse is also a validator. An absent contract is legacy (nil, nil); a present
// unsupported/malformed contract is an error, never a downgrade to legacy.
func Parse(data map[string]any) (*Contract, error) { return ValidatePayload(data) }

func ValidatePayload(data map[string]any) (*Contract, error) {
	raw, exists := data["result_contract"]
	if !exists {
		return nil, nil
	}
	whole, marshalErr := json.Marshal(data)
	if marshalErr != nil || len(whole) > 8*1024*1024 {
		return nil, fmt.Errorf("structured_result_payload_invalid_or_over_budget")
	}
	allDecoder := json.NewDecoder(bytes.NewReader(whole))
	allDecoder.UseNumber()
	// Decode into a fresh map. Decoding into the caller's non-nil map replaces
	// typed transport witnesses (and RawMessage) with generic JSON values,
	// changing serialized field order and invalidating immutable source hashes.
	var normalized map[string]any
	if allDecoder.Decode(&normalized) != nil {
		return nil, fmt.Errorf("structured_result_payload_invalid")
	}
	data = normalized
	raw = data["result_contract"]
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) > 256*1024 {
		return nil, fmt.Errorf("structured_result_contract_invalid_or_over_budget")
	}
	var c Contract
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil || c.SchemaVersion != 2 || c.HashAlgorithm != HashAlgorithm {
		return nil, fmt.Errorf("structured_result_contract_version_or_shape_invalid")
	}
	if !hexSize(c.ResultID, 12) || !hexSize(c.ResultHash, 32) {
		return nil, fmt.Errorf("structured_result_contract_identity_invalid")
	}
	if id, exists := data["result_id"]; exists && id != c.ResultID {
		return nil, fmt.Errorf("structured_result_contract_result_id_mismatch")
	}
	keys := []string{}
	for _, key := range hashInputKeys {
		if _, ok := data[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, c.HashInputKeys) {
		return nil, fmt.Errorf("structured_result_contract_binding_keys_invalid")
	}
	contractMap := map[string]any{}
	// Decode again with UseNumber: contract evidence may itself contain large IDs
	// or precise coordinates. A float64 round trip would invalidate true evidence.
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&contractMap); err != nil {
		return nil, err
	}
	if err = requiredContractShape(contractMap); err != nil {
		return nil, err
	}
	delete(contractMap, "result_hash")
	payload := map[string]any{}
	for _, key := range keys {
		payload[key] = data[key]
	}
	actual, err := TypedSHA256(map[string]any{"payload": payload, "contract": contractMap})
	if err != nil || actual != c.ResultHash {
		return nil, fmt.Errorf("structured_result_contract_hash_mismatch")
	}
	if err = validateSemantics(&c, data); err != nil {
		return nil, err
	}
	return &c, nil
}

func hexSize(s string, bytes int) bool {
	decoded, err := hex.DecodeString(s)
	return err == nil && len(decoded) == bytes && s == strings.ToLower(s)
}

func sha256Text(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func validateSemantics(c *Contract, data map[string]any) error {
	s := c.SourceSnapshot
	if !hexSize(s.SourceSHA256, 32) || !hexSize(s.InputDigestSHA256, 32) || strings.TrimSpace(s.File) == "" || len(s.File) > 1024 || len(s.Versions) == 0 {
		return fmt.Errorf("structured_result_source_snapshot_invalid")
	}
	source, ok := data["source"].(map[string]any)
	if !ok || source["sha256"] != s.SourceSHA256 {
		return fmt.Errorf("structured_result_source_snapshot_mismatch")
	}
	file := source["file"]
	if file == nil {
		file = source["file_name"]
	}
	if file != s.File {
		return fmt.Errorf("structured_result_source_file_mismatch")
	}
	if s.KnowledgeID != "" && source["knowledge_id"] != s.KnowledgeID {
		return fmt.Errorf("structured_result_knowledge_identity_mismatch")
	}
	if sourceKB, exists := source["knowledge_base_id"]; exists && sourceKB != s.KnowledgeBaseID {
		return fmt.Errorf("structured_result_source_scope_mismatch")
	}
	if s.TableID != "" && data["table_id"] != s.TableID || s.DatasetID != "" && data["dataset_id"] != s.DatasetID {
		return fmt.Errorf("structured_result_source_identity_mismatch")
	}
	if context, ok := data["source_context"].(map[string]any); ok && s.KnowledgeBaseID != "" && context["knowledge_base_id"] != s.KnowledgeBaseID {
		return fmt.Errorf("structured_result_source_scope_mismatch")
	}
	for _, digest := range s.Versions {
		if !hexSize(digest, 32) {
			return fmt.Errorf("structured_result_source_version_invalid")
		}
	}
	if s.Kind == "pdf_table" {
		if s.TableID == "" {
			return fmt.Errorf("structured_result_table_identity_missing")
		}
		for _, key := range []string{"record_sha256", "bundle_sha256", "plan_sha256"} {
			if !hexSize(s.Versions[key], 32) {
				return fmt.Errorf("structured_result_pdf_version_missing")
			}
		}
		if provenance, ok := data["provenance"].(map[string]any); ok {
			for _, keys := range [][2]string{{"table_record_sha256", "record_sha256"}, {"bundle_sha256", "bundle_sha256"}, {"plan_sha256", "plan_sha256"}} {
				if value, exists := provenance[keys[0]]; exists && value != s.Versions[keys[1]] {
					return fmt.Errorf("structured_result_source_version_mismatch")
				}
			}
		}
	} else if s.Kind == "excel_observations" {
		if s.DatasetID == "" || !hexSize(s.Versions["manifest_sha256"], 32) {
			return fmt.Errorf("structured_result_dataset_version_missing")
		}
	} else {
		return fmt.Errorf("structured_result_source_kind_invalid")
	}
	for _, key := range []string{"record_sha256", "bundle_sha256", "plan_sha256"} {
		if value, exists := data[key]; exists && s.Versions[key] != value {
			return fmt.Errorf("structured_result_source_version_mismatch")
		}
	}
	if c.Execution.Kind != "sql" && c.Execution.Kind != "raw_read" && c.Execution.Kind != "series_query" || c.Execution.Engine == "" || c.Execution.EngineVersion == "" || c.Execution.NumericSemantics == "" {
		return fmt.Errorf("structured_result_execution_invalid")
	}
	if c.Capabilities.CellLineageVerified || c.Execution.CellLineageVerified || c.Capabilities.AggregateMembershipVerified {
		return fmt.Errorf("structured_result_lineage_claim_unsupported")
	}
	if c.Capabilities.Level != "dependency_only" && c.Capabilities.Level != "source_cells" || (c.Execution.Kind == "sql" && (c.Capabilities.Level != "dependency_only" || c.Capabilities.SourceCellsAvailable)) {
		return fmt.Errorf("structured_result_evidence_capability_invalid")
	}
	if (c.Capabilities.Level == "source_cells") != c.Capabilities.SourceCellsAvailable {
		return fmt.Errorf("structured_result_evidence_capability_invalid")
	}
	if s.Scope != c.Execution.InputScope || (s.Scope != "loaded_inputs_before_user_sql" && s.Scope != "returned_source_rows") {
		return fmt.Errorf("structured_result_input_scope_invalid")
	}
	if c.Execution.Kind == "sql" {
		sql, ok := data["sql"].(string)
		if !ok || !hexSize(c.Execution.SQLSHA256, 32) {
			return fmt.Errorf("structured_result_sql_missing")
		}
		sum := sha256Text(sql)
		if sum != c.Execution.SQLSHA256 {
			return fmt.Errorf("structured_result_sql_hash_mismatch")
		}
	}
	names, err := columnNames(data)
	if err != nil || len(names) != len(c.Columns) {
		return fmt.Errorf("structured_result_column_shape_invalid")
	}
	rows, ok := data["rows"].([]any)
	if !ok || len(rows) > 50000 {
		return fmt.Errorf("structured_result_rows_invalid_or_over_budget")
	}
	for _, raw := range rows {
		if c.Execution.Kind == "sql" {
			if row, ok := raw.([]any); !ok || len(row) != len(names) {
				return fmt.Errorf("structured_result_row_shape_invalid")
			}
		} else {
			row, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("structured_result_row_shape_invalid")
			}
			if c.Execution.Kind == "raw_read" {
				row, ok = row["values"].(map[string]any)
				if !ok {
					return fmt.Errorf("structured_result_row_shape_invalid")
				}
			}
			if len(row) != len(names) {
				return fmt.Errorf("structured_result_row_shape_invalid")
			}
			for _, name := range names {
				if _, ok := row[name]; !ok {
					return fmt.Errorf("structured_result_row_shape_invalid")
				}
			}
		}
	}
	for i, column := range c.Columns {
		if column.Index != i || column.Name != names[i] || column.DisplayName == "" || len(column.DisplayName) > 512 || column.LogicalType == "" {
			return fmt.Errorf("structured_result_column_identity_invalid")
		}
		switch column.Dependency.Kind {
		case "source_value", "source_cardinality", "constant_label", "source_metadata", "mixed_constant_values", "unknown":
		default:
			return fmt.Errorf("structured_result_dependency_invalid")
		}
		if column.Dependency.Kind == "constant_label" && (len(column.Dependency.SourceColumns) > 0 || column.Dependency.SourceRowDependency || column.Dependency.TechnicalMetadata) {
			return fmt.Errorf("structured_result_literal_dependency_invalid")
		}
		if c.Capabilities.ColumnDependenciesVerified && column.Dependency.Kind == "unknown" {
			return fmt.Errorf("structured_result_dependency_claim_invalid")
		}
		u := column.Unit
		if u.BindingScope != "result_column" || len(u.Symbol) > 128 {
			return fmt.Errorf("structured_result_unit_scope_invalid")
		}
		switch u.Status {
		case "unknown", "mixed":
			if u.Symbol != "" || u.EvidenceLevel != "none" {
				return fmt.Errorf("structured_result_unknown_unit_claim_invalid")
			}
		case "declared":
			if u.Symbol == "" || len(u.Evidence) == 0 || (u.EvidenceLevel != "reviewed_plan_declaration" && u.EvidenceLevel != "imported_series_metadata") {
				return fmt.Errorf("structured_result_unit_declaration_invalid")
			}
		case "derived":
			if u.Symbol == "" || u.EvidenceLevel != "deterministic_expression" || u.Expression == "" {
				return fmt.Errorf("structured_result_unit_derivation_invalid")
			}
		default:
			return fmt.Errorf("structured_result_unit_status_invalid")
		}
		if column.Dependency.Kind == "constant_label" && u.Status != "unknown" {
			return fmt.Errorf("structured_result_literal_unit_promotion_invalid")
		}
		for _, e := range u.Evidence {
			if e["source_sha256"] != s.SourceSHA256 || e["original_annotation_verified"] != false {
				return fmt.Errorf("structured_result_unit_evidence_binding_invalid")
			}
			if s.Kind == "pdf_table" && (e["table_id"] != s.TableID || e["plan_sha256"] != s.Versions["plan_sha256"]) {
				return fmt.Errorf("structured_result_unit_plan_binding_invalid")
			}
		}
	}
	if dependencies, ok := data["sql_column_dependencies"].([]any); ok {
		if len(dependencies) != len(c.Columns) || data["sql_source_dependency_verified"] != c.Capabilities.ColumnDependenciesVerified {
			return fmt.Errorf("structured_result_dependency_contract_mismatch")
		}
		for i, raw := range dependencies {
			d, ok := raw.(map[string]any)
			if !ok || d["column"] != c.Columns[i].Name || d["kind"] != c.Columns[i].Dependency.Kind {
				return fmt.Errorf("structured_result_dependency_contract_mismatch")
			}
		}
	}
	return nil
}

func requiredContractShape(contract map[string]any) error {
	invalid := func() error { return fmt.Errorf("structured_result_required_metadata_missing_or_invalid") }
	bools := func(value any, names ...string) bool {
		m, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, name := range names {
			if _, ok := m[name].(bool); !ok {
				return false
			}
		}
		return true
	}
	if !bools(contract["execution"], "cell_lineage_verified") || !bools(contract["evidence_capabilities"], "column_dependencies_verified", "cell_lineage_verified", "source_cells_available", "aggregate_membership_verified") {
		return invalid()
	}
	columns, ok := contract["column_metadata"].([]any)
	if !ok {
		return invalid()
	}
	for _, raw := range columns {
		column, ok := raw.(map[string]any)
		if !ok {
			return invalid()
		}
		if _, ok := column["index"].(json.Number); !ok {
			return invalid()
		}
		dependency, ok := column["dependency"].(map[string]any)
		if !ok || !bools(dependency, "source_row_dependency", "technical_metadata") {
			return invalid()
		}
		if _, ok := dependency["source_columns"].([]any); !ok {
			return invalid()
		}
		unit, ok := column["unit"].(map[string]any)
		if !ok {
			return invalid()
		}
		if _, ok := unit["evidence"].([]any); !ok {
			return invalid()
		}
	}
	return nil
}

func columnNames(data map[string]any) ([]string, error) {
	if cols, ok := data["columns"].([]any); ok {
		names := make([]string, len(cols))
		for i, raw := range cols {
			switch v := raw.(type) {
			case string:
				names[i] = v
			case map[string]any:
				names[i], _ = v["name"].(string)
			default:
				return nil, fmt.Errorf("invalid column")
			}
			if names[i] == "" {
				return nil, fmt.Errorf("invalid column")
			}
		}
		return names, nil
	}
	rows, ok := data["rows"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid rows")
	}
	if len(rows) == 0 {
		return []string{}, nil
	}
	first, ok := rows[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid observation rows")
	}
	names := make([]string, 0, len(first))
	for name := range first {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
