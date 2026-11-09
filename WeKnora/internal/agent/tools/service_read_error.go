package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const serviceReadErrorLimit = 16 * 1024

var serviceReadErrorCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,95}$`)

// Only the fixed, authenticated service destination supplies these details.
// Copy declared diagnostic fields, never an arbitrary body, stack, credential,
// returned source row or HTML proxy response into the model/user output.
func readServiceFailure(service string, status int, reader io.Reader) *types.ToolResult {
	body, err := io.ReadAll(io.LimitReader(reader, serviceReadErrorLimit+1))
	return serviceReadFailure(service, status, body, err)
}

func serviceReadFailure(service string, status int, body []byte, readErr error) *types.ToolResult {
	code, detail, detailStatus := "", "", "unavailable"
	if readErr == nil && len(body) <= serviceReadErrorLimit {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(body, &envelope) == nil && envelope != nil {
			detailStatus = "omitted"
			_ = json.Unmarshal(envelope["code"], &code)
			_ = json.Unmarshal(envelope["message"], &detail)
			for _, field := range []string{"error", "detail"} {
				var text string
				if json.Unmarshal(envelope[field], &text) == nil && strings.TrimSpace(text) != "" {
					detail = text
					continue
				}
				var nested struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(envelope[field], &nested) == nil {
					if nested.Code != "" {
						code = nested.Code
					}
					if nested.Message != "" {
						detail = nested.Message
					}
				}
			}
			if strings.TrimSpace(detail) != "" {
				detailStatus = "provided"
			}
		} else {
			detailStatus = "invalid_json"
		}
	} else if len(body) > serviceReadErrorLimit {
		detailStatus = "too_large"
	}
	if !serviceReadErrorCode.MatchString(code) {
		code = ""
	}
	detail = strings.TrimSpace(detail)
	if len([]rune(detail)) > 2048 {
		detail = string([]rune(detail)[:2048]) + "…"
	}
	reason := fmt.Sprintf("http_%d", status)
	if code != "" {
		reason = code
	}
	message := fmt.Sprintf("%s读取失败（HTTP %d），本次未取得可用结果", service, status)
	if detail != "" {
		message += "：" + detail
	}
	data := map[string]any{"kind": "service_read_error", "status": "error", "reason_code": reason,
		"http_status": status, "service": service, "detail_status": detailStatus,
		"notice": "此返回是服务拒绝或读取失败，不是来源事实或无数据证明。按具体错误核对当前来源、权限、版本或参数；不能绕过保护、替换旧版本或仅凭状态码自动重放。"}
	if detail != "" {
		data["detail"] = detail
	}
	if code != "" {
		data["service_code"] = code
	}
	output, _ := json.Marshal(data)
	return &types.ToolResult{Success: false, Error: message, Data: data, Output: string(output)}
}
