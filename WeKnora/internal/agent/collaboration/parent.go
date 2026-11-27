package collaboration

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type parentOwnerKey struct{}

type parentHeartbeatKey struct {
	scope Scope
	owner string
}

type parentHeartbeat struct {
	ctx   context.Context
	lease atomic.Pointer[parentHeartbeatLease]
}

type parentHeartbeatLease struct {
	durable time.Time
	expires time.Time // retains Go's monotonic clock reading
}

// The durable lease fences ownership across instances. A heartbeat owned by
// this store can also prove the very same lease is still live after a wall
// clock jump, until it renews the durable lease against the new wall time.
func (s *Store) parentLeaseCurrent(g *Group) bool {
	if g.ParentOwner == "" {
		return false
	}
	entry, ok := s.parentHeartbeats.Load(parentHeartbeatKey{g.Scope, g.ParentOwner})
	if ok {
		heartbeat := entry.(*parentHeartbeat)
		lease := heartbeat.lease.Load()
		if heartbeat.ctx.Err() == nil && lease != nil && g.ParentLease.Equal(lease.durable) {
			return time.Now().Before(lease.expires)
		}
	}
	return time.Now().Before(g.ParentLease)
}

func (s *Store) parentRenewalDue(g *Group, now time.Time, interval time.Duration) bool {
	if g.ParentLease.Sub(now) < interval {
		return true
	}
	entry, ok := s.parentHeartbeats.Load(parentHeartbeatKey{g.Scope, g.ParentOwner})
	if !ok {
		return false
	}
	lease := entry.(*parentHeartbeat).lease.Load()
	return lease != nil && g.ParentLease.Equal(lease.durable) && lease.expires.Sub(now) < interval
}

