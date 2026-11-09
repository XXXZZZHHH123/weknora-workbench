package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// The remote scheduler owns queue/execution clocks. Native workers only perform
// short polls. A queue wait never consumes a document worker's process budget.
func (s *knowledgeService) deferRemotePDF(ctx context.Context, payload types.DocumentProcessPayload, pending *docparser.RemotePDFPendingError) error {
	if s.task == nil {
		return fmt.Errorf("remote PDF polling requires the durable task scheduler")
	}
	ref := pending.Job
	ref.Poll++
	payload.RemotePDFJob = &ref
	if payload.FilePath != "" {
		payload.FileURL = ""
	} // reuse the saved original instead of downloading its URL on every poll
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("pdf-poll:%s:%d:%s:%d", payload.KnowledgeID, payload.Attempt, ref.ID, ref.Poll)
	task := asynq.NewTask(types.TypeDocumentProcess, raw,
		documentProcessTaskOptions(s.config, asynq.ProcessIn(10*time.Second), asynq.TaskID(id))...)
	if _, err = s.task.Enqueue(task); err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
		return fmt.Errorf("schedule remote PDF poll: %w", err)
	}
	label := "正在识别PDF"
	if pending.Status == "queued" {
		label = "等待解析资源"
	}
	s.beginStage(ctx, payload.KnowledgeID, types.StageDocReader, types.JSONMap{"parser_engine": docparser.RemotePDFEngineName, "parser_job_id": ref.ID, "queue_status": pending.Status, "message": label})
	return nil
}
