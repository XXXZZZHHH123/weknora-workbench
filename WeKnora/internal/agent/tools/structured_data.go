package tools

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

const ToolStructuredData = "structured_data"

// StructuredDataTool uses the authenticated caller, independent of a model's arguments
// or a shared agent's execution tenant. No operation writes research state.
type StructuredDataTool struct {
	BaseTool
	endpoint        string
	sourceQuestion  string
	key             []byte
	client          *http.Client
	readCache       structuredReadCache
	knowledgeScopes []structuredKnowledgeScope
	scopeConfigured bool
	scopeError      error
}

func NewStructuredDataTool() *StructuredDataTool {
	key, _ := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	return &StructuredDataTool{BaseTool: NewBaseTool(ToolStructuredData,
		"只读查询本轮授权范围的Excel、字段、序列和原单元格证据。单知识库自动限定，多知识库先读取返回的真实知识库目录，再根据完整问题和上下文用knowledge_base_id选择来源。按需describe_tools获取目标操作schema；发现字段后查询或用analyze_table运行完整只读SQL计算；inspect_source检查原件未解析区域、年度标签和单元格，并可用sums做原值求和。resolve_fields处理语义歧义，confirm_fields须真实用户选择。不会上传、删除或修改原表。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation","arguments"],"properties":{"knowledge_base_id":{"type":"string","description":"可选的真实知识库ID，必须在本轮已授权的知识库目录中；单库时默认使用该库，多库时先读取返回目录再根据完整问题选择"},"operation":{"type":"string","enum":["describe_tools","inspect_source","list_datasets","list_series","list_charts","query_series","compare_series","convert_price","resolve_fields","confirm_fields","execute_field_plan","get_result_evidence","analyze_table"]},"arguments":{"type":"object","description":"先describe_tools {operations:[目标操作]}获取参数schema；其余参数沿用操作schema，不接受账号、空间、URL或凭据"}}}`)),
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
			return nil, fmt.Errorf("结构化表格不接受账号、空间或未声明的参数")
		}
	}
	var operation string
	var arguments map[string]json.RawMessage
	if json.Unmarshal(input["operation"], &operation) != nil || json.Unmarshal(input["arguments"], &arguments) != nil || arguments == nil {
		return nil, fmt.Errorf("结构化表格需要合法 operation 和 arguments 对象")
	}
	allowed := map[string]bool{"describe_tools": true, "inspect_source": true, "list_datasets": true, "list_series": true, "list_charts": true, "query_series": true, "compare_series": true, "convert_price": true, "resolve_fields": true, "confirm_fields": true, "execute_field_plan": true, "get_result_evidence": true, "analyze_table": true}
	if !allowed[operation] {
		return nil, fmt.Errorf("结构化表格不支持此操作")
	}
	for _, field := range []string{"tenant_id", "user_id", "role", "token", "api_key", "url", "endpoint", "data_dir", "knowledge_base_id", "knowledge_base_tenant_id", "knowledge_base_can_write"} {
		if _, exists := arguments[field]; exists {
			return nil, fmt.Errorf("结构化表格参数不接受身份、凭据或服务配置")
		}
	}
	scope, directory, err := t.resolveStructuredKnowledgeScope(input["knowledge_base_id"])
	if err != nil {
		return nil, err
	}
	if err = t.authorizeStructuredAPIKeyScope(ctx, scope, directory != nil); err != nil {
		return nil, err
	}
	if directory != nil {
		return directory, nil
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
		return cached, nil
	}
	var cacheResult *types.ToolResult
	defer func() { t.finishStructuredReadCache(lease, cacheResult) }()
	identity := map[string]any{"user_id": caller.UserID, "tenant_id": caller.TenantID,
		"role": string(caller.Role), "email": "", "display_name": caller.UserID}
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
	req.Header.Set("X-Research-Identity", encoded)
	req.Header.Set("X-Research-Timestamp", stamp)
	req.Header.Set("X-Research-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("结构化表格服务连接失败")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest {
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
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	if err != nil || len(body) > 512*1024 {
		return nil, fmt.Errorf("结构化表格结果过大，请缩小范围")
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil || data == nil {
		return nil, fmt.Errorf("结构化表格结果格式无效")
	}
	if status, _ := data["status"].(string); status == "error" {
		detail, _ := data["error"].(string)
		if detail == "" {
			detail = "结构化表格服务返回查询错误；请按真实反馈修正参数"
		}
		return &types.ToolResult{Success: false, Output: string(body), Data: data, Error: detail}, nil
	}
	cacheResult = &types.ToolResult{Success: true, Output: string(body), Data: data}
	return cacheResult, nil
}
