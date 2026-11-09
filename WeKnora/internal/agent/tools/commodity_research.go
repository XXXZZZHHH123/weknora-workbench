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

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

const ToolCommodityResearch = "commodity_research"

// CommodityResearchTool uses the authenticated caller, independent of a model's arguments
// or a shared agent's execution tenant. No operation writes research state.
type CommodityResearchTool struct {
	BaseTool
	endpoint string
	key      []byte
	client   *http.Client
}

func NewCommodityResearchTool() *CommodityResearchTool {
	key, _ := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	return &CommodityResearchTool{BaseTool: NewBaseTool(ToolCommodityResearch,
		"只读查询本会话已由服务器绑定的研究工作台及当前账号的判断、信号、简报、策略。需要真实已验证的工作台来源范围；普通未绑定会话不可使用，不能自行指定或猜测工作台。先检索真实指标再读取序列；所有数字、日期、口径和草稿状态以返回事实为准。缺失结果不能编造，也不能通过本工具发布判断或记录成交。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["summary","indicators","series","judgment","signals","brief","strategies"]},"date":{"type":"string","description":"YYYY-MM-DD；省略时读取最新已存行情日期"},"series_id":{"type":"string","description":"真实指标ID，series操作必填"},"search":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200},"offset":{"type":"integer","minimum":0,"maximum":100000}}}`)),
		endpoint: strings.TrimRight(os.Getenv("COMMODITY_RESEARCH_URL"), "/"),
		key:      bytes.TrimSpace(key), client: &http.Client{Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// CommodityResearchScopeAvailable checks runtime prerequisites, not model
// arguments or a saved allowlist. It grants neither a workspace nor access.
func CommodityResearchScopeAvailable(ctx context.Context) bool {
	caller := types.CallerFromContext(ctx)
	scope, ok := types.ResearchWorkbenchScopeFromContext(ctx)
	return ok && caller.TenantID != 0 && caller.UserID != "" && caller.Role.IsValid() &&
		validCommodityResearchScope(scope, caller) && types.ResearchAuthorizationFromContext(ctx) != ""
}

func validCommodityResearchScope(scope types.ResearchWorkbenchScope, caller types.Caller) bool {
	_, err := uuid.Parse(scope.ID)
	return err == nil && scope.TenantID == caller.TenantID && scope.UserID == caller.UserID &&
		(scope.ScopeVersion == 2 && scope.SourcesRevision >= 1 ||
			scope.ScopeVersion != 2 && scope.KnowledgeBaseID != "" && scope.KnowledgeBaseTenantID != 0)
}

func (t *CommodityResearchTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() {
		return nil, fmt.Errorf("商品研究读取需要已核验的用户和空间")
	}
	scope, scoped := types.ResearchWorkbenchScopeFromContext(ctx)
	if !scoped {
		message := "本会话未绑定已验证的研究工作台，商品研究工具未执行。请在实际已授权的研究工作台会话使用；普通文档或原表问题可选择本轮已开放的文档/结构化工具，不能猜测工作台或读取历史空间数据。"
		data := map[string]any{"kind": "research_tool_scope", "status": "error", "reason_code": "scope_required", "request_started": false, "notice": message}
		output, _ := json.Marshal(data)
		return &types.ToolResult{Success: false, Error: message, Data: data, Output: string(output)}, nil
	}
	if !validCommodityResearchScope(scope, caller) {
		return nil, fmt.Errorf("研究工作台作用域无效，拒绝回退到空间数据")
	}
	if len(t.key) < 32 || t.endpoint == "" {
		return nil, fmt.Errorf("商品研究服务尚未配置")
	}
	parsed, err := url.Parse(t.endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, fmt.Errorf("商品研究服务地址配置无效")
	}
	var input map[string]json.RawMessage
	if len(args) > 16384 || json.Unmarshal(args, &input) != nil || input == nil {
		return nil, fmt.Errorf("商品研究参数格式无效")
	}
	for field := range input {
		if field != "operation" && field != "date" && field != "series_id" && field != "search" && field != "limit" && field != "offset" {
			return nil, fmt.Errorf("商品研究不接受账号、空间或未声明的参数")
		}
	}
	identity := map[string]any{"user_id": caller.UserID, "tenant_id": caller.TenantID,
		"role": string(caller.Role), "email": "", "display_name": caller.UserID}
	if scoped {
		identity["workbench_id"] = scope.ID
		if scope.ScopeVersion == 2 {
			identity["scope_version"] = 2
			identity["sources_revision"] = scope.SourcesRevision
		}
		identity["knowledge_base_id"] = scope.KnowledgeBaseID
		identity["knowledge_base_tenant_id"] = scope.KnowledgeBaseTenantID
		authorization := types.ResearchAuthorizationFromContext(ctx)
		if authorization == "" {
			return nil, fmt.Errorf("研究事实读取需要当前登录核验，不能回退历史研究库")
		}
		authorizationHash := sha256.Sum256([]byte(authorization))
		identity["authorization_sha256"] = hex.EncodeToString(authorizationHash[:])
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
		return nil, fmt.Errorf("商品研究请求构造失败")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Research-Identity", encoded)
	req.Header.Set("X-Research-Timestamp", stamp)
	req.Header.Set("X-Research-Signature", hex.EncodeToString(mac.Sum(nil)))
	if scoped {
		req.Header.Set("X-Research-Authorization", types.ResearchAuthorizationFromContext(ctx))
	}
	response, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("商品研究服务连接失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return readServiceFailure("商品研究", response.StatusCode, response.Body), nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	if err != nil || len(body) > 512*1024 {
		return nil, fmt.Errorf("商品研究结果过大，请缩小范围")
	}
	var data map[string]any
	if json.Unmarshal(body, &data) != nil || data == nil {
		return nil, fmt.Errorf("商品研究结果格式无效")
	}
	return &types.ToolResult{Success: true, Output: string(body), Data: data}, nil
}
