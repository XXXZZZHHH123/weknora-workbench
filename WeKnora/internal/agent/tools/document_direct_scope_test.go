package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func directScopeReadFixture() (*ReadDocumentTool, *RequestedDocumentConstraint, *readDocChunkRepo) {
	tool, repo := newReadDocumentFixture(3)
	for i, text := range []string{"<!-- PDF page 1 -->\nFirst source page.", "<!-- PDF page 2 -->\nSecond source page.", "<!-- PDF page 3 -->\nThird source page."} {
		repo.ordered[i].Content = text
	}
	state := NewDocumentScopeState("Use only physical pages 1 and 2.")
	return tool.WithDocumentConstraint(state), state, repo
}

func TestReadDocumentDirectTargetVerifiesInitialSourceInSameCall(t *testing.T) {
	for _, args := range []string{
		`{"id":"doc-1","page":2}`,
		`{"id":"doc-1","pages":[1,2]}`,
		`{"id":"chunk-1"}`,
		`{"id":"chunk-1","page":2}`,
		`{"id":"doc-1","query":"Second source"}`,
	} {
		t.Run(args, func(t *testing.T) {
			tool, state, _ := directScopeReadFixture()
			result, err := tool.Execute(t.Context(), json.RawMessage(args))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			require.Contains(t, result.Output, "Second source page.")
			require.Equal(t, []string{"doc-1"}, state.selectedIDs())
			selected, err := state.selection("doc-1")
			require.NoError(t, err)
			require.Equal(t, "Engine Manual", selected.Title)
			require.Nil(t, selected.UserPages, "a read hint is never promoted to a claimed user restriction")
			require.Equal(t, 0, selected.Page)
		})
	}
}

func TestReadDocumentDirectTargetRejectsUnknownRetiredAndOutOfScopeBeforeBinding(t *testing.T) {
	for _, mutation := range []string{"unknown", "resource", "retired", "document_scope", "knowledge_base", "tenant", "metadata"} {
		t.Run(mutation, func(t *testing.T) {
			tool, state, repo := directScopeReadFixture()
			knowledge := tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"]
			id := "doc-1"
			switch mutation {
			case "unknown":
				id = "invented-document"
			case "resource":
				id = "resource://abcdefghijklmnopqrstuv"
			case "retired":
				knowledge.ParseStatus = "archived"
			case "document_scope":
				tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", TenantID: 7, KnowledgeIDs: []string{"another-document"}}}
			case "knowledge_base":
				knowledge.KnowledgeBaseID = "foreign-library"
			case "tenant":
				knowledge.TenantID = 99
			case "metadata":
				knowledge.Title, knowledge.FileName = "", ""
			}
			args, err := json.Marshal(map[string]any{"id": id, "page": 1})
			require.NoError(t, err)
			result, _ := tool.Execute(t.Context(), args)
			require.False(t, result.Success)
			require.Empty(t, state.selectedIDs())
			require.Empty(t, repo.requests, "rejected source identity must not start a body read")
		})
	}
}

func TestReadDocumentDirectTargetNeverOverridesExplicitPlanOrUserPages(t *testing.T) {
	tool, state, repo := directScopeReadFixture()
	service := tool.knowledgeService.(*readDocKnowledgeService)
	service.docs["doc-2"] = &types.Knowledge{ID: "doc-2", Title: "Another version", FileName: "other.pdf", KnowledgeBaseID: "kb-1", TenantID: 7}
	plan := DocumentScopePlan{Reason: "The model declares the actual user source and page restriction", Sources: []DocumentSourceSelection{{KnowledgeID: "doc-1", Title: "Engine Manual", UserPages: &DocumentPageConstraint{Pages: []int{1, 2}, UserQuote: "Use only physical pages 1 and 2."}}}}
	_, err := state.bind(t.Context(), plan, service, tool.searchTargets)
	require.NoError(t, err)
	for _, args := range []string{`{"id":"doc-2","page":1}`, `{"id":"doc-1","page":3}`, `{"id":"chunk-2"}`} {
		repo.requests = nil
		result, err := tool.Execute(t.Context(), json.RawMessage(args))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Equal(t, []string{"doc-1"}, state.selectedIDs())
	}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":2}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	selected, err := state.selection("doc-1")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, selected.UserPages.Pages)
}

func TestReadDocumentDirectTargetDoesNotFallbackAfterInvalidExplicitSourceScope(t *testing.T) {
	for _, selection := range []DocumentSourceSelection{
		{KnowledgeID: "doc-1", Title: "A fabricated source title"},
		{KnowledgeID: "doc-1", Title: "Engine Manual", Date: &DocumentDateEvidence{Date: "2031-04-18", SourceField: "title", SourceText: "20310418"}},
		{KnowledgeID: "doc-1", Title: "Engine Manual", UserPages: &DocumentPageConstraint{Pages: []int{1}, UserQuote: "A fabricated user constraint"}},
	} {
		tool, state, repo := directScopeReadFixture()
		args, err := json.Marshal(ReadDocumentInput{ID: "doc-1", Page: 1, SourceScope: &DocumentScopePlan{Reason: "model source plan", Sources: []DocumentSourceSelection{selection}}})
		require.NoError(t, err)
		result, err := tool.Execute(t.Context(), args)
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Empty(t, state.selectedIDs())
		require.Empty(t, repo.requests)
	}
}

func TestReadDocumentDirectTargetKeepsBoundIdentityMetadataCurrent(t *testing.T) {
	tool, state, repo := directScopeReadFixture()
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	tool.knowledgeService.(*readDocKnowledgeService).docs["doc-1"].Title = "Different current source identity"
	repo.requests = nil
	result, err = tool.Execute(t.Context(), json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "身份元数据")
	require.Empty(t, repo.requests)
	selected, err := state.selection("doc-1")
	require.NoError(t, err)
	require.Equal(t, "Engine Manual", selected.Title)
}

func TestReadDocumentDirectTargetInvalidNavigationDoesNotBind(t *testing.T) {
	for _, args := range []string{`{"id":"doc-1","pages":[1,1]}`, `{"id":"doc-1","page":1,"pages":[2]}`} {
		tool, state, repo := directScopeReadFixture()
		result, err := tool.Execute(t.Context(), json.RawMessage(args))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Empty(t, state.selectedIDs())
		require.Empty(t, repo.requests)
	}
}

func TestReadDocumentDirectTargetInitialBindingIsAtomic(t *testing.T) {
	state := NewDocumentScopeState()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, id := range []string{"first-document", "second-document"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := state.selectionForRead(&types.Knowledge{ID: id, Title: "Actual source"})
			results <- err == nil
		}(id)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for ok := range results {
		if ok {
			accepted++
		}
	}
	require.Equal(t, 1, accepted, "parallel reads cannot race to overwrite the selected source")
	require.Len(t, state.selectedIDs(), 1)
	_, err := state.selectionForRead(&types.Knowledge{ID: state.selectedIDs()[0], Title: "Actual source"})
	require.NoError(t, err)
}

// Ensure a caller tenant different from the source owner is not confused with
// a real cross-tenant source mismatch: shared targets carry the source owner.
func TestReadDocumentDirectTargetUsesAuthorizedSourceTenantForSharedKnowledge(t *testing.T) {
	tool, _, _ := directScopeReadFixture()
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 99, UserID: "shared-reader", Role: types.TenantRoleViewer})
	result, err := tool.Execute(ctx, json.RawMessage(`{"id":"doc-1","page":1}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
}
