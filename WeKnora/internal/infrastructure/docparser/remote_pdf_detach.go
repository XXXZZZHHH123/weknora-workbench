package docparser

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DetachRemotePDF releases only the durable task's consumer lease after an
// authorized cancellation, replacement or deletion. Other consumers keep theirs.
func DetachRemotePDF(ctx context.Context, ref *types.RemotePDFJobReference) {
	if ref == nil || ref.ID == "" || ref.LeaseID == "" || strings.ContainsAny(ref.ID+ref.LeaseID, "/\\?#\r\n") {
		return
	}
	tenant, ok := types.TenantIDFromContext(ctx)
	if !ok || tenant == 0 {
		return
	}
	endpoint := strings.TrimRight(os.Getenv("WEKNORA_REMOTE_PDF_URL"), "/")
	token := os.Getenv("WEKNORA_REMOTE_PDF_TOKEN")
	if endpoint == "" || token == "" || validateMinerUOutboundURL(endpoint) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint+"/v1/jobs/"+url.PathEscape(ref.ID), nil)
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Parse-Scope", "tenant:"+strconv.FormatUint(tenant, 10))
	request.Header.Set("X-Parse-Lease", ref.LeaseID)
	client := utils.NewSSRFSafeHTTPClient(utils.SSRFSafeHTTPClientConfig{Timeout: 5 * time.Second, MaxRedirects: 0})
	if response, err := client.Do(request); err == nil {
		response.Body.Close()
	}
}
