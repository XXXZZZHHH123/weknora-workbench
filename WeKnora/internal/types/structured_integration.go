package types

import "regexp"

// The installer records a unique ownership marker on its managed agent and
// MCP service. Names are editable and are never used to identify integrations.
var managedStructuredIntegration = regexp.MustCompile(`\[managed-by:cotton-structured-data/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\]`)

func IsManagedStructuredIntegration(description string) bool {
	return managedStructuredIntegration.MatchString(description)
}
