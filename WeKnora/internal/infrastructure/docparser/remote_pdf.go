package docparser

// Remote PDF is an explicit engine. Its queue protocol is not MinerU's API.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

const RemotePDFEngineName = "remote_vlm"

// Bound native buffers before io.ReadAll, independently of asynchronous workers.
var remotePDFBuffers = make(chan struct{}, 2)

func AcquireRemotePDFBuffer(ctx context.Context) (func(), error) {
	select {
	case remotePDFBuffers <- struct{}{}:
		return func() { <-remotePDFBuffers }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type remotePDFEngine struct{}

func (*remotePDFEngine) Name() string { return RemotePDFEngineName }
func (*remotePDFEngine) Description() string {
	return "Remote vision PDF parser with shared queue and table validation"
}
func (*remotePDFEngine) FileTypes(bool) []string { return []string{"pdf"} }
func (*remotePDFEngine) CheckAvailable(bool, map[string]string) (bool, string) {
	if os.Getenv("WEKNORA_REMOTE_PDF_URL") == "" || os.Getenv("WEKNORA_REMOTE_PDF_TOKEN") == "" {
		return false, "Remote PDF service is not configured by the administrator"
	}
	return true, ""
}
func (*remotePDFEngine) NewReader(context.Context, ReaderDeps) (interfaces.DocReader, error) {
	return &RemotePDFReader{endpoint: strings.TrimRight(os.Getenv("WEKNORA_REMOTE_PDF_URL"), "/"),
		token: os.Getenv("WEKNORA_REMOTE_PDF_TOKEN"), poll: 2 * time.Second}, nil
}

type RemotePDFReader struct {
	endpoint, token string
	poll            time.Duration
}

type remotePDFJob struct {
	ID      string          `json:"id"`
	Status  string          `json:"status"`
	LeaseID string          `json:"lease_id"`
	Error   json.RawMessage `json:"error"`
}

// RemotePDFError is a safe protocol failure: no backend body or secret is exposed.
type RemotePDFError struct{ Code string }

func (e *RemotePDFError) Error() string { return "Remote PDF parser: " + e.Code }

func (e *RemotePDFError) PublicCode() string {
	return "REMOTE_PDF_" + strings.TrimPrefix(strings.ToUpper(safeRemotePDFCode(e.Code)), "REMOTE_PDF_")
}

func (e *RemotePDFError) PublicMessage() string { return remotePDFFailureMessage(e.Code) }

func (c *RemotePDFReader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	tenant, ok := types.TenantIDFromContext(ctx)
	if !ok || tenant == 0 {
		return nil, &RemotePDFError{Code: "missing_scope"}
	}
	if c.endpoint == "" || c.token == "" {
		return nil, &RemotePDFError{Code: "not_configured"}
	}
	if err := validateMinerUOutboundURL(c.endpoint); err != nil {
		return nil, err
	}
	if len(req.FileContent) == 0 || len(req.FileContent) > 100<<20 || !bytes.HasPrefix(req.FileContent, []byte("%PDF-")) {
		return &types.ReadResult{Error: "Remote PDF requires a valid PDF of at most 100 MiB"}, nil
	}
	scope := "tenant:" + strconv.FormatUint(tenant, 10)
	owner, _ := types.UserIDFromContext(ctx)
	if owner == "" {
		owner = scope
	}
	client := utils.NewSSRFSafeHTTPClient(utils.SSRFSafeHTTPClientConfig{Timeout: 60 * time.Second, MaxRedirects: 0})
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	leaseHeader := ""
	retryHeader := ""
	call := func(callCtx context.Context, method, route string, body io.Reader, contentType string, out any) error {
		r, err := http.NewRequestWithContext(callCtx, method, c.endpoint+route, body)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+c.token)
		r.Header.Set("X-Parse-Scope", scope)
		r.Header.Set("X-Parse-Owner", owner)
		if retryHeader != "" {
			r.Header.Set("X-Parse-Retry-Key", retryHeader)
		}
		if method == "DELETE" {
			r.Header.Set("X-Parse-Lease", leaseHeader)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(r)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &RemotePDFError{Code: fmt.Sprintf("http_%d", resp.StatusCode)}
		}
		if out == nil {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 64<<20 {
			return &RemotePDFError{Code: "oversize_result"}
		}
		if err = json.Unmarshal(raw, out); err != nil {
			return &RemotePDFError{Code: "invalid_response"}
		}
		return nil
	}
	var upload bytes.Buffer
	w := multipart.NewWriter(&upload)
	part, err := w.CreateFormFile("file", "document.pdf")
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(req.FileContent); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	var job remotePDFJob
	if err = call(ctx, "POST", "/v1/jobs", &upload, w.FormDataContentType(), &job); err != nil {
		return nil, err
	}
	if job.ID == "" || strings.ContainsAny(job.ID, "/\\?#") {
		return nil, &RemotePDFError{Code: "invalid_job_id"}
	}
	route := "/v1/jobs/" + url.PathEscape(job.ID)
	if req.ParserRetryKey != "" && (job.Status == "failed" || job.Status == "cancelled" || job.Status == "canceled") {
		retryHeader = req.ParserRetryKey
		if err = call(ctx, "POST", route+"/retry", nil, "", &job); err != nil {
			return nil, err
		}
		retryHeader = ""
	}
	if job.LeaseID == "" || len(job.LeaseID) > 128 || strings.ContainsAny(job.LeaseID, "/\\?#\r\n") {
		return nil, &RemotePDFError{Code: "invalid_lease_id"}
	}
	leaseID := job.LeaseID
	finished := false
	defer func() {
		if finished {
			return
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		leaseHeader = leaseID
		_ = call(cancelCtx, "DELETE", route, nil, "", nil)
	}()
	interval := c.poll
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for job.Status != "completed" {
		if job.Status == "failed" || job.Status == "canceled" || job.Status == "cancelled" {
			finished = true
			var detail struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(job.Error, &detail)
			code := detail.Code
			if code == "" {
				code = job.Status
			}
			// Do not trigger native whole-document retries for a terminal extraction failure.
			code = safeRemotePDFCode(code)
			return &types.ReadResult{Error: remotePDFFailureMessage(code), Metadata: map[string]string{
				"parser_engine": RemotePDFEngineName, "parser_job_id": job.ID, "parser_error_code": code,
			}}, nil
		}
		if job.Status != "queued" && job.Status != "running" {
			return nil, &RemotePDFError{Code: "invalid_status"}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if err = call(ctx, "GET", route, nil, "", &job); err != nil {
			return nil, err
		}
	}
	var artifact struct {
		Schema string `json:"schema_version"`
		Source struct {
			SHA       string `json:"sha256"`
			PageCount int    `json:"page_count"`
		} `json:"source"`
		Pages []struct {
			Page     int    `json:"page"`
			Text     string `json:"text"`
			Markdown string `json:"markdown"`
			Tables   []struct {
				Title   string   `json:"title"`
				Columns []string `json:"columns"`
			} `json:"tables"`
			Formulas []struct {
				Latex string `json:"latex"`
			} `json:"formulas"`
		} `json:"pages"`
		Quality json.RawMessage `json:"quality"`
	}
	if err = call(ctx, "GET", route+"/result", nil, "", &artifact); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(req.FileContent)
	if artifact.Schema != "remote_pdf_v1" || artifact.Source.SHA != hex.EncodeToString(hash[:]) || len(artifact.Pages) == 0 || artifact.Source.PageCount != len(artifact.Pages) {
		return nil, &RemotePDFError{Code: "source_mismatch"}
	}
	result := &types.ReadResult{Metadata: map[string]string{"pages": strconv.Itoa(len(artifact.Pages)), "parser_engine": RemotePDFEngineName, "parser_job_id": job.ID, "parser_quality": string(artifact.Quality), "parser_table_index_policy": "reviewed_structured_values_only"}}
	var output strings.Builder
	offset := 0
	for i, p := range artifact.Pages {
		if p.Page != i+1 {
			return nil, &RemotePDFError{Code: "page_coverage_invalid"}
		}
		text := p.Markdown
		if text == "" {
			text = p.Text
		}
		if len(p.Tables) > 0 {
			// Independent model agreement is not source approval. Even unanimously
			// misread numbers must not become authoritative RAG data. Index discovery
			// metadata only; exact cells remain in the source-linked review bundle.
			// Omit page text too: providers can duplicate table cells into that field.
			var catalog strings.Builder
			catalog.WriteString("[PDF 表格尚未核对原件。此索引仅供发现表格和字段；数值请查询经过审核发布的结构化表，或核对原件。不能由此索引回答具体数值。]\n")
			for _, table := range p.Tables {
				catalog.WriteString("表格：" + table.Title + "\n字段：" + strings.Join(table.Columns, "；") + "\n")
			}
			for _, formula := range p.Formulas {
				catalog.WriteString("[未核对的原页公式]\n$$\n" + formula.Latex + "\n$$\n")
			}
			text = catalog.String()
		}
		if text == "" {
			text = "[No readable text on this page]"
		}
		segment := fmt.Sprintf("\n\n<!-- PDF page %d -->\n%s", p.Page, text)
		output.WriteString(segment)
		end := offset + len([]rune(segment))
		result.SourceBlocks = append(result.SourceBlocks, types.SourceBlock{Start: offset, End: end, Locator: types.SourceLocator{Type: types.SourceLocatorPDF, Page: p.Page, SourceID: fmt.Sprintf("remote_pdf:%d", p.Page)}})
		offset = end
	}
	result.MarkdownContent = output.String()
	finished = true
	return result, nil
}

func safeRemotePDFCode(code string) string {
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "failed"
		}
	}
	if len(code) > 64 {
		return "failed"
	}
	return code
}

func remotePDFFailureMessage(code string) string {
	message := map[string]string{
		"HTTP_429":                  "解析队列已满，请稍后提交；现有任务不会被重复识别。",
		"HTTP_507":                  "解析服务存储空间或配额不足，请管理员清理或扩容。",
		"HTTP_401":                  "远程解析服务认证失败，请管理员检查内部服务配置。",
		"HTTP_403":                  "远程解析服务拒绝访问，请管理员检查服务权限。",
		"NOT_CONFIGURED":            "远程 PDF 解析尚未配置，请管理员配置解析服务地址及凭据。",
		"MODEL_CONTENT_FILTER":      "模型服务拒绝处理文档页面，未返回识别结果；请检查获准模型或本地 OCR 配置，并保留页面审核。",
		"MODEL_AUTH_FAILED":         "远程模型认证或授权失败，请检查模型配置及账户状态。",
		"MODEL_RATE_LIMIT":          "远程模型限流，重试预算已用完；请稍后手动重试。",
		"MODEL_TIMEOUT":             "远程模型请求超时；已保留完成页面，请检查服务状态后手动重试。",
		"MODEL_UNAVAILABLE":         "远程模型暂不可用；已保留完成页面，请稍后手动重试。",
		"REMOTE_PDF_OUTPUT_INVALID": "模型返回的页面或表格结构未通过校验，未发布不完整结果；请检查原页及解析诊断。",
		"PARSER_STORAGE_EXHAUSTED":  "解析服务存储空间或配额不足，请管理员清理或扩容。",
		"EXECUTION_TIMEOUT":         "文档解析超过执行时限；已保留完成页面，请检查服务负载后手动重试。",
		"QUEUE_TIMEOUT":             "文档等待解析超过排队时限，请稍后重新提交。",
	}[strings.ToUpper(code)]
	if message == "" {
		message = "远程解析未完成，请查看任务状态及错误代码后手动重试。"
	}
	return message + " (" + safeRemotePDFCode(code) + ")"
}
