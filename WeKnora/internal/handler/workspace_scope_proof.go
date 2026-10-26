package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// A browser's workbench grant is not permission to enumerate private originals.
// Only the existing service secret can admit the internal facts inventory;
// the end-user token and resource permission are still rechecked by the service.
func workspaceScopeInventoryProof(c *gin.Context, now time.Time) (bool, error) {
	stamp, signature := c.GetHeader("X-Research-Scope-Timestamp"), c.GetHeader("X-Research-Scope-Signature")
	if stamp == "" && signature == "" {
		return false, nil
	}
	deny := func() (bool, error) {
		return false, apperrors.NewForbiddenError("研究来源清单需要有效的内部服务证明")
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || seconds <= 0 || strconv.FormatInt(seconds, 10) != stamp || seconds < now.Unix()-30 || seconds > now.Unix()+30 || c.Request.Method != "GET" || c.Request.URL.RawQuery != "" {
		return deny()
	}
	authorization := c.GetHeader("Authorization")
	caller := types.CallerFromContext(c.Request.Context())
	if !strings.HasPrefix(authorization, "Bearer ") || caller.TenantID == 0 || caller.UserID == "" {
		return deny()
	}
	key, err := os.ReadFile(os.Getenv("COMMODITY_RESEARCH_TOKEN_FILE"))
	key = bytes.TrimSpace(key)
	if err != nil || len(key) < 32 || len(key) > 1024 {
		return deny()
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return deny()
	}
	authHash := sha256.Sum256([]byte(authorization))
	message := strings.Join([]string{"weknora.research-scope.v1", "GET", c.Request.URL.Path, stamp, hex.EncodeToString(authHash[:]), strconv.FormatUint(caller.TenantID, 10)}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return deny()
	}
	return true, nil
}
