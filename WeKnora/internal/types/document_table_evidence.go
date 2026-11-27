package types

import (
	"os"
	"strconv"
	"strings"
)

// DocumentTableEvidenceEnabled controls model-led table planning and evidence.
func DocumentTableEvidenceEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE")))
	return err != nil || enabled
}
