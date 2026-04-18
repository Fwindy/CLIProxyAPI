package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestResponsesLogsClientUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	restore := captureOpenAIStandardLogger(t, &buf)
	defer restore()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	body := strings.NewReader(`{"model":"unknown-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex-cli/0.121.0")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	out := buf.String()
	for _, want := range []string{
		"responses request received",
		"\"user_agent\":\"codex-cli/0.121.0\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("responses log output missing %q: %s", want, out)
		}
	}
}
