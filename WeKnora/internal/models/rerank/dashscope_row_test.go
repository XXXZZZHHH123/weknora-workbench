package rerank

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/api/dashscoperank"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type dashscopeTestTransport func(*http.Request) (*http.Response, error)

func (f dashscopeTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDashScopeRerankFromSQLText(t *testing.T) {
	const base = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"
	const params = `{"provider":"aliyun","base_url":"` + base + `","api_key":"synthetic-key"}`
	withRerankSSRFWhitelist(t, "dashscope.aliyuncs.com")
	for _, model := range []string{"qwen3.7-text-rerank", "qwen3-rerank"} {
		t.Run(model, func(t *testing.T) {
			row := &types.Model{ID: "test-model", Name: model, Type: types.ModelTypeRerank, Source: types.ModelSourceRemote}
			require.NoError(t, row.Parameters.Scan(params))
			cfg := ConfigFromModel(row, "", "")
			r, err := NewReranker(cfg)
			require.NoError(t, err)
			require.Equal(t, model, r.GetModelName())

			resolved, err := modelruntime.Resolve(modelruntime.Ref{Provider: cfg.Provider, BaseURL: cfg.BaseURL, Model: cfg.ModelName, ModelType: row.Type})
			require.NoError(t, err)
			require.Equal(t, api.RerankDashScope, resolved.RerankAPI)
			require.Equal(t, model == "qwen3-rerank", resolved.Cataloged)
			endpoint, err := resolved.Endpoint(row.Type, modelruntime.Connection{
				Credentials: api.Credentials{APIKey: cfg.APIKey},
				Client: &http.Client{Transport: dashscopeTestTransport(func(req *http.Request) (*http.Response, error) {
					require.Equal(t, http.MethodPost, req.Method)
					require.Equal(t, base, req.URL.String())
					require.Equal(t, "Bearer synthetic-key", req.Header.Get("Authorization"))
					var body struct {
						Model string
						Input struct {
							Query     string
							Documents []string
						}
						Parameters struct {
							TopN int `json:"top_n"`
						}
					}
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					require.Equal(t, model, body.Model)
					require.Equal(t, "设备状态", body.Input.Query)
					require.Equal(t, []string{"报告甲", "报告乙"}, body.Input.Documents)
					require.Equal(t, len(body.Input.Documents), body.Parameters.TopN)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
						`{"output":{"results":[{"index":1,"relevance_score":0.9}]}}`))}, nil
				})},
			})
			require.NoError(t, err)
			client := dashscoperank.New(dashscoperank.Config{Endpoint: endpoint, Settings: resolved.Rerank})
			ranks, err := client.Rerank(t.Context(), "设备状态", []string{"报告甲", "报告乙"})
			require.NoError(t, err)
			require.Equal(t, []api.RerankResult{{Index: 1, Score: 0.9}}, ranks)
		})
	}
}

func TestRerankRequiresAbsoluteServiceAddress(t *testing.T) {
	for _, address := range []string{"", "/rerank", "https:///rerank"} {
		_, err := NewReranker(&RerankerConfig{ModelName: "synthetic-rerank", Source: types.ModelSourceRemote, BaseURL: address})
		require.ErrorContains(t, err, "服务地址无效")
	}
}
