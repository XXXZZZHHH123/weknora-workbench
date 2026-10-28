package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// admissionRepo reports every knowledge row as processing unless its status
// was set explicitly, which is all the gate's reconcile step reads.
type admissionRepo struct {
	interfaces.KnowledgeRepository
	status map[string]string
}

func (r *admissionRepo) GetKnowledgeBatchByIDOnly(_ context.Context, ids []string) ([]*types.Knowledge, error) {
	rows := make([]*types.Knowledge, 0, len(ids))
	for _, id := range ids {
		status, ok := r.status[id]
		if !ok {
			status = types.ParseStatusProcessing
		}
		if status == "" {
			continue // deleted row
		}
		rows = append(rows, &types.Knowledge{ID: id, ParseStatus: status})
	}
	return rows, nil
}

func newAdmissionService(t *testing.T, global, perUser int) (*knowledgeService, *admissionRepo) {
	t.Helper()
	t.Setenv("WEKNORA_PARSE_MAX_CONCURRENT_DOCUMENTS", fmt.Sprint(global))
	t.Setenv("WEKNORA_PARSE_MAX_CONCURRENT_PER_USER", fmt.Sprint(perUser))
	stubParseResources(t, parseResourceSample{MemAvailableMB: -1, Load1: -1})
	_, rdb := newSlotReleaseRedis(t)
	repo := &admissionRepo{status: map[string]string{}}
	return &knowledgeService{repo: repo, redisClient: rdb}, repo
}

func admit(svc *knowledgeService, user, knowledgeID string) error {
	return svc.admitDocumentParse(context.Background(), types.DocumentProcessPayload{
		TenantID:    1,
		KnowledgeID: knowledgeID,
		Initiator:   types.TaskInitiator{UserID: user},
	})
}

func TestParseAdmissionCapsEachUserAtTwo(t *testing.T) {
	svc, _ := newAdmissionService(t, 10, 2)

	require.NoError(t, admit(svc, "alice", "a1"))
	require.NoError(t, admit(svc, "alice", "a2"))
	require.ErrorIs(t, admit(svc, "alice", "a3"), types.ErrParseDeferred)

	// Redelivery of an admitted document keeps its slot.
	require.NoError(t, admit(svc, "alice", "a1"))
}

func TestParseAdmissionCapsGlobalConcurrency(t *testing.T) {
	svc, _ := newAdmissionService(t, 10, 2)

	for u := 0; u < 10; u++ {
		require.NoError(t, admit(svc, fmt.Sprintf("u%d", u), fmt.Sprintf("u%d-1", u)))
	}
	require.ErrorIs(t, admit(svc, "u10", "u10-1"), types.ErrParseDeferred)
}

func TestParseAdmissionUsesIdleSlotsButYieldsToWaitingUsers(t *testing.T) {
	svc, repo := newAdmissionService(t, 10, 2)

	for u := 0; u < 8; u++ {
		require.NoError(t, admit(svc, fmt.Sprintf("u%d", u), fmt.Sprintf("u%d-1", u)))
	}
	// Nobody else is waiting, so an idle slot goes to a second document.
	require.NoError(t, admit(svc, "u0", "u0-2"))

	// The last slot is taken by u9 below; u8 and u10 then wait with nothing
	// running and u1 must not jump ahead of them once slots free up.
	require.NoError(t, admit(svc, "u9", "u9-1"))
	require.ErrorIs(t, admit(svc, "u8", "u8-1"), types.ErrParseDeferred)
	require.ErrorIs(t, admit(svc, "u10", "u10-1"), types.ErrParseDeferred)

	repo.status["u0-2"] = types.ParseStatusCompleted
	require.ErrorIs(t, admit(svc, "u1", "u1-2"), types.ErrParseDeferred)
	require.NoError(t, admit(svc, "u8", "u8-1"))
}

func TestParseAdmissionLeavesFreedSlotForPoorerWaiter(t *testing.T) {
	svc, repo := newAdmissionService(t, 2, 2)

	require.NoError(t, admit(svc, "alice", "a1"))
	require.NoError(t, admit(svc, "bob", "b1"))
	require.ErrorIs(t, admit(svc, "carol", "c1"), types.ErrParseDeferred)
	require.ErrorIs(t, admit(svc, "alice", "a2"), types.ErrParseDeferred)

	// Bob's document finishes; Alice already holds a slot while Carol has
	// none, so the freed slot is reserved for Carol.
	repo.status["b1"] = types.ParseStatusCompleted
	require.ErrorIs(t, admit(svc, "alice", "a2"), types.ErrParseDeferred)
	require.NoError(t, admit(svc, "carol", "c1"))
}

