package tools

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const ToolStructuredData = "structured_data"

// StructuredDataTool uses the authenticated caller, independent of a model's arguments
// or a shared agent's execution tenant. No operation writes research state.
type StructuredDataTool struct {
	BaseTool
	endpoint            string
	sourceQuestion      string
	key                 []byte
	client              *http.Client
	readCache           structuredReadCache
	knowledgeScopes     []structuredKnowledgeScope
	knowledgeMetadata   map[string]structuredKnowledgeMetadata
	scopeConfigured     bool
	scopeError          error
	protocolMu          sync.RWMutex
	protocolGeneration  uint64
	protocolDescriptors map[string]StructuredOperationDescriptor
}

func NewStructuredDataTool() *StructuredDataTool {
	key, _ := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	return &StructuredDataTool{BaseTool: NewBaseTool(ToolStructuredData,
		"只读查询本轮授权范围的Excel、已发布PDF表及原单元格证据。单库自动限定，多库先读取真实目录，再按完整问题和上下文选knowledge_base_id。list_datasets的datasets仅指Excel，pdf_tables才是PDF；旧文档索引的待审/仅字段说明可能过时，断言PDF未入库或无数值前须核实当前目录。reviewed/auto_accepted均已入库，extraction_verified=false不表示未入库；历史警告不等于当前未解决警告。list_pdf_tables分页列出列/单位/前两行导航样例，output_truncated时按next_offset继续，通用列名可通过样例理解；样例不能作数值证据。read_pdf_table核对原值/来源，query_pdf_table用真实列名只读计算。PDF草稿不可计算，人工确认不保证OCR正确，仍须检查单位、跨页覆盖。只交付问题所需且绑定实际表/单元格的事实；缺乏绑定证据的附加统计范围或原件版面断言应省略。Excel可analyze_table，inspect_source检查未解析原件；resolve_fields处理语义歧义，confirm_fields须真实用户选择。按需describe_tools获取schema，不会修改原表。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation","arguments"],"properties":{"knowledge_base_id":{"type":"string","description":"可选的真实知识库ID，必须在本轮已授权的知识库目录中；单库时默认使用该库，多库时先读取返回目录再根据完整问题选择"},"operation":{"type":"string","enum":["describe_tools","inspect_source","list_datasets","list_series","list_charts","query_series","compare_series","convert_price","resolve_fields","confirm_fields","execute_field_plan","get_result_evidence","analyze_table","list_pdf_tables","read_pdf_table","query_pdf_table"]},"arguments":{"type":"object","description":"先describe_tools {operations:[目标操作]}获取参数schema；其余参数沿用操作schema，不接受账号、空间、URL或凭据"}}}`)),
		endpoint: strings.TrimRight(os.Getenv("STRUCTURED_DATA_URL"), "/"),
		key:      bytes.TrimSpace(key), client: &http.Client{Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// BindSourceQuestion records the real human request for this per-turn tool.
func (t *StructuredDataTool) BindSourceQuestion(question string) {
	t.bindStructuredSourceQuestion(question)
}

// BindSourceConversation supplies trusted conversation provenance to semantic
// source review. It preserves the complete current request; older context is
// bounded by protocol budgets rather than any question vocabulary.
func (t *StructuredDataTool) BindSourceConversation(question string, messages []chat.Message) {
	rows := []map[string]string{}
	budget := 4096
	for i := len(messages) - 1; i >= 0 && len(rows) < 8; i-- {
		message := messages[i]
		if (message.Role != "user" && message.Role != "assistant") || message.Content == question || len(message.Content) > budget {
			continue
		}
		rows = append([]map[string]string{{"role": message.Role, "content": message.Content}}, rows...)
		budget -= len(message.Content)
	}
	payload, _ := json.Marshal(map[string]any{"current_user_question": question, "recent_conversation": rows})
	t.BindSourceQuestion(string(payload))
}

func (t *StructuredDataTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() {
		return nil, fmt.Errorf("结构化表格读取需要已核验的用户和空间")
	}
	if len(t.key) < 32 || t.endpoint == "" {
		return nil, fmt.Errorf("结构化表格服务尚未配置")
	}
	parsed, err := url.Parse(t.endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, fmt.Errorf("结构化表格服务地址配置无效")
	}
	var input map[string]json.RawMessage
	if len(args) > 16384 || json.Unmarshal(args, &input) != nil || input == nil {
		return nil, fmt.Errorf("结构化表格参数格式无效")
	}
	for field := range input {
		if field != "operation" && field != "arguments" && field != "knowledge_base_id" {
			return nil, fmt.Errorf("结构化表格不接受未声明的顶层参数 %q；顶层只允许 operation、arguments、knowledge_base_id。操作参数须按真实工具schema放在 arguments 内，或直接使用已注册的 structured_ 操作工具；不要提交账号、权限或服务配置", field)
		}
	}
	var operation string
	var arguments map[string]json.RawMessage
	if json.Unmarshal(input["operation"], &operation) != nil || json.Unmarshal(input["arguments"], &arguments) != nil || arguments == nil {
		return nil, fmt.Errorf("结构化表格需要合法 operation 和 arguments 对象")
	}
	if _, supported := structuredOperationContracts[operation]; !supported {
		return nil, fmt.Errorf("结构化表格不支持此操作")
	}
	if _, pinned := ctx.Value(structuredInvocationKey{}).(StructuredOperationDescriptor); !pinned && operation != "describe_tools" {
		t.protocolMu.RLock()
		descriptor, available := t.protocolDescriptors[operation]
		t.protocolMu.RUnlock()
		if available {
			ctx = withStructuredInvocation(ctx, descriptor)
		}
	}
	for _, field := range []string{"tenant_id", "user_id", "role", "token", "api_key", "url", "endpoint", "data_dir", "knowledge_base_id", "knowledge_base_tenant_id", "knowledge_base_can_write"} {
		if _, exists := arguments[field]; exists {
			if field == "knowledge_base_id" {
				return nil, fmt.Errorf("knowledge_base_id 必须放在顶层，与 operation、arguments 同级，不能放在 arguments 内；请从本轮已授权目录选择真实ID")
			}
			return nil, fmt.Errorf("结构化表格参数不接受身份、凭据或服务配置")
		}
	}
	scope, directory, err := t.resolveStructuredKnowledgeScope(input["knowledge_base_id"])
	if err != nil {
		return t.structuredScopeFailure(ctx, err), err
	}
	if err = t.authorizeStructuredAPIKeyScope(ctx, scope, directory != nil); err != nil {
		return t.structuredScopeFailure(ctx, &structuredScopeError{"scope_not_authorized", err.Error(), false}), err
	}
	if directory != nil {
		if operation == "list_datasets" {
			if recalled, recallErr := t.recallAcrossKnowledgeBases(ctx, arguments, directory); recalled != nil {
				return fitStructuredRecallDirectory(ctx, recalled), recallErr
			}
		}
		if operation != "describe_tools" {
			if operation != "list_datasets" {
				directory.Success = false
				directory.Error = "尚未执行 " + operation + "：多知识库必须在顶层传 knowledge_base_id（与 operation、arguments 同级）；请从返回的已授权目录选择真实ID并重试原操作。此返回不是查询结果，也不表示无数据。"
			}
			if descriptor, ok := ctx.Value(structuredInvocationKey{}).(StructuredOperationDescriptor); ok {
				attachStructuredDiagnosticDescriptor(directory, descriptor)
			}
			return fitStructuredDirectory(ctx, operation, directory), nil
		}
		// describe_tools is a service protocol directory: the server returns
		// mcp.list_tools() before accessing any workspace data. Keep the caller
		// identity, but do not choose a business KB merely to fetch schemas.
		scope = structuredKnowledgeScope{}
	}
	if operation == "describe_tools" {
		// Schema discovery is a service protocol read and carries no knowledge
		// data. Keep its signed identity unscoped for single- and multi-base
		// callers alike; data operations retain their verified scope below.
		scope = structuredKnowledgeScope{}
	}
	sourceQuestion, cacheGeneration := t.structuredSourceQuestion()
	if sourceQuestion != "" && (operation == "query_series" || operation == "analyze_table" || operation == "compare_series" || operation == "convert_price" || operation == "resolve_fields") {
		question, _ := json.Marshal(sourceQuestion)
		arguments["source_question"] = question
	}
	// Scope is a signed identity field, never a service operation parameter.
	args, err = json.Marshal(map[string]any{"operation": operation, "arguments": arguments})
	if err != nil {
		return nil, fmt.Errorf("结构化来源口径参数无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, cached, err := t.beginStructuredReadCache(ctx, caller, scope, operation, arguments, sourceQuestion, cacheGeneration)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return fitStructuredDirectory(ctx, operation, cached), nil
	}
	var cacheResult *types.ToolResult
	defer func() { t.finishStructuredReadCache(lease, cacheResult) }()
	identity := map[string]any{"user_id": caller.UserID, "tenant_id": caller.TenantID,
		"role": string(caller.Role), "email": "", "display_name": caller.UserID}
	if _, machine := types.TenantAPIKeyScopeFromContext(ctx); machine && scope.KnowledgeBaseID != "" {
		identity["principal_type"] = "api_key"
	} else if authorization := types.ResearchAuthorizationFromContext(ctx); authorization != "" && scope.KnowledgeBaseID != "" {
		authorizationHash := sha256.Sum256([]byte(authorization))
		identity["authorization_sha256"] = hex.EncodeToString(authorizationHash[:])
	}
	if scope.KnowledgeBaseID != "" {
		identity["knowledge_base_id"] = scope.KnowledgeBaseID
		identity["knowledge_base_tenant_id"] = scope.TenantID
	}
	if user, ok := ctx.Value(types.UserContextKey).(*types.User); ok && user.ID == caller.UserID {
		identity["email"], identity["display_name"] = user.Email, user.Username
	}
	rawIdentity, _ := json.Marshal(identity)
	encoded := base64.RawURLEncoding.EncodeToString(rawIdentity)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	bodyHash := sha256.Sum256(args)
	message := strings.Join([]string{stamp, "POST", "/api/tools/read", encoded, hex.EncodeToString(bodyHash[:])}, "\n")
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(message))
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint+"/api/tools/read", bytes.NewReader(args))
	if err != nil {
		return nil, fmt.Errorf("结构化表格请求构造失败")
	}
	req.Header.Set("Content-Type", "application/json")
	if descriptor, ok := ctx.Value(structuredInvocationKey{}).(StructuredOperationDescriptor); ok && descriptor.ManifestSHA256 != "" {
		req.Header.Set("X-Structured-Manifest-SHA256", descriptor.ManifestSHA256)
	}
	req.Header.Set("X-Research-Identity", encoded)
	req.Header.Set("X-Research-Timestamp", stamp)
	req.Header.Set("X-Research-Signature", hex.EncodeToString(mac.Sum(nil)))
	if authorization := types.ResearchAuthorizationFromContext(ctx); authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	requestStarted := time.Now()
	var confirmationAttempt structuredConfirmationAttempt
	if operation == "confirm_fields" {
		var allowed bool
		confirmationAttempt, allowed = t.beginStructuredConfirmationAttempt(caller, scope, arguments)
		if !allowed {
			return structuredProtocolFailure("confirmation_already_attempted", "本轮已向该待选方案提交过一次真实用户回复；不会再次确认或重放。请依据实际返回等待用户继续补充，传输失败时不能假定确认未执行", "confirmation", false), nil
		}
	}
	response, err := t.client.Do(req)
	if err != nil && ctx.Err() == nil {
		contract := structuredOperationContracts[operation]
		deadline, bounded := ctx.Deadline()
		if contract.effect == "read" && structuredConnectionFailureBeforeRequest(err) && (!bounded || time.Until(deadline) > 250*time.Millisecond) && req.GetBody != nil {
			// Retry only an unsuccessful connection attempt. A response/read
			// timeout can leave the original service computation running, even
			// for pure reads. Stateful operations never enter this path.
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			retryCtx, retryCancel := context.WithTimeout(ctx, 2*time.Second)
			defer retryCancel()
			retry := req.Clone(retryCtx)
			if retry.Body, err = req.GetBody(); err == nil {
				response, err = t.client.Do(retry)
			}
		}
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		ctxErr := ctx.Err()
		ctxCancelled := errors.Is(ctxErr, context.Canceled)
		timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctxErr, context.DeadlineExceeded)
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			timedOut = true
		}
		logger.Warnf(ctx, "[Tool][StructuredData] request failed operation=%s elapsed_ms=%d error_type=%T ctx_cancelled=%t timeout=%t",
			operation, time.Since(requestStarted).Milliseconds(), err, ctxCancelled, timedOut)
		return nil, structuredRequestFailure(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
		if readErr == nil && len(body) <= 16*1024 {
			var conflict struct {
				Detail struct {
					Code string `json:"code"`
				} `json:"detail"`
				Code string `json:"code"`
			}
			if json.Unmarshal(body, &conflict) == nil && (conflict.Code == "manifest_changed" || conflict.Detail.Code == "manifest_changed") {
				t.releaseUnexecutedStructuredConfirmation(confirmationAttempt)
				return structuredProtocolFailure("manifest_changed", "结构化服务能力版本已变化；本次未执行。请通过 structured_capabilities refresh=true 取得当前扁平定义，再修正调用，不能自动重放旧参数", "manifest", false), nil
			}
		}
		return nil, fmt.Errorf("结构化表格读取失败（HTTP 409），本次未取得可用结果；核对真实来源范围与当前版本")
	}
	if response.StatusCode == http.StatusBadRequest {
		// Native argument validation happens before its operation is called.
		// Correcting a rejected schema is not replay of an executed selection.
		t.releaseUnexecutedStructuredConfirmation(confirmationAttempt)
		// The destination is fixed and signed. Validation details are evidence
		// for correcting arguments, not instructions, and have a smaller bound.
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024+1))
		if readErr != nil || len(body) > 16*1024 {
			return nil, fmt.Errorf("结构化表格参数校验失败（HTTP 400）；错误详情超过限制")
		}
		var data map[string]any
		if json.Unmarshal(body, &data) != nil || data == nil {
			return nil, fmt.Errorf("结构化表格参数校验失败（HTTP 400）；错误详情格式无效")
		}
		detail := string(body)
		if value, exists := data["detail"]; exists {
			if encoded, encodeErr := json.Marshal(value); encodeErr == nil {
				detail = string(encoded)
			}
		}
		return &types.ToolResult{Success: false, Output: string(body), Data: data,
			Error: "结构化表格参数校验失败（HTTP 400）：" + detail}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("结构化表格读取失败（HTTP %d），请检查服务、身份或查询参数", response.StatusCode)
	}
	if descriptor, ok := ctx.Value(structuredInvocationKey{}).(StructuredOperationDescriptor); ok && descriptor.ManifestSHA256 != "" && response.Header.Get("X-Structured-Manifest-SHA256") != descriptor.ManifestSHA256 {
		return structuredProtocolFailure("manifest_execution_unverified", "服务未确认本次工具协议版本；未接受结果。状态操作不得重放，请核对当前服务能力", "manifest", true), nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	if err != nil {
		return nil, structuredRequestFailure(ctx, err)
	}
	if len(body) > 512*1024 {
		return nil, fmt.Errorf("结构化表格结果过大，请缩小范围")
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil || data == nil {
		return nil, fmt.Errorf("结构化表格结果格式无效")
	}
	if status, _ := data["status"].(string); status == "error" {
		detail, _ := data["error"].(string)
		if detail == "" {
			detail = "结构化表格服务返回查询错误；请按真实反馈修正参数"
		}
		cacheResult = &types.ToolResult{Success: false, Output: string(body), Data: data, Error: detail}
		return cacheResult, nil
	}
	if scope.KnowledgeBaseID != "" && (operation == "resolve_fields" || operation == "confirm_fields") && data["kind"] == "structured_field_resolution" {
		// Persist the actual resolution's transport binding separately from the
		// model call. A subsequent human clarification must return to this KB,
		// including when the prior model omitted the scope restored by runtime.
		// No account, owner tenant or credential is exposed in this locator.
		data["resolution_binding"] = map[string]any{"knowledge_base_id": scope.KnowledgeBaseID, "canonical_operation": operation, "resolution_id": data["resolution_id"]}
		if visible, encodeErr := json.Marshal(data); encodeErr == nil {
			body = visible
		}
	}
	if operation != "describe_tools" && scope.KnowledgeBaseID != "" {
		data["source_context"] = map[string]any{
			"knowledge_base_id":        scope.KnowledgeBaseID,
			"knowledge_base_tenant_id": scope.TenantID,
		}
	}
	cacheResult = &types.ToolResult{Success: true, Output: string(body), Data: data}
	return fitStructuredDirectory(ctx, operation, cacheResult), nil
}

// A dial failure establishes that this attempt did not reach the service.
// Other transport errors can occur after the request was submitted and must
// not start a second still-running computation automatically.
func structuredConnectionFailureBeforeRequest(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return false
	}
	var operationError *net.OpError
	return errors.As(err, &operationError) && operationError.Op == "dial"
}

// Preserve cancellation/deadline identity without exposing request URLs,
// provider bodies or credentials through the underlying transport error.
func structuredRequestFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("结构化表格请求已取消；未取得完整结果: %w", context.Canceled)
	}
	var networkError net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("结构化表格请求超时；未取得完整结果，不能据此判断来源不存在: %w", context.DeadlineExceeded)
	}
	return fmt.Errorf("结构化表格服务连接或响应读取失败；未取得完整结果")
}
