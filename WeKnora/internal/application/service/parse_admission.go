package service

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/vlm"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/redis/go-redis/v9"
)

const (
	// The global limit is derived from live resources on every decision;
	// these only bound it. The floor keeps documents flowing even when the
	// signals say the host is busy, the ceiling protects worker pools.
	defaultParseMinConcurrentDocuments = 2
	defaultParseMaxConcurrentDocuments = 30
	defaultParseMaxConcurrentPerUser   = 2
	// Measured on weekly-report PPTX: docreader peaks around 1.1 GB above its
	// idle footprint per document while converting vector images.
	defaultParseMemoryPerDocumentMB = 1536
	defaultParseMemoryReserveMB     = 3072
	// Load average per CPU at which no new document is started.
	defaultParseCPULoadPerCore = 1.0
	// VLM 429/503 responses within parseVLMThrottleWindow at which new
	// documents wait: more parallel documents would only queue more images
	// behind the same provider quota.
	defaultParseVLMThrottleLimit = 10
	parseVLMThrottleWindow       = 2 * time.Minute
	parseWarmupWindow            = 5 * time.Minute

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

// parseResourceSample is a snapshot of the signals the global limit follows.
// Host values come from /proc, which inside a container without its own
// limits reports the whole machine; that is the budget docreader and the app
// actually share.
type parseResourceSample struct {
	MemAvailableMB int64   // -1 when unknown
	Load1          float64 // -1 when unknown
	CPUs           int
	VLMThrottled   int // rate-limited VLM calls within parseVLMThrottleWindow
	// Warming counts documents admitted within parseWarmupWindow: their
	// docreader peak has probably not shown up in MemAvailable yet, so the
	// memory budget reserves room for them up front.
	Warming int
}

// sampleParseResources is swapped in tests.
var sampleParseResources = func() parseResourceSample {
	return parseResourceSample{
		MemAvailableMB: readMemAvailableMB(),
		Load1:          readLoad1(),
		CPUs:           runtime.NumCPU(),
		VLMThrottled:   vlm.RecentRateLimits(parseVLMThrottleWindow),
	}
}

// parseGlobalLimit decides how many documents may be in flight right now.
// Each signal either leaves room for more documents or holds the limit at the
// current number in flight; it never evicts a running document. Unknown
// signals are ignored rather than treated as pressure.
func parseGlobalLimit(active int, r parseResourceSample) (limit int, reason string) {
	floor := positiveEnvInt("WEKNORA_PARSE_MIN_CONCURRENT_DOCUMENTS", defaultParseMinConcurrentDocuments)
	ceiling := positiveEnvInt("WEKNORA_PARSE_MAX_CONCURRENT_DOCUMENTS", defaultParseMaxConcurrentDocuments)
	limit, reason = ceiling, "ceiling"

	hold := func(why string) {
		if active < limit {
			limit, reason = active, why
		}
	}
	if r.MemAvailableMB >= 0 {
		perDoc := int64(positiveEnvInt("WEKNORA_PARSE_MEMORY_PER_DOCUMENT_MB", defaultParseMemoryPerDocumentMB))
		reserve := int64(positiveEnvInt("WEKNORA_PARSE_MEMORY_RESERVE_MB", defaultParseMemoryReserveMB))
		room := (r.MemAvailableMB-reserve)/perDoc - int64(r.Warming)
		if room < 0 {
			room = 0
		}
		if fit := active + int(room); fit < limit {
			limit, reason = fit, "memory"
		}
	}
	if r.Load1 >= 0 && r.CPUs > 0 {
		threshold := defaultParseCPULoadPerCore
		if v, err := strconv.ParseFloat(os.Getenv("WEKNORA_PARSE_CPU_LOAD_PER_CORE"), 64); err == nil && v > 0 {
			threshold = v
		}
		if r.Load1/float64(r.CPUs) >= threshold {
			hold("cpu")
		}
	}
	if r.VLMThrottled >= positiveEnvInt("WEKNORA_PARSE_VLM_THROTTLE_LIMIT", defaultParseVLMThrottleLimit) {
		hold("vlm_rate_limited")
	}
	if limit < floor {
		limit, reason = floor, reason+"+floor"
	}
	return limit, reason
}

func readMemAvailableMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "MemAvailable:" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb / 1024
			}
		}
	}
	return -1
}

func readLoad1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return -1
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return -1
	}
	return v
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
	active, warming := s.reconcileParseAdmission(ctx, payload.KnowledgeID)

	sample := sampleParseResources()
	sample.Warming = warming
	global, reason := parseGlobalLimit(active, sample)
	perUser := positiveEnvInt("WEKNORA_PARSE_MAX_CONCURRENT_PER_USER", defaultParseMaxConcurrentPerUser)
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
		logger.Infof(ctx, "Parse admission deferred: knowledge=%s owner=%s active=%d limit=%d (%s) per_user=%d",
			payload.KnowledgeID, parseAdmissionOwner(payload), active, global, reason, perUser)
		return types.ErrParseDeferred
	}
	return nil
}

// reconcileParseAdmission releases slots whose documents are no longer in
// flight. Terminal status is written from many places (pipeline, cancel,
// delete, housekeeping), so instead of hooking each of them the gate checks
// the rows themselves before every decision. It returns how many documents
// still hold a slot and how many of those were admitted within
// parseWarmupWindow (a slot's score is its lease deadline, refreshed on
// redelivery, so admission time is deadline minus lease).
func (s *knowledgeService) reconcileParseAdmission(ctx context.Context, current string) (active, warming int) {
	slots, err := s.redisClient.ZRangeWithScores(ctx, parseAdmissionActiveKey, 0, -1).Result()
	if err != nil || len(slots) == 0 {
		return 0, 0
	}
	warmCutoff := float64(time.Now().Add(parseAdmissionLease - parseWarmupWindow).Unix())
	ids := make([]string, 0, len(slots))
	warm := make(map[string]bool)
	for _, z := range slots {
		id, _ := z.Member.(string)
		ids = append(ids, id)
		if z.Score > warmCutoff {
			warm[id] = true
		}
	}
	countWarm := func(keep func(string) bool) int {
		n := 0
		for id := range warm {
			if keep(id) {
				n++
			}
		}
		return n
	}
	rows, err := s.repo.GetKnowledgeBatchByIDOnly(ctx, ids)
	if err != nil {
		logger.Warnf(ctx, "Parse admission reconcile skipped: %v", err)
		return len(ids), len(warm)
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
		return len(ids), len(warm)
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
		return len(ids), len(warm)
	}
	return len(ids) - len(stale), countWarm(func(id string) bool { return id == current || live[id] })
}

func parseStatusHoldsSlot(status string) bool {
	switch status {
	case types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFinalizing:
		return true
	}
	return false
}
