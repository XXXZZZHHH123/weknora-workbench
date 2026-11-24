package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// WebSearcher runs one search against a provider.
type WebSearcher interface {
	Search(ctx context.Context, req types.WebSearchRequest) ([]*types.WebSearchResult, error)
}

// WebFetcher reads one page.
type WebFetcher interface {
	Fetch(ctx context.Context, pageURL string) (*types.WebPage, error)
}

<<<<<<< d7d91fcc398687cff8a13d498d2ff91c00e8d109
// WebSearchFilterCapabilities is an optional, local capability lookup for the
// effective workspace provider. It must not issue a search or another external
// request. An error means capability is unknown, not unsupported.
type WebSearchFilterCapabilities interface {
	SupportsFilters(ctx context.Context, providerID string, config *types.WebSearchConfig) (bool, error)
}

// WebSearchService defines the interface for web search services
=======
// WebClient is one stored provider bound to its registered clients and the tenant's policy.
type WebClient interface {
	WebSearcher
	// Fetch uses the provider's own reader when it has one, and the local reader otherwise.
	WebFetcher
	Capabilities() types.WebSearchCapabilities
}

// WebSearchService resolves the web provider for a request.
>>>>>>> 4020d25e0dde206f1058eada8a73aab808dace21
type WebSearchService interface {
	// Resolve loads providerID for the tenant in ctx once. An empty providerID is an error;
	// callers select the tenant's default WebSearchProviderEntity before calling Resolve.
	Resolve(ctx context.Context, providerID string) (WebClient, error)
}

// WebResultCompressor condenses web results through a temporary, hidden knowledge base.
type WebResultCompressor interface {
	// Compress updates state in place so later calls in the session reuse the knowledge base.
	Compress(ctx context.Context, state *types.WebSearchCompressionState, questions []string,
		results []*types.WebSearchResult, cfg *types.WebSearchConfig) ([]*types.WebSearchResult, error)
}
