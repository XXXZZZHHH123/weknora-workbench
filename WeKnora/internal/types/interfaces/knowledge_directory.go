package interfaces

import "context"

// KnowledgeFolderDeletionRepository reserves a directory while its documents
// are removed through the normal knowledge cleanup pipeline. The reservation
// survives a failed request so the same authorized deletion can be retried.
type KnowledgeFolderDeletionRepository interface {
	BeginKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path string) error
	ClaimKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path string) (string, error)
	ReleaseKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path, lease string) error
	ListKnowledgeFolderDeletionBatch(ctx context.Context, tenantID uint64, kbID, path string, limit int) ([]string, error)
	FinishKnowledgeFolderDeletion(ctx context.Context, tenantID uint64, kbID, path, lease string) error
}
