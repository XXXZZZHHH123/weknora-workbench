package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func testScopeSignature(key, path, stamp, authorization string, tenant uint64) string {
	digest := sha256.Sum256([]byte(authorization))
	message := strings.Join([]string{"weknora.research-scope.v1", "GET", path, stamp, hex.EncodeToString(digest[:]), strconv.FormatUint(tenant, 10)}, "\n")
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWorkspaceScopeProofBindsKeyTenantPathTokenAndTimestamp(t *testing.T) {
	key := strings.Repeat("k", 48)
	file := filepath.Join(t.TempDir(), "service-token")
	require.NoError(t, os.WriteFile(file, []byte(key+"\n"), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	now := time.Now()
	stamp := strconv.FormatInt(now.Unix(), 10)
	path := "/api/v1/workspaces/research/a-workbench/source-scope"
	authorization := "Bearer synthetic-actor"
	for _, test := range []struct {
		name, key, path, stamp, token string
		tenant                        uint64
		missing, public               bool
	}{
		{name: "internal", key: key, path: path, stamp: stamp, token: authorization, tenant: 7},
		{name: "public", public: true, tenant: 7},
		{name: "wrong-key", key: strings.Repeat("z", 48), path: path, stamp: stamp, token: authorization, tenant: 7},
		{name: "wrong-path", key: key, path: path + "-other", stamp: stamp, token: authorization, tenant: 7},
		{name: "wrong-token", key: key, path: path, stamp: stamp, token: authorization + "-other", tenant: 7},
		{name: "wrong-tenant", key: key, path: path, stamp: stamp, token: authorization, tenant: 8},
		{name: "expired", key: key, path: path, stamp: strconv.FormatInt(now.Unix()-31, 10), token: authorization, tenant: 7},
		{name: "future", key: key, path: path, stamp: strconv.FormatInt(now.Unix()+31, 10), token: authorization, tenant: 7},
		{name: "missing-header", key: key, path: path, stamp: stamp, token: authorization, tenant: 7, missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			request := httptest.NewRequest("GET", path, nil)
			request.Header.Set("Authorization", authorization)
			request = request.WithContext(types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actor", Role: types.TenantRoleViewer}))
			if !test.public {
				request.Header.Set("X-Research-Scope-Timestamp", test.stamp)
				if !test.missing {
					request.Header.Set("X-Research-Scope-Signature", testScopeSignature(test.key, test.path, test.stamp, test.token, test.tenant))
				}
			}
			c.Request = request
			internal, err := workspaceScopeInventoryProof(c, now)
			if test.name == "internal" {
				require.NoError(t, err)
				require.True(t, internal)
			} else if test.public {
				require.NoError(t, err)
				require.False(t, internal)
			} else {
				require.Error(t, err)
				require.False(t, internal)
			}
		})
	}
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-Research-Scope-Timestamp", stamp)
	request.Header.Set("X-Research-Scope-Signature", testScopeSignature(key, path, stamp, authorization, 7))
	c.Request = request.WithContext(types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "actor", Role: types.TenantRoleViewer}))
	internal, err := workspaceScopeInventoryProof(c, now)
	require.Error(t, err)
	require.False(t, internal)
}

func TestWorkspacePublicSourceScopeRedactsDelegatedOriginalIDsAndHashes(t *testing.T) {
	f := newWorkspaceDiagramFixture(t)
	f.kbs.private = true
	f.user = "reader"
	f.role = types.TenantRoleViewer
	require.NoError(t, f.db.Model(&types.ResearchWorkbench{}).Where("id=?", f.workbenchID).Updates(map[string]any{"access_policy": `{"space_permission":"viewer","members":[]}`, "source_sharing_approved": true, "source_sharing_approved_by": "source-owner", "source_sharing_modules": `["diagram"]`}).Error)
	secretID, secretHash := "private-original-id", strings.Repeat("b", 64)
	require.NoError(t, f.db.Create(&types.Knowledge{ID: secretID, KnowledgeBaseID: "kb", TenantID: 1, FileType: "xlsx", FileHash: secretHash}).Error)
	f.router.GET("/api/v1/workspaces/research/:id/source-scope", NewWorkspaceHandler(f.service).GetWorkbenchSourceScope)
	path := "/api/v1/workspaces/research/" + f.workbenchID + "/source-scope"
	authorization := "Bearer synthetic-viewer"
	request := func(internal bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", authorization)
		if internal {
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set("X-Research-Scope-Timestamp", stamp)
			req.Header.Set("X-Research-Scope-Signature", testScopeSignature(strings.Repeat("k", 48), path, stamp, authorization, 1))
		}
		recorder := httptest.NewRecorder()
		f.router.ServeHTTP(recorder, req)
		return recorder
	}
	public := request(false)
	require.Equal(t, 200, public.Code, public.Body.String())
	require.NotContains(t, public.Body.String(), secretID)
	require.NotContains(t, public.Body.String(), secretHash)
	var document struct {
		Data struct {
			InventoryIncluded bool `json:"inventory_included"`
		}
	}
	require.NoError(t, json.Unmarshal(public.Body.Bytes(), &document))
	require.False(t, document.Data.InventoryIncluded)
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("k", 48)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", file)
	internal := request(true)
	require.Equal(t, 200, internal.Code, internal.Body.String())
	require.Contains(t, internal.Body.String(), secretID)
	require.Contains(t, internal.Body.String(), secretHash)
	require.NoError(t, json.Unmarshal(internal.Body.Bytes(), &document))
	require.True(t, document.Data.InventoryIncluded)
}
