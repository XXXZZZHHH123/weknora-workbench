package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type wikiSlugLockContextKey struct{}

var localWikiSlugLocks = struct {
	sync.Mutex
	locks map[string]*localWikiSlugLock
}{locks: make(map[string]*localWikiSlugLock)}

type localWikiSlugLock struct {
	gate chan struct{}
	refs int
}

func withWikiSlugLock(ctx context.Context, redisClient *redis.Client, kbID, slug string, fn func(context.Context) error) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lockKey := wikiSlugLockPrefix + kbID + ":" + slug
	if held, _ := ctx.Value(wikiSlugLockContextKey{}).(string); held == lockKey {
		return true, fn(ctx)
	}
	if kbID == "" || slug == "" {
		return false, errors.New("wiki slug lock: knowledge base and slug are required")
	}

	if redisClient == nil {
		unlock, err := acquireLocalWikiSlugLock(ctx, lockKey)
		if err != nil {
			return false, err
		}
		defer unlock()
		return true, fn(context.WithValue(ctx, wikiSlugLockContextKey{}, lockKey))
	}

	deadline := time.Now().Add(wikiSlugLockWait)
	token := uuid.NewString()
	for {
		ok, err := redisClient.SetNX(ctx, lockKey, token, wikiSlugLockTTL).Result()
		if err != nil {
			logger.Warnf(ctx, "wiki slug lock: SetNX failed for %s: %v", slug, err)
			return false, err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(wikiSlugLockPoll):
		}
	}
	lockCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErr := make(chan error, 1)
	stopRenew := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		renewWikiSlugLease(lockCtx, redisClient, lockKey, token, stopRenew, cancel, renewErr)
	}()
	defer redisClient.Eval(context.Background(), `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`, []string{lockKey}, token)
	callbackErr := fn(context.WithValue(lockCtx, wikiSlugLockContextKey{}, lockKey))
	close(stopRenew)
	<-renewDone
	select {
	case leaseErr := <-renewErr:
		return true, errors.Join(callbackErr, leaseErr)
	default:
		return true, callbackErr
	}
}

func renewWikiSlugLease(ctx context.Context, redisClient *redis.Client, lockKey, token string, stop <-chan struct{}, cancel context.CancelFunc, errOut chan<- error) {
	ticker := time.NewTicker(wikiSlugLockTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, err := redisClient.Eval(renewCtx, `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`, []string{lockKey}, token, wikiSlugLockTTL.Milliseconds()).Int()
			renewCancel()
			if err == nil && result == 1 {
				continue
			}
			if err == nil {
				err = errors.New("wiki slug lock lease was lost")
			}
			select {
			case errOut <- err:
			default:
			}
			cancel()
			return
		}
	}
}

func acquireLocalWikiSlugLock(ctx context.Context, key string) (func(), error) {
	localWikiSlugLocks.Lock()
	entry := localWikiSlugLocks.locks[key]
	if entry == nil {
		entry = &localWikiSlugLock{gate: make(chan struct{}, 1)}
		localWikiSlugLocks.locks[key] = entry
	}
	entry.refs++
	localWikiSlugLocks.Unlock()

	select {
	case entry.gate <- struct{}{}:
		return func() {
			<-entry.gate
			localWikiSlugLocks.Lock()
			entry.refs--
			if entry.refs == 0 {
				delete(localWikiSlugLocks.locks, key)
			}
			localWikiSlugLocks.Unlock()
		}, nil
	case <-ctx.Done():
		localWikiSlugLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(localWikiSlugLocks.locks, key)
		}
		localWikiSlugLocks.Unlock()
		return nil, ctx.Err()
	}
}
