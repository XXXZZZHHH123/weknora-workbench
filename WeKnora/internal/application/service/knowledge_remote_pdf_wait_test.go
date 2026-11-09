package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"testing"
)

type pdfPollQueue struct {
	tasks []*asynq.Task
	err   error
}

func (q *pdfPollQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return &asynq.TaskInfo{}, q.err
}
func TestDeferRemotePDFPersistsLeaseAttemptAndSettings(t *testing.T) {
	q := &pdfPollQueue{}
	service := knowledgeService{task: q}
	policy := types.DefaultPDFProcessingSettings()
	payload := types.DocumentProcessPayload{TenantID: 77, KnowledgeID: "doc", KnowledgeBaseID: "kb", Attempt: 8, PDFProcessing: &policy}
	pending := &docparser.RemotePDFPendingError{Job: types.RemotePDFJobReference{ID: "remote1", LeaseID: "lease1", Poll: 4}, Status: "queued"}
	require.NoError(t, service.deferRemotePDF(context.Background(), payload, pending))
	require.Len(t, q.tasks, 1)
	var got types.DocumentProcessPayload
	require.NoError(t, json.Unmarshal(q.tasks[0].Payload(), &got))
	require.Equal(t, payload.TenantID, got.TenantID)
	require.Equal(t, 8, got.Attempt)
	require.Equal(t, "lease1", got.RemotePDFJob.LeaseID)
	require.Equal(t, 5, got.RemotePDFJob.Poll)
	require.Equal(t, &policy, got.PDFProcessing)
	q.err = asynq.ErrTaskIDConflict
	require.NoError(t, service.deferRemotePDF(context.Background(), payload, pending))
	q.err = errors.New("unavailable")
	require.Error(t, service.deferRemotePDF(context.Background(), payload, pending))
}
