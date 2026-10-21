package handler

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/models/api"
)

// Classify transport/status information, never vendor error text or model names.
// A parameter rejection can have many causes; the UI must not call it a proven
// protocol mismatch. The original error remains available for troubleshooting.
func modelConnectionDiagnostic(err error, configuration bool) map[string]any {
	kind := "unknown"
	if configuration {
		kind = "configuration"
	}
	result := map[string]any{}
	var upstream *api.HTTPError
	var transport *api.TransportError
	if errors.As(err, &upstream) {
		result["http_status"] = upstream.StatusCode
		switch upstream.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			kind = "request_rejected"
		case http.StatusUnauthorized, http.StatusForbidden:
			kind = "access_denied"
		case http.StatusNotFound:
			kind = "not_found"
		case http.StatusTooManyRequests:
			kind = "rate_limited"
		default:
			if upstream.StatusCode >= 500 {
				kind = "upstream_unavailable"
			}
		}
	} else if errors.As(err, &transport) {
		kind = "connection"
	}
	result["kind"] = kind
	return result
}