func TestParseAdmissionReleasesTerminalAndDeletedDocuments(t *testing.T) {
	svc, repo := newAdmissionService(t, 2, 2)

	require.NoError(t, admit(svc, "alice", "a1"))
	require.NoError(t, admit(svc, "alice", "a2"))
	require.ErrorIs(t, admit(svc, "alice", "a3"), types.ErrParseDeferred)

	repo.status["a1"] = types.ParseStatusFailed
	repo.status["a2"] = "" // row deleted
	require.NoError(t, admit(svc, "alice", "a3"))
	require.NoError(t, admit(svc, "alice", "a4"))
}

func TestParseAdmissionFailsOpenWithoutRedis(t *testing.T) {
	svc := &knowledgeService{}
	for i := 0; i < 5; i++ {
		require.NoError(t, admit(svc, "alice", fmt.Sprintf("a%d", i)))
	}
}

func stubParseResources(t *testing.T, sample parseResourceSample) {
	t.Helper()
	original := sampleParseResources
	sampleParseResources = func() parseResourceSample { return sample }
	t.Cleanup(func() { sampleParseResources = original })
}

func TestParseGlobalLimitFollowsResources(t *testing.T) {
	t.Setenv("WEKNORA_PARSE_MIN_CONCURRENT_DOCUMENTS", "2")
	t.Setenv("WEKNORA_PARSE_MAX_CONCURRENT_DOCUMENTS", "30")
	idle := parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 1, CPUs: 8}

	cases := []struct {
		name   string
		active int
		sample parseResourceSample
		want   int
		reason string
	}{
		// (17 GB - 3 GB reserve) / 1.5 GB = 9 more documents.
		{"memory bounds an idle host", 0, idle, 9, "memory"},
		{"memory room adds to running documents", 4, idle, 13, "memory"},
		{"just-admitted documents reserve their memory", 4,
			parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 1, CPUs: 8, Warming: 4}, 9, "memory"},
		{"plenty of memory hits the ceiling", 0,
			parseResourceSample{MemAvailableMB: 64 * 1024, Load1: 1, CPUs: 8}, 30, "ceiling"},
		{"busy CPU holds at the running count", 6,
			parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 9, CPUs: 8}, 6, "cpu"},
		{"VLM throttling holds at the running count", 5,
			parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 1, CPUs: 8, VLMThrottled: 12}, 5, "vlm_rate_limited"},
		{"the floor keeps documents moving", 0,
			parseResourceSample{MemAvailableMB: 2 * 1024, Load1: 20, CPUs: 8, VLMThrottled: 50}, 2, "memory+floor"},
		{"unknown signals are not pressure", 3,
			parseResourceSample{MemAvailableMB: -1, Load1: -1}, 30, "ceiling"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := parseGlobalLimit(tc.active, tc.sample)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestParseAdmissionReservesMemoryForJustAdmittedDocuments(t *testing.T) {
	svc, _ := newAdmissionService(t, 30, 2)
	// Room for three documents; the first three have not reached their
	// docreader peak yet, so a fourth must wait even though MemAvailable has
	// not dropped.
	stubParseResources(t, parseResourceSample{MemAvailableMB: 3072 + 3*1536, Load1: 1, CPUs: 8})
	require.NoError(t, admit(svc, "alice", "a1"))
	require.NoError(t, admit(svc, "bob", "b1"))
	require.NoError(t, admit(svc, "carol", "c1"))
	require.ErrorIs(t, admit(svc, "dave", "d1"), types.ErrParseDeferred)
}

func TestParseAdmissionWaitsWhileResourcesAreTight(t *testing.T) {
	svc, repo := newAdmissionService(t, 30, 2)
	stubParseResources(t, parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 1, CPUs: 8})

	require.NoError(t, admit(svc, "alice", "a1"))
	require.NoError(t, admit(svc, "bob", "b1"))
	require.NoError(t, admit(svc, "carol", "c1"))

	stubParseResources(t, parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 12, CPUs: 8})
	require.ErrorIs(t, admit(svc, "dave", "d1"), types.ErrParseDeferred)
	// A running document keeps its slot when the host gets busy.
	require.NoError(t, admit(svc, "alice", "a1"))

	// Still busy: a finished document is not replaced until the load drops.
	repo.status["a1"] = types.ParseStatusCompleted
	require.ErrorIs(t, admit(svc, "dave", "d1"), types.ErrParseDeferred)

	stubParseResources(t, parseResourceSample{MemAvailableMB: 17 * 1024, Load1: 2, CPUs: 8})
	require.NoError(t, admit(svc, "dave", "d1"))
}
