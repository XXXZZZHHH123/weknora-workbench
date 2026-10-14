package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type searchScopeFailingKBService struct {
	tagTargetKnowledgeBaseService
	err error
}

func (s *searchScopeFailingKBService) GetKnowledgeBasesByIDsOnly(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tagTargetKnowledgeBaseService.GetKnowledgeBasesByIDsOnly(ctx, ids)
}

type searchScopeFailingKnowledgeService struct {
	tagTargetKnowledgeService
	err error
}

func (s *searchScopeFailingKnowledgeService) GetKnowledgeBatchWithSharedAccess(ctx context.Context, tenant uint64, ids []string) ([]*types.Knowledge, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tagTargetKnowledgeService.GetKnowledgeBatchWithSharedAccess(ctx, tenant, ids)
}

func TestBuildSearchTargetsRejectsUnknownDeletedAndUnavailableKnowledgeBases(t *testing.T) {
	for _, test := range []struct {
		name   string
		ids    []string
		mutate func(*sessionService)
	}{
		{"missing ID", []string{"old-kb"}, func(*sessionService) {}},
		{"missing ID beside readable KB", []string{"doc-kb", "old-kb"}, func(*sessionService) {}},
		{"deleted KB with residual data", []string{"doc-kb"}, func(s *sessionService) {
			s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs["doc-kb"].DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
		}},
		{"database lookup failed", []string{"doc-kb"}, func(s *sessionService) {
			s.knowledgeBaseService = &searchScopeFailingKBService{tagTargetKnowledgeBaseService: *s.knowledgeBaseService.(*tagTargetKnowledgeBaseService), err: errors.New("offline lookup failed")}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTagTargetSessionService()
			test.mutate(s)
			targets, err := s.buildSearchTargets(tagTargetContext(), 100, test.ids, nil, nil)
			require.Error(t, err, "a stale ID must not gain the caller's tenant or become a legacy source scope")
			require.Empty(t, targets)
		})
	}
}

func TestBuildSearchTargetsVerifiesDocumentParentAndOwner(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*sessionService)
	}{
		{"missing parent", func(s *sessionService) { delete(s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs, "doc-kb") }},
		{"deleted parent", func(s *sessionService) {
			s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs["doc-kb"].DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
		}},
		{"document tenant mismatches parent", func(s *sessionService) { s.knowledgeService.(*tagTargetKnowledgeService).knowledges[0].TenantID = 29 }},
		{"deleted document", func(s *sessionService) {
			s.knowledgeService.(*tagTargetKnowledgeService).knowledges[0].DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
		}},
		{"unknown document", func(s *sessionService) { s.knowledgeService.(*tagTargetKnowledgeService).knowledges = nil }},
		{"document batch failed", func(s *sessionService) {
			s.knowledgeService = &searchScopeFailingKnowledgeService{tagTargetKnowledgeService: *s.knowledgeService.(*tagTargetKnowledgeService), err: errors.New("offline document lookup failed")}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTagTargetSessionService()
			test.mutate(s)
			targets, err := s.buildSearchTargets(tagTargetContext(), 100, nil, []string{"doc-1"}, nil)
			require.Error(t, err)
			require.Empty(t, targets)
		})
	}
}

func TestBuildSearchTargetsSelectedEmptyScopeCannotFallBackToLegacy(t *testing.T) {
	for _, test := range []struct {
		name      string
		kbIDs     []string
		documents []string
		tags      []types.TagScope
		mutate    func(*sessionService)
	}{
		{"no readable KB", []string{"doc-kb"}, nil, nil, func(s *sessionService) {
			s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs["doc-kb"].TenantID = 29
		}},
		{"tag has no readable document", nil, nil, []types.TagScope{{KnowledgeBaseID: "doc-kb", TagIDs: []string{"nonexistent-tag"}}}, func(*sessionService) {}},
		{"empty tag intersection", []string{"doc-kb"}, []string{"doc-2"}, []types.TagScope{{KnowledgeBaseID: "doc-kb", TagIDs: []string{"tag-a"}}}, func(*sessionService) {}},
		{"invalid empty selection", []string{""}, nil, nil, func(*sessionService) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTagTargetSessionService()
			test.mutate(s)
			targets, err := s.buildSearchTargets(tagTargetContext(), 100, test.kbIDs, test.documents, test.tags)
			require.Error(t, err)
			require.Empty(t, targets)
		})
	}
	targets, err := (&sessionService{}).buildSearchTargets(tagTargetContext(), 100, nil, nil, nil)
	require.NoError(t, err, "only a genuinely unselected request keeps legacy compatibility")
	require.Empty(t, targets)
}

func TestBuildSearchTargetsExactGrantStillVerifiesPersistedKnowledgeBase(t *testing.T) {
	s := newTagTargetSessionService()
	kb := s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs["doc-kb"]
	kb.TenantID = 29
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 100, UserID: "caller", Role: types.TenantRoleViewer})
	grant := &access.KBAccess{KnowledgeBase: kb, Caller: types.CallerFromContext(ctx), EffectiveTenantID: 29, Permission: types.OrgRoleViewer}
	ctx = types.WithExecutionTenant(grant.Context(ctx), 77)
	targets, err := s.buildSearchTargets(ctx, 77, []string{"doc-kb"}, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, uint64(29), targets[0].TenantID)
	require.Equal(t, uint64(100), types.CallerFromContext(ctx).TenantID)
	delete(s.knowledgeBaseService.(*tagTargetKnowledgeBaseService).kbs, "doc-kb")
	targets, err = s.buildSearchTargets(ctx, 77, []string{"doc-kb"}, nil, nil)
	require.Error(t, err, "a previously granted but deleted KB cannot recreate its old namespace")
	require.Empty(t, targets)
}

func TestResolveKnowledgeBasesRejectedExplicitMentionCannotBecomeLegacyScope(t *testing.T) {
	for _, mention := range []struct {
		name  string
		kbIDs []string
		tags  []types.TagScope
	}{
		{"knowledge base", []string{"outside-agent"}, nil},
		{"tag", nil, []types.TagScope{{KnowledgeBaseID: "outside-agent", TagIDs: []string{"tag"}}}},
	} {
		t.Run(mention.name, func(t *testing.T) {
			s := &sessionService{}
			_, _, err := s.resolveKnowledgeBases(tagTargetContext(), &types.QARequest{
				Session:          &types.Session{TenantID: 100},
				CustomAgent:      &types.CustomAgent{TenantID: 29, Config: types.CustomAgentConfig{KBSelectionMode: "selected", KnowledgeBases: []string{"allowed-agent-kb"}}},
				KnowledgeBaseIDs: mention.kbIDs, TagScopes: mention.tags,
			})
			require.Error(t, err, "blocked explicit mentions cannot become an unselected request")
		})
	}
}
