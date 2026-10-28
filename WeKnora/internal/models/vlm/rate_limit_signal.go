package vlm

import (
	"sync"
	"time"
)

// rateLimitSignal remembers when VLM providers last pushed back with 429/503
// so the parse admission gate can stop starting new documents while the image
// stage is already saturating the provider quota. It is process-local: the
// gate runs in the same app process that issues the VLM calls.
var rateLimitSignal = &rateLimitLog{}

type rateLimitLog struct {
	mu     sync.Mutex
	events []time.Time
}

const rateLimitLogHorizon = 10 * time.Minute

func (l *rateLimitLog) note(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(at)
	l.events = append(l.events, at)
}

func (l *rateLimitLog) since(cutoff time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(time.Now())
	n := 0
	for i := len(l.events) - 1; i >= 0 && !l.events[i].Before(cutoff); i-- {
		n++
	}
	return n
}

func (l *rateLimitLog) prune(now time.Time) {
	horizon := now.Add(-rateLimitLogHorizon)
	i := 0
	for i < len(l.events) && l.events[i].Before(horizon) {
		i++
	}
	l.events = l.events[i:]
}

// RecentRateLimits returns how many VLM calls were rate limited within window.
func RecentRateLimits(window time.Duration) int {
	return rateLimitSignal.since(time.Now().Add(-window))
}
