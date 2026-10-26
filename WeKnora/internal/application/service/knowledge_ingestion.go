package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Tencent/WeKnora/internal/ingestion"
	"github.com/Tencent/WeKnora/internal/types"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ingestionMetadataKey = "_knowledge_ingestion"

type knowledgeIngestionState struct {
	Attempt        string          `json:"attempt"`
	SourceHash     string          `json:"source_hash"`
	Status         string          `json:"status"`
	Stage          string          `json:"stage"`
	AttentionCount int             `json:"attention_count,omitempty"`
	Error          string          `json:"error,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
}

func ingestionState(knowledge *types.Knowledge) (knowledgeIngestionState, error) {
	var state knowledgeIngestionState
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(knowledge.Metadata, &envelope); err != nil {
		return state, err
	}
	if raw := envelope[ingestionMetadataKey]; len(raw) > 0 {
		return state, json.Unmarshal(raw, &state)
	}
	return state, nil
}
func setIngestionState(knowledge *types.Knowledge, state knowledgeIngestionState) error {
	envelope := map[string]json.RawMessage{}
	if len(knowledge.Metadata) > 0 {
		if err := json.Unmarshal(knowledge.Metadata, &envelope); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	envelope[ingestionMetadataKey] = raw
	encoded, err := json.Marshal(envelope)
	if err == nil {
		knowledge.Metadata = types.JSON(encoded)
	}
	return err
}

// ValidateIngestionSource is callable only behind the dedicated HMAC route. The
// server-loaded attempt is the admitted task grant; caller-supplied IDs alone
// cannot authorize a write. Normal read/user routes retain their existing ACLs.
func (s *knowledgeService) ValidateIngestionSource(ctx context.Context, claim ingestion.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	ctx = types.WithExecutionTenant(ctx, claim.TenantID)
	knowledge, err := s.repo.GetKnowledgeByID(ctx, claim.TenantID, claim.KnowledgeID)
	if err != nil {
		return err
	}
	if knowledge == nil || knowledge.TenantID != claim.TenantID || knowledge.KnowledgeBaseID != claim.KnowledgeBaseID || knowledge.Type != "file" || knowledge.FileHash != claim.FileHash || knowledge.FileSize != claim.FileSize || knowledge.FileName != claim.FileName || knowledge.FileType != claim.FileType || knowledge.ParseStatus == types.ParseStatusDeleting || knowledge.ParseStatus == types.ParseStatusCancelled || knowledge.DeletedAt.Valid {
		return errors.New("ingestion source is no longer current")
	}
	state, err := ingestionState(knowledge)
	if err != nil {
		return err
	}
	if state.Attempt != claim.Attempt || state.SourceHash != knowledge.FileHash {
		return errors.New("ingestion attempt is no longer current")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, claim.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if kb == nil || kb.TenantID != claim.TenantID || kb.DeletedAt.Valid {
		return fmt.Errorf("ingestion knowledge base is no longer current")
	}
	return nil
}

func unifiedIngestionAvailable() bool {
	return os.Getenv("STRUCTURED_DATA_URL") != "" && os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE") != ""
}
func (s *knowledgeService) saveIngestionState(ctx context.Context, knowledge *types.Knowledge, state knowledgeIngestionState) error {
	current, err := s.repo.GetKnowledgeByID(ctx, knowledge.TenantID, knowledge.ID)
	if err != nil {
		return err
	}
	if current == nil || current.FileHash != knowledge.FileHash || current.KnowledgeBaseID != knowledge.KnowledgeBaseID || current.ParseStatus == types.ParseStatusCancelled || current.ParseStatus == types.ParseStatusDeleting {
		return errors.New("source changed during ingestion")
	}
	old, err := ingestionState(current)
	if err != nil {
		return err
	}
	if old.Attempt != state.Attempt {
		return errors.New("ingestion attempt superseded")
	}
	if err = setIngestionState(current, state); err != nil {
		return err
	}
	if err = s.repo.UpdateKnowledgeColumn(ctx, current.ID, "metadata", current.Metadata); err != nil {
		return err
	}
	knowledge.Metadata = current.Metadata
	return nil
}

// processUnifiedTables runs inside the durable native document task. Only a
// source-bound receipt is persisted, never a user bearer token or service key.
func (s *knowledgeService) processUnifiedTables(ctx context.Context, knowledge *types.Knowledge, kb *types.KnowledgeBase, envelope json.RawMessage) (json.RawMessage, error) {
	state, err := ingestionState(knowledge)
	if err != nil {
		return nil, err
	}
	if state.Attempt == "" {
		return nil, nil
	}
	if state.SourceHash != knowledge.FileHash {
		return nil, errors.New("ingestion source changed")
	}
	if state.Status == "completed" || state.Status == "needs_attention" {
		return state.Result, nil
	}
	if len(envelope) == 0 {
		envelope = json.RawMessage(`{}`)
	}
	var captured map[string]json.RawMessage
	if err := json.Unmarshal(envelope, &captured); err != nil {
		return nil, errors.New("识别结果格式无效")
	}
	captured["native_default_model_id"], _ = json.Marshal(kb.SummaryModelID)
	envelope, err = json.Marshal(captured)
	if err != nil {
		return nil, err
	}
	if len(envelope) > 32<<20 {
		return nil, errors.New("PDF识别结果超过单次处理范围，原件已保留")
	}
	key, err := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	if err != nil {
		return nil, fmt.Errorf("读取表格处理服务配置失败")
	}
	key = bytes.TrimSpace(key)
	endpoint := strings.TrimRight(os.Getenv("STRUCTURED_DATA_URL"), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("表格处理服务地址无效")
	}
	original, err := s.resolveFileServiceForPath(ctx, kb, knowledge.FilePath).GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(original, (100<<20)+1))
	original.Close()
	if readErr != nil {
		return nil, readErr
	}
	if len(raw) > 100<<20 || int64(len(raw)) != knowledge.FileSize {
		return nil, errors.New("原件大小已改变")
	}
	md5sum, sha := md5.Sum(raw), sha256.Sum256(raw)
	if hex.EncodeToString(md5sum[:]) != knowledge.FileHash {
		return nil, errors.New("原件内容已改变")
	}
	bundleHash := sha256.Sum256(envelope)
	claim := ingestion.Claim{Attempt: state.Attempt, TenantID: knowledge.TenantID, KnowledgeBaseID: knowledge.KnowledgeBaseID, KnowledgeID: knowledge.ID, UserID: types.TaskInitiatorFromContext(ctx).UserID, FileName: knowledge.FileName, FileType: knowledge.FileType, FileHash: knowledge.FileHash, FileSize: knowledge.FileSize, SourceSHA256: hex.EncodeToString(sha[:]), BundleSHA256: hex.EncodeToString(bundleHash[:])}
	if err = s.ValidateIngestionSource(ctx, claim); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", knowledge.FileName)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(raw); err != nil {
		return nil, err
	}
	if err = writer.WriteField("bundle", string(envelope)); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	raw = nil
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, payload io.Reader, contentType string) (map[string]json.RawMessage, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, payload)
		if err != nil {
			return nil, err
		}
		if err = ingestion.Sign(req, claim, key, time.Now()); err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("表格处理服务暂时不可用")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("表格处理未完成（HTTP %d），已有结果保留", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if err != nil || len(data) > 8<<20 {
			return nil, errors.New("表格处理状态超过大小限制")
		}
		var result map[string]json.RawMessage
		if err = json.Unmarshal(data, &result); err != nil {
			return nil, errors.New("表格处理状态无效")
		}
		return result, nil
	}
	state.Status, state.Stage, state.Error = "processing", "正在整理表格", ""
	if err = s.saveIngestionState(ctx, knowledge, state); err != nil {
		return nil, err
	}
	if _, err = call(http.MethodPost, ingestion.UploadPath, &body, writer.FormDataContentType()); err != nil {
		return nil, err
	}
	body.Reset()
	for {
		if err = s.ValidateIngestionSource(ctx, claim); err != nil {
			return nil, err
		}
		result, err := call(http.MethodGet, ingestion.ResultPath, nil, "")
		if err != nil {
			return nil, err
		}
		var status, stage string
		json.Unmarshal(result["status"], &status)
		json.Unmarshal(result["stage"], &stage)
		state.Status, state.Stage = status, stage
		if status == "failed" {
			state.Error = "表格处理未完成，原件和已有表格已保留，请重试。"
			var safeError string
			if json.Unmarshal(result["error"], &safeError) == nil && safeError != "" && len([]rune(safeError)) <= 500 {
				state.Error = safeError
			}
			_ = s.saveIngestionState(ctx, knowledge, state)
			return nil, errors.New(state.Error)
		}
		if status == "completed" {
			state.Result = result["result"]
			var summary struct {
				AttentionCount int `json:"attention_count"`
			}
			if err = json.Unmarshal(state.Result, &summary); err != nil {
				return nil, errors.New("表格处理结果无效")
			}
			state.AttentionCount = summary.AttentionCount
			if state.AttentionCount > 0 {
				state.Status = "needs_attention"
				state.Stage = fmt.Sprintf("可用表格已入库，%d处需要处理", state.AttentionCount)
			}
			if err = s.saveIngestionState(ctx, knowledge, state); err != nil {
				return nil, err
			}
			return state.Result, nil
		}
		if status != "processing" {
			return nil, errors.New("表格处理状态无效")
		}
		if err = s.saveIngestionState(ctx, knowledge, state); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// workbookIngestionDocument indexes the actual sheet catalog, not unreviewed
// cell values. Embedded raster originals remain bound to this same workbook.
func (s *knowledgeService) workbookIngestionDocument(ctx context.Context, knowledge *types.Knowledge, kb *types.KnowledgeBase, result json.RawMessage) (*types.ReadResult, error) {
	var catalog struct {
		Sheets []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"sheets"`
		AttentionCount int `json:"attention_count"`
	}
	if err := json.Unmarshal(result, &catalog); err != nil {
		return nil, err
	}
	var text strings.Builder
	text.WriteString("工作簿：" + knowledge.FileName + "\n表格数据已自动整理；具体数值请查询本知识库已入库的表格，并核对单位和来源。\n")
	for _, sheet := range catalog.Sheets {
		text.WriteString("工作表：" + sheet.Name + "\n")
	}
	if catalog.AttentionCount > 0 {
		text.WriteString(fmt.Sprintf("还有%d张工作表需要处理，请查看文件处理结果。\n", catalog.AttentionCount))
	}
	file, err := s.resolveFileServiceForPath(ctx, kb, knowledge.FilePath).GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (100<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 100<<20 {
		return nil, errors.New("工作簿超过大小限制")
	}
	book, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errors.New("工作簿文件格式无效")
	}
	out := &types.ReadResult{}
	total := 0
	for _, entry := range book.File {
		if !strings.HasPrefix(entry.Name, "xl/media/") || strings.Contains(entry.Name, "..") || strings.Contains(entry.Name, "\\") || entry.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" {
			continue
		}
		if entry.UncompressedSize64 > 16<<20 || len(out.ImageRefs) >= 256 {
			return nil, errors.New("工作簿图片超过处理范围")
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(stream, (16<<20)+1))
		stream.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if len(data) > 16<<20 || total > 64<<20 {
			return nil, errors.New("工作簿图片超过处理范围")
		}
		ref := fmt.Sprintf("workbook-image-%d%s", len(out.ImageRefs)+1, ext)
		out.ImageRefs = append(out.ImageRefs, types.ImageRef{Filename: ref, OriginalRef: ref, MimeType: http.DetectContentType(data), ImageData: data, SkipMultimodal: catalog.AttentionCount > 0})
		text.WriteString(fmt.Sprintf("\n![工作簿原图 %d](%s)\n", len(out.ImageRefs), ref))
	}
	out.MarkdownContent = text.String()
	return out, nil
}
