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
		"只读查询当前空间商品研究数据和当前账号的判断、信号、简报、策略。先检索真实指标再读取序列；所有数字、日期、口径和草稿状态以返回事实为准。缺失结果不能编造，也不能通过本工具发布判断或记录成交。",
		json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operation"],"properties":{"operation":{"type":"string","enum":["summary","indicators","series","judgment","signals","brief","strategies"]},"date":{"type":"string","description":"YYYY-MM-DD；省略时读取最新已存行情日期"},"series_id":{"type":"string","description":"真实指标ID，series操作必填"},"search":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200},"offset":{"type":"integer","minimum":0,"maximum":100000}}}`)),
		endpoint: strings.TrimRight(os.Getenv("COMMODITY_RESEARCH_URL"), "/"),
		key:      bytes.TrimSpace(key), client: &http.Client{Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (t *CommodityResearchTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.IsValid() {
		return nil, fmt.Errorf("商品研究读取需要已核验的用户和空间")
	}
	scope, scoped := types.ResearchWorkbenchScopeFromContext(ctx)
	if scoped {
		if _, err := uuid.Parse(scope.ID); err != nil || scope.TenantID != caller.TenantID ||
			scope.UserID != caller.UserID || scope.KnowledgeBaseID == "" || scope.KnowledgeBaseTenantID == 0 {
			return nil, fmt.Errorf("研究工作台作用域无效，拒绝回退到空间数据")
		}
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
		identity["knowledge_base_id"] = scope.KnowledgeBaseID
		identity["knowledge_base_tenant_id"] = scope.KnowledgeBaseTenantID
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
	response, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("商品研究服务连接失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("商品研究读取失败（HTTP %d），请检查服务、身份或查询参数", response.StatusCode)
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
