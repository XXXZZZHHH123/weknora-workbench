package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/redis/go-redis/v9"
)

const (
	defaultParseMaxConcurrentDocuments = 10
	defaultParseMaxConcurrentPerUser   = 2

	parseAdmissionActiveKey = "parse:admission:active"
	parseAdmissionOwnersKey = "parse:admission:owners"
	parseAdmissionWaitKey   = "parse:admission:waiting"

	// parseAdmissionLease bounds how long a slot survives if its document
	// never reaches a terminal status (crashed worker, lost task). Normal
	// release happens through reconcile once the row is completed/failed.
	parseAdmissionLease = 3 * time.Hour
	// parseAdmissionWaitWindow is how long a deferred owner still counts as
	// waiting after its last retry; it must exceed ParseDeferredRetryDelay.
	parseAdmissionWaitWindow = 3 * types.ParseDeferredRetryDelay
)

// parseAdmissionScript atomically decides whether a document may start.
//
// A slot is held for the whole document lifecycle (docreader, embedding and
// the image fan-out), not just this task, because the image stage dominates
// wall-clock time and model quota. Idle capacity is always used up to the
// per-owner cap, but a free slot is left for any waiting owner who holds
// fewer slots than the requester, so under load each user gets one document
// at a time before anyone gets a second.
//
// KEYS: active zset (knowledge id -> lease deadline), owners hash
// (knowledge id -> owner), waiting zset (owner -> last deferral time).
// ARGV: now, knowledge id, owner, global cap, per-owner cap, lease deadline,
// waiting cutoff. Returns 1 when admitted, 0 when deferred.
var parseAdmissionScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local id, owner = ARGV[2], ARGV[3]
local global, per = tonumber(ARGV[4]), tonumber(ARGV[5])

for _, m in ipairs(redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now)) do
  redis.call('HDEL', KEYS[2], m)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', ARGV[7])

if redis.call('ZSCORE', KEYS[1], id) then
  redis.call('ZADD', KEYS[1], ARGV[6], id)
  return 1
end

local counts, total = {}, 0
for _, m in ipairs(redis.call('ZRANGE', KEYS[1], 0, -1)) do
  local o = redis.call('HGET', KEYS[2], m) or ''
  counts[o] = (counts[o] or 0) + 1
  total = total + 1
end

local mine = counts[owner] or 0
local waiting = redis.call('ZRANGE', KEYS[3], 0, -1)

local ok = total < global and mine < per
if ok then
  local poorer = 0
  for _, o in ipairs(waiting) do
    if o ~= owner and (counts[o] or 0) < mine then poorer = poorer + 1 end
  end
  if global - total <= poorer then ok = false end
end

if ok then
  redis.call('ZADD', KEYS[1], ARGV[6], id)
  redis.call('HSET', KEYS[2], id, owner)
  redis.call('ZREM', KEYS[3], owner)
  return 1
end
redis.call('ZADD', KEYS[3], now, owner)
return 0
`)

// parseAdmissionLimits reads the gate limits from the environment on every
// call so operators can tune them per deployment without a schema change.
func parseAdmissionLimits() (global, perUser int) {
	return positiveEnvInt("WEKNORA_PARSE_MAX_CONCURRENT_DOCUMENTS", defaultParseMaxConcurrentDocuments),
		positiveEnvInt("WEKNORA_PARSE_MAX_CONCURRENT_PER_USER", defaultParseMaxConcurrentPerUser)
}

func positiveEnvInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

// parseAdmissionOwner identifies whose quota a document counts against: the
// user who started the parse, falling back to the tenant for system work.
func parseAdmissionOwner(payload types.DocumentProcessPayload) string {
	if payload.Initiator.UserID != "" {
		return "user:" + payload.Initiator.UserID
	}
	return fmt.Sprintf("tenant:%d", payload.TenantID)
}

// admitDocumentParse returns nil when the document may proceed and
// types.ErrParseDeferred when it must wait. Without Redis (Lite mode) or on a Redis
// error the gate fails open: throttling is a fairness aid, not a correctness
// guarantee, and must never strand a document.
func (s *knowledgeService) admitDocumentParse(ctx context.Context, payload types.DocumentProcessPayload) error {
	if s.redisClient == nil {
		return nil
	}
	s.reconcileParseAdmission(ctx, payload.KnowledgeID)

	global, perUser := parseAdmissionLimits()
	now := time.Now()
	admitted, err := parseAdmissionScript.Run(ctx, s.redisClient,
		[]string{parseAdmissionActiveKey, parseAdmissionOwnersKey, parseAdmissionWaitKey},
		now.Unix(), payload.KnowledgeID, parseAdmissionOwner(payload), global, perUser,
		now.Add(parseAdmissionLease).Unix(), now.Add(-parseAdmissionWaitWindow).Unix(),
	).Int()
	if err != nil {
		logger.Warnf(ctx, "Parse admission unavailable, proceeding without throttling: %v", err)
		return nil
	}
	if admitted == 0 {
		logger.Infof(ctx, "Parse admission deferred: knowledge=%s owner=%s limits=%d/%d",
			payload.KnowledgeID, parseAdmissionOwner(payload), global, perUser)
		return types.ErrParseDeferred
	}
	return nil
}

// reconcileParseAdmission releases slots whose documents are no longer in
// flight. Terminal status is written from many places (pipeline, cancel,
// delete, housekeeping), so instead of hooking each of them the gate checks
// the rows themselves before every decision.
func (s *knowledgeService) reconcileParseAdmission(ctx context.Context, current string) {
	ids, err := s.redisClient.ZRange(ctx, parseAdmissionActiveKey, 0, -1).Result()
	if err != nil || len(ids) == 0 {
		return
	}
	rows, err := s.repo.GetKnowledgeBatchByIDOnly(ctx, ids)
	if err != nil {
		logger.Warnf(ctx, "Parse admission reconcile skipped: %v", err)
		return
	}
	live := make(map[string]bool, len(rows))
	for _, k := range rows {
		if k != nil && parseStatusHoldsSlot(k.ParseStatus) {
			live[k.ID] = true
		}
	}
	var stale []string
	for _, id := range ids {
		if id != current && !live[id] {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return
	}
	members := make([]any, len(stale))
	for i, id := range stale {
		members[i] = id
	}
	pipe := s.redisClient.TxPipeline()
	pipe.ZRem(ctx, parseAdmissionActiveKey, members...)
	pipe.HDel(ctx, parseAdmissionOwnersKey, stale...)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.Warnf(ctx, "Parse admission release failed: %v", err)
	}
}

func parseStatusHoldsSlot(status string) bool {
	switch status {
	case types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFinalizing:
		return true
	}
	return false
}
