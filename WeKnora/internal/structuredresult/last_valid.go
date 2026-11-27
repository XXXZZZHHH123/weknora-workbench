package structuredresult

import (
	"os"
	"strings"
)

// LastValidEnabled mirrors the structured-data service's endpoint switch.
func LastValidEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WEKNORA_STRUCTURED_LAST_VALID"))) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}
