package imagegen

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderError is an allowlisted public diagnostic, not a wrapped provider
// error. It deliberately contains no response body, message, URL or credential.
// Code/Param are protocol identifiers; invalid/free-text values are omitted.
type ProviderError struct {
	StatusCode int
	Code       string
	Param      string
}

func publicProviderToken(value string) string {
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == '[' || c == ']') {
			return ""
		}
	}
	return value
}

// Diagnostic revalidates exported fields so even manually constructed errors
// cannot turn a safe diagnostic into a free-text/URL channel.
func (e *ProviderError) Diagnostic() map[string]any {
	out := map[string]any{"kind": "provider_http"}
	if e == nil {
		return out
	}
	if e.StatusCode >= 100 && e.StatusCode <= 599 {
		out["http_status"] = e.StatusCode
	}
	if value := publicProviderToken(e.Code); value != "" {
		out["code"] = value
	}
	if value := publicProviderToken(e.Param); value != "" {
		out["param"] = value
	}
	return out
}
func (e *ProviderError) Error() string {
	out := e.Diagnostic()
	message := "image generation provider rejected the request"
	if status, ok := out["http_status"]; ok {
		message = fmt.Sprintf("image generation provider returned HTTP %d", status)
	}
	var fields []string
	if code, ok := out["code"].(string); ok {
		fields = append(fields, "code="+code)
	}
	if param, ok := out["param"].(string); ok {
		fields = append(fields, "param="+param)
	}
	if len(fields) > 0 {
		message += " (" + strings.Join(fields, ", ") + ")"
	}
	return message
}
func providerError(status int, body string, secrets []string) *ProviderError {
	result := &ProviderError{StatusCode: status}
	var response struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(body), &response) != nil {
		return result
	}
	token := func(raw json.RawMessage) string {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return ""
		}
		value = publicProviderToken(value)
		for _, secret := range secrets {
			if secret != "" && strings.Contains(strings.ToLower(value), strings.ToLower(secret)) {
				return ""
			}
		}
		return value
	}
	result.Code = token(response.Error["code"])
	result.Param = token(response.Error["param"])
	return result
}
