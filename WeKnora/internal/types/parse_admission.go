package types

import (
	"errors"
	"time"
)

// ErrParseDeferred is returned by the document process handler when the
// parse admission gate has no slot for the document yet. Worker servers treat
// it as a non-failure (the retry budget is not consumed, no dead letter is
// written) and retry after ParseDeferredRetryDelay, so a waiting document
// simply stays queued.
var ErrParseDeferred = errors.New("document parse deferred: admission slots are full")

// ParseDeferredRetryDelay is how often a deferred document re-asks the gate.
const ParseDeferredRetryDelay = 10 * time.Second
