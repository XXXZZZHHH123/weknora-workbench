package types

import (
	"os"
	"strconv"
	"strings"
)

// VerifiedPublicationEnabled controls the checked public answer projection.
func VerifiedPublicationEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_VERIFIED_PUBLICATION")))
	return err != nil || enabled
}
