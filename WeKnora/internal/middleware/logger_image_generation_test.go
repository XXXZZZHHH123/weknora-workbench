package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestOmitBodyLoggingKeepsAccessMetadataAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, omit := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "image preview"}[omit], func(t *testing.T) {
			var logs bytes.Buffer
			localLogger := logrus.New()
			localLogger.SetOutput(&logs)
			localLogger.SetFormatter(&logrus.JSONFormatter{})
			router := gin.New()
			router.ContextWithFallback = true
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.LoggerContextKey, logrus.NewEntry(localLogger)))
				c.Next()
			}, Logger())
			router.POST("/test", func(c *gin.Context) {
				var req map[string]any
				require.NoError(t, c.ShouldBindJSON(&req))
				if omit {
					OmitBodyLogging(c)
				}
				c.JSON(http.StatusOK, gin.H{"image": gin.H{"data_url": "data:image/png;base64,private-image-bytes"}})
				if omit {
					writer, ok := c.Writer.(*loggerResponseBodyWriter)
					require.True(t, ok)
					require.Zero(t, writer.body.Len())
				}
			})
			request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"apiKey":"secret-api-key","customHeaders":{"X-Model-Secret":"private-header"}}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "private-image-bytes")
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			require.Equal(t, "/test", entry["path"])
			require.Equal(t, float64(200), entry["status_code"])
			if omit {
				require.NotContains(t, entry, "request_body")
				require.NotContains(t, entry, "response_body")
				require.NotContains(t, logs.String(), "secret-api-key")
				require.NotContains(t, logs.String(), "private-header")
				require.NotContains(t, logs.String(), "private-image-bytes")
			} else {
				require.Contains(t, entry, "request_body")
				require.Contains(t, entry, "response_body")
			}
		})
	}
}
