package collaboration

import (
	"context"
	"sync"
	"time"
)

type executionKey struct{}
type parentLeaseKey struct{}

type parentLeaseIdentity struct {
	runtime *Runtime
	scope   Scope
	owner   string
	closing bool
}

func (b *Binding) leaseIdentity() parentLeaseIdentity {
	return parentLeaseIdentity{b.Runtime, b.Scope, b.ParentOwner, b.Closing}
}

type executionLease struct {
	binding *Binding
	stop    func()
	detach  func() bool
}

type executionScope struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	closed bool
	leases map[parentLeaseIdentity]executionLease
}

// BeginExecution owns leases acquired later during engine creation. It neither
// acquires authority nor changes eligibility: attach still performs those checks
// at its original boundary. Its cleanup also covers failure before Execute/Resume.
func BeginExecution(ctx context.Context) (context.Context, func()) {
	if _, ok := ctx.Value(executionKey{}).(*executionScope); ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	scope := &executionScope{cancel: cancel, leases: make(map[parentLeaseIdentity]executionLease)}
	ctx = context.WithValue(ctx, executionKey{}, scope)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			scope.mu.Lock()
			scope.closed = true
			leases := scope.leases
			scope.leases = nil
			scope.mu.Unlock()
			for _, lease := range leases {
				// Detach before stopping normally, which also cancels the heartbeat
				// context. A closing outbox belongs to its recovery worker.
				lease.detach()
				if !lease.binding.Closing {
					lease.binding.End(false)
				}
				lease.stop()
			}
			cancel()
		})
	}
}

// StartExecutionLease immediately renews an already authorized parent when the
// caller owns an execution scope. Unmanaged engine factories must not start an
// orphan heartbeat; their Execute/Resume entry starts its own bounded lease.
func StartExecutionLease(ctx context.Context, b *Binding) error {
	if _, ok := ctx.Value(executionKey{}).(*executionScope); !ok {
		return nil
	}
	runCtx, _ := b.Start(ctx)
	return context.Cause(runCtx)
}

func (s *executionScope) start(ctx context.Context, b *Binding, lease time.Time) (context.Context, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return canceledLeaseContext(ctx, context.Canceled)
	}
	id := b.leaseIdentity()
	if _, ok := s.leases[id]; ok {
		return context.WithValue(b.Context(ctx), parentLeaseKey{}, id), func() {}
	}
	runCtx, stop := b.startHeartbeat(ctx, lease)
	s.leases[id] = executionLease{binding: b, stop: stop, detach: context.AfterFunc(runCtx, s.cancel)}
	return runCtx, func() {}
}

func canceledLeaseContext(ctx context.Context, reason error) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	cancel(reason)
	return ctx, func() {}
}
