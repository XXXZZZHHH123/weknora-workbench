package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type documentImageCatalogFake struct {
	interfaces.ResourceCatalog
	resource *types.StoredResource
	owner    string
	refs     []string
}

func (c *documentImageCatalogFake) Resolve(context.Context, string) (*types.StoredResource, error) {
	return c.resource, nil
}

func (c *documentImageCatalogFake) ListReferencesByOwner(_ context.Context, ownerType string, owners ...string) ([]string, error) {
	if ownerType == types.ResourceOwnerKnowledge && len(owners) == 1 && owners[0] == c.owner {
		return c.refs, nil
	}
	return nil, nil
}

func TestDocumentImageReadRequiresExactLiveDocumentBinding(t *testing.T) {
	for _, variant := range []string{"valid", "other-document", "other-tenant", "deleted", "foreign-kb", "unregistered"} {
		t.Run(variant, func(t *testing.T) {
			ref := "resource://AbCdEfGhIjKlMnOpQrStUv"
			files := &imageReaderFiles{body: []byte("original picture bytes")}
			catalog := &documentImageCatalogFake{owner: "chosen-document", refs: []string{ref},
				resource: &types.StoredResource{TenantID: 7, State: types.ResourceStateActive}}
			kb := &types.KnowledgeBase{ID: "chosen-kb", TenantID: 7}
			knowledge := &types.Knowledge{ID: "chosen-document", KnowledgeBaseID: kb.ID, TenantID: 7}
			switch variant {
			case "other-document":
				catalog.owner = "another-document"
			case "other-tenant":
				catalog.resource.TenantID = 8
			case "deleted":
				catalog.resource.State = types.ResourceStateDeleted
			case "foreign-kb":
				kb.TenantID = 8
			case "unregistered":
				ref = "https://example.invalid/private.png"
			}
			s := &knowledgeBaseService{repo: &imageReaderKBRepo{kb: kb}, tenantRepo: &attrsTenantRepo{},
				fileSvc: files, resourceCatalog: catalog}
			data, err := s.ReadDocumentImage(t.Context(), knowledge, ref)
			if variant == "valid" {
				require.NoError(t, err)
				require.Equal(t, files.body, data)
				require.Equal(t, []string{ref}, files.reads)
			} else {
				require.Error(t, err)
				require.Empty(t, data)
				require.Empty(t, files.reads)
			}
		})
	}
}
