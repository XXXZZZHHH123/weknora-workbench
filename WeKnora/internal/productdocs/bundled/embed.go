// Package bundled contains only upstream runtime help pages. The website build and assets remain excluded.
package bundled

import "embed"

// Pages contains the versioned upstream Markdown used by the product documentation tool.
//
//go:embed 01-getting-started/*.md 02-architecture/*.md 03-features/*.md 04-api/*.md 05-clients/*.md 06-development/*.md 07-releases/*.md
var Pages embed.FS
