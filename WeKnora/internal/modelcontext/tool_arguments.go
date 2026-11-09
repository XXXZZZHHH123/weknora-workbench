package modelcontext

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ToolArgumentError is protocol feedback, never a substitute for source or
// authorization validation. Path is a JSON Pointer into the submitted call.
type ToolArgumentError struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Expected string `json:"expected,omitempty"`
	Message  string `json:"message"`
}

func (e *ToolArgumentError) Error() string {
	return fmt.Sprintf("invalid tool arguments at %s: %s", e.Path, e.Message)
}

type argumentSchema struct {
	Type                 json.RawMessage            `json:"type"`
	Properties           map[string]json.RawMessage `json:"properties"`
	AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	Items                json.RawMessage            `json:"items"`
	Required             []string                   `json:"required"`
	Pattern              string                     `json:"pattern"`
	ExactDecimalString   bool                       `json:"x-weknora-exact-decimal-string"`
}

var exactArgumentDecimalRE = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

// NormalizeToolArguments accepts only schema-declared, lossless transport
// differences: one complete JSON string around a container, and exact decimal
// number tokens for fields whose schema explicitly opts into decimal strings.
// It never repairs JSON, invents required fields, parses prose, or resolves IDs.
// Callers must use the actual registered schema and decode handles afterwards.
func NormalizeToolArguments(raw string, schema json.RawMessage) (string, error) {
	value, err := strictArgumentJSON(raw, "")
	if err != nil {
		return raw, err
	}
	normalized, changed, err := normalizeArgumentValue(value, schema, "", 0)
	if err != nil || !changed {
		return raw, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return raw, err
	}
	return string(encoded), nil
}

func normalizeArgumentValue(value any, rawSchema json.RawMessage, path string, depth int) (any, bool, error) {
	if depth > 64 {
		return nil, false, &ToolArgumentError{Code: "nesting_limit", Path: path, Message: "argument nesting exceeds the protocol limit"}
	}
	var schema argumentSchema
	if len(rawSchema) == 0 || json.Unmarshal(rawSchema, &schema) != nil {
		return value, false, nil // Opaque/untyped schemas do not authorize coercion.
	}
	var expected string
	if json.Unmarshal(schema.Type, &expected) != nil {
		return value, false, nil // Union types are ambiguous; the tool validates them.
	}
	changed := false
	if wrapped, ok := value.(string); ok && (expected == "object" || expected == "array") {
		decoded, err := strictArgumentJSON(wrapped, path)
		if err != nil {
			return nil, false, &ToolArgumentError{Code: "invalid_container_json", Path: path, Expected: expected, Message: "send one complete JSON " + expected + "; the encoded value is malformed, truncated, duplicated, or contains trailing text"}
		}
		value, changed = decoded, true
	}
	if token, ok := value.(json.Number); ok && expected == "string" && schema.ExactDecimalString && exactArgumentDecimalRE.MatchString(token.String()) {
		return token.String(), true, nil // Preserve the token, including trailing zeros.
	}
	typeMismatch := func() (any, bool, error) {
		return nil, false, &ToolArgumentError{Code: "argument_type", Path: path, Expected: expected, Message: "expected " + expected + "; submit the complete field using its declared type"}
	}
	switch expected {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return typeMismatch()
		}
		for _, required := range schema.Required {
			if _, exists := object[required]; !exists {
				return nil, false, &ToolArgumentError{Code: "required_argument", Path: argumentPointer(path, required), Message: "required field is missing; provide it from the current task and actual evidence"}
			}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childSchema, declared := schema.Properties[key]
			if !declared {
				childSchema = schema.AdditionalProperties
			}
			child, childChanged, err := normalizeArgumentValue(object[key], childSchema, argumentPointer(path, key), depth+1)
			if err != nil {
				return nil, false, err
			}
			object[key], changed = child, changed || childChanged
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return typeMismatch()
		}
		for i, item := range array {
			child, childChanged, err := normalizeArgumentValue(item, schema.Items, argumentPointer(path, strconv.Itoa(i)), depth+1)
			if err != nil {
				return nil, false, err
			}
			array[i], changed = child, changed || childChanged
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return typeMismatch()
		}
		if schema.Pattern != "" {
			// This is deliberately not a complete JSON Schema validator. An
			// unsupported regex dialect remains the owning tool's contract.
			if pattern, err := regexp.Compile(schema.Pattern); err == nil && !pattern.MatchString(text) {
				return nil, false, &ToolArgumentError{Code: "argument_pattern", Path: path, Expected: schema.Pattern,
					Message: "string must match the declared pattern " + schema.Pattern + "; correct this field before retrying"}
			}
		}
	}
	return value, changed, nil
}

func argumentPointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// strictArgumentJSON rejects duplicate keys before encoding/json can silently
// choose one identity or value. json.Number preserves provider numeric lexemes.
func strictArgumentJSON(raw, path string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var read func(string, int) (any, error)
	read = func(current string, depth int) (any, error) {
		if depth > 64 {
			return nil, &ToolArgumentError{Code: "nesting_limit", Path: current, Message: "argument nesting exceeds the protocol limit"}
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, &ToolArgumentError{Code: "invalid_json", Path: current, Message: "JSON must be complete and contain exactly one value"}
		}
		switch token {
		case json.Delim('{'):
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return nil, &ToolArgumentError{Code: "invalid_json", Path: current, Message: "invalid object key"}
				}
				childPath := argumentPointer(current, key)
				if _, exists := object[key]; exists {
					return nil, &ToolArgumentError{Code: "duplicate_argument", Path: childPath, Message: "duplicate object keys are ambiguous; submit each field once"}
				}
				child, err := read(childPath, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
				return nil, &ToolArgumentError{Code: "invalid_json", Path: current, Message: "object is incomplete"}
			}
			return object, nil
		case json.Delim('['):
			array := []any{}
			for decoder.More() {
				child, err := read(argumentPointer(current, strconv.Itoa(len(array))), depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
				return nil, &ToolArgumentError{Code: "invalid_json", Path: current, Message: "array is incomplete"}
			}
			return array, nil
		default:
			return token, nil
		}
	}
	value, err := read(path, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, &ToolArgumentError{Code: "invalid_json", Path: path, Message: "JSON must contain exactly one value without trailing text"}
	}
	return value, nil
}

// decodeJSONNumbers is used by handle codecs that only rewrite strings. Those
// rewrites must not round unrelated numeric arguments through float64.
func decodeJSONNumbers(raw string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

// Only our registered built-in schemas call this projection. The annotation
// remains in Tool.Parameters for execution; providers see standard schema
// vocabulary. Literal defaults/examples/enums and foreign schemas stay opaque.
func stripPrivateArgumentSchemaAnnotations(raw json.RawMessage, depth int) (json.RawMessage, bool) {
	if depth > 64 {
		return raw, false
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return raw, false
	}
	_, changed := schema["x-weknora-exact-decimal-string"]
	delete(schema, "x-weknora-exact-decimal-string")
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		var children map[string]json.RawMessage
		if json.Unmarshal(schema[key], &children) != nil {
			continue
		}
		childrenChanged := false
		for name, child := range children {
			normalized, childChanged := stripPrivateArgumentSchemaAnnotations(child, depth+1)
			children[name], childrenChanged = normalized, childrenChanged || childChanged
		}
		if childrenChanged {
			schema[key], _ = json.Marshal(children)
			changed = true
		}
	}
	for _, key := range []string{"items", "additionalItems", "additionalProperties", "propertyNames", "contains", "not", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems"} {
		if normalized, childChanged := stripPrivateArgumentSchemaAnnotations(schema[key], depth+1); childChanged {
			schema[key], changed = normalized, true
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"} {
		var children []json.RawMessage
		if json.Unmarshal(schema[key], &children) != nil {
			continue
		}
		childrenChanged := false
		for index, child := range children {
			normalized, childChanged := stripPrivateArgumentSchemaAnnotations(child, depth+1)
			children[index], childrenChanged = normalized, childrenChanged || childChanged
		}
		if childrenChanged {
			schema[key], _ = json.Marshal(children)
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return raw, false
	}
	return encoded, true
}