// AcquireParent attaches a new engine to an unfinished task after its prior
// engine lease expires. Completed groups never revive. Automatic recovery uses
// wakeParent and an owned safe checkpoint; this path also lets a new user turn
// take over unfinished work without replaying old tool calls.
func (r *Runtime) AcquireParent(ctx context.Context, requested Scope) (*Binding, error) {
	messageID := requested.ID
	var row Record
	err := r.Store.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND session_id = ? AND status = ?",
			requested.TenantID,
			requested.UserID,
			requested.SessionID,
			"active").
		Order("updated_at DESC").
		First(&row).
		Error
	recovered := false
	if err == nil {
		recovered = row.ID != requested.ID
		requested.ID = row.ID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	} else {
		members, err := r.previousChildren(ctx, &requested)
		if err != nil {
			return nil, err
		}
		if err := r.Store.ensure(ctx, requested, members); err != nil {
			return nil, err
		}
	}
	owner := uuid.NewString()
	err = r.Store.Change(ctx, requested, func(g *Group) error {
		if g.Status != "active" {
			return ErrClosed
		}
		if r.Store.parentLeaseCurrent(g) {
			return errors.New("the parent task is already running")
		}
		g.ParentWait = nil
		if g.ParentMessageID != messageID {
			g.ParentEvidence = nil
			g.ParentEvidenceCurrent = false
		}
		g.ParentMessageID = messageID
		g.ParentOwner = owner
		g.ParentLease = time.Now().Add(r.Lease)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Binding{Runtime: r, Scope: requested, ParentOwner: owner, Recovered: recovered}, nil
}

// Context adds the parent ownership fence to store operations.
func (b *Binding) Context(ctx context.Context) context.Context {
	if b.ParentOwner != "" {
		return context.WithValue(ctx, parentOwnerKey{}, b.ParentOwner)
	}
	return ctx
}

// Start renews the parent lease until the returned cleanup function is called.
func (b *Binding) Start(ctx context.Context) (context.Context, func()) {
	if b.ParentOwner == "" {
		return ctx, func() {}
	}
	if ctx.Err() != nil {
		return ctx, func() {}
	}
	// Starting or sharing a heartbeat never revives an expired/replaced owner.
	// Check synchronously, before any preparation, model request or renewal.
	g, err := b.Runtime.Store.Read(ctx, b.Scope)
	if err != nil {
		return canceledLeaseContext(ctx, err)
	}
	closing := b.Closing && g.ParentClosePending && g.Status == "cancelling"
	if (g.Status != "active" && !closing) || g.ParentOwner != b.ParentOwner || !b.Runtime.Store.parentLeaseCurrent(g) {
		return canceledLeaseContext(ctx, ErrConflict)
	}
	if b.Runtime.Store.parentRenewalDue(g, time.Now(), b.Runtime.Lease*2/3) {
		// A valid owner may have less time left than the first heartbeat tick.
		// Renew before returning execution authority, while the store fence
		// rechecks the current owner/lease and the callback rechecks its state.
		lease := g.ParentLease
		renewed := false
		err = b.Runtime.Store.Change(b.Context(ctx), b.Scope, func(current *Group) error {
			renewed = false
			closing := b.Closing && current.ParentClosePending && current.Status == "cancelling"
			now := time.Now()
			if (current.Status != "active" && !closing) || current.ParentOwner != b.ParentOwner || !b.Runtime.Store.parentLeaseCurrent(current) {
				return ErrConflict
			}
			if b.Runtime.Store.parentRenewalDue(current, now, b.Runtime.Lease*2/3) {
				current.ParentLease = now.Add(b.Runtime.Lease)
				touchDeadline(current, now)
				renewed = true
			}
			lease = current.ParentLease
			return nil
		})
		if err != nil {
			return canceledLeaseContext(ctx, err)
		}
		g.ParentLease = lease
		if entry, ok := b.Runtime.Store.parentHeartbeats.Load(parentHeartbeatKey{b.Scope, b.ParentOwner}); ok && renewed {
			heartbeat := entry.(*parentHeartbeat)
			heartbeat.lease.Store(&parentHeartbeatLease{durable: lease, expires: lease})
		}
	}
	id := b.leaseIdentity()
	if existing, ok := ctx.Value(parentLeaseKey{}).(parentLeaseIdentity); ok && existing == id {
		return b.Context(ctx), func() {}
	}
	if scope, ok := ctx.Value(executionKey{}).(*executionScope); ok {
		return scope.start(ctx, b, g.ParentLease)
	}
	return b.startHeartbeat(ctx, g.ParentLease)
}

func (b *Binding) startHeartbeat(ctx context.Context, initialLease time.Time) (context.Context, func()) {
	ctx = context.WithValue(ctx, parentLeaseKey{}, b.leaseIdentity())
	ctx, cancel := context.WithCancel(b.Context(ctx))
	done := make(chan struct{})
	store := b.Runtime.Store
	key := parentHeartbeatKey{b.Scope, b.ParentOwner}
	initialTime := time.Now()
	lease := initialTime.Add(initialLease.Sub(initialTime))
	heartbeat := &parentHeartbeat{ctx: ctx}
	heartbeat.lease.Store(&parentHeartbeatLease{durable: initialLease, expires: lease})
	store.parentHeartbeats.Store(key, heartbeat)
	go func() {
		defer close(done)
		defer store.parentHeartbeats.CompareAndDelete(key, heartbeat)
		tick := time.NewTicker(b.Runtime.heartbeatInterval())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				completed := false
				var renewed time.Time
				err := store.Change(ctx, b.Scope, func(g *Group) error {
					renewed = time.Time{}
					completed = g.Status == "completed"
					if completed {
						return nil
					}
					closing := b.Closing && g.ParentClosePending && g.Status == "cancelling"
					if g.Status != "active" && !closing {
						return ErrClosed
					}
					now := time.Now()
					if store.parentRenewalDue(g, now, b.Runtime.Lease*2/3) {
						g.ParentLease = now.Add(b.Runtime.Lease)
						renewed = g.ParentLease
						touchDeadline(g, now)
					}
					return nil
				})
				if err == nil && completed {
					return
				} // normal completion must not cancel final event delivery
				if err != nil && !isFenceError(err) && time.Now().Before(lease) && ctx.Err() == nil {
					continue
				}
				if err != nil {
					cancel()
					return
				}
				if !renewed.IsZero() {
					lease = renewed
					heartbeat.lease.Store(&parentHeartbeatLease{durable: renewed, expires: renewed})
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

// CheckParent rejects expired, replaced or closed parent executions.
func (r *Runtime) CheckParent(ctx context.Context, scope Scope, owner string) error {
	g, err := r.Store.Read(ctx, scope)
	if err != nil {
		return err
	}
	if !r.ParentCanExecute(g, owner) {
		return ErrConflict
	}
	return nil
}

// ParentCanExecute checks an already loaded parent lease without another read.
func (r *Runtime) ParentCanExecute(g *Group, owner string) bool {
	return g.Status == "active" && g.ParentOwner == owner && r.Store.parentLeaseCurrent(g)
}

// A new human turn may continue a completed child's conversation. Copy only
// idle instance identities into a fresh group: old events, approvals, leases and
// mail remain fenced in the old group and can never wake the new parent.
func (r *Runtime) previousChildren(ctx context.Context, scope *Scope) ([]*Member, error) {
	var row Record
	err := r.Store.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND session_id = ? AND id <> ?",
			scope.TenantID, scope.UserID, scope.SessionID, scope.ID).
		Order("updated_at DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The initial active-group lookup may have raced another caller's ensure.
	if row.Status == "active" {
		scope.ID = row.ID
		return nil, nil
	}
	// A cancelled/uncertain newer group is a barrier. Never reach backwards
	// past it to revive an older copy of the same child session.
	if row.Status != "completed" {
		return nil, nil
	}
	previous := *scope
	previous.ID = row.ID
	g, err := r.Store.Read(ctx, previous)
	if err != nil {
		return nil, err
	}
	// Concurrent new human turns must not clone this child session into two
	// independently runnable groups. The completed predecessor is a durable
	// CAS admission record; a crash before ensure is repaired by the next caller.
	nextID := scope.ID
	if err := r.Store.Change(ctx, previous, func(g *Group) error {
		if g.Status != "completed" {
			return ErrConflict
		}
		if g.SuccessorID == "" {
			g.SuccessorID = nextID
		}
		nextID = g.SuccessorID
		return nil
	}); err != nil {
		return nil, err
	}
	scope.ID = nextID
	var members []*Member
	for _, original := range g.Members {
		last := current(original)
		if last == nil || active(last) {
			continue
		}
		m, run := *original, *last
		m.Inherited, m.Mail = true, nil
		run.Inherited = true
		run.Owner, run.Lease, run.PromptID, run.Prompt = "", time.Time{}, "", nil
		m.Runs = []*Run{&run}
		members = append(members, &m)
	}
	return members, nil
}
