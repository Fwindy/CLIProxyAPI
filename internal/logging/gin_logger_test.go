package logging

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusRecoveryRepanicsErrAbortHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/abort", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	recorder := httptest.NewRecorder()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic, got nil")
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("expected error panic, got %T", recovered)
		}
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("expected ErrAbortHandler, got %v", err)
		}
		if err != http.ErrAbortHandler {
			t.Fatalf("expected exact ErrAbortHandler sentinel, got %v", err)
		}
	}()

	engine.ServeHTTP(recorder, req)
}

func TestGinLogrusRecoveryHandlesRegularPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestGinLogrusLoggerIncludesClientMetadataInAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		body        string
		wantLogPart string
	}{
		{
			name:        "instructions starts with codex prefix",
			body:        `{"instructions":"You are Codex. Hello"}`,
			wantLogPart: `instructions_codex=true`,
		},
		{
			name:        "instructions without codex prefix",
			body:        `{"instructions":"You are Claude"}`,
			wantLogPart: `instructions_codex=false`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := captureGinLoggerOutput(t, &buf)
			defer restore()

			engine := gin.New()
			engine.Use(GinLogrusLogger())
			engine.POST("/v1/responses", func(c *gin.Context) {
				body, err := c.GetRawData()
				if err != nil {
					t.Fatalf("GetRawData: %v", err)
				}
				if string(body) != tc.body {
					t.Fatalf("handler body = %q, want %q", string(body), tc.body)
				}
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "codex-cli/0.121.0")
			req.Header.Set("originator", "codex_cli_rs")
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", recorder.Code)
			}

			out := buf.String()
			for _, want := range []string{
				`ua="codex-cli/0.121.0"`,
				`originator="codex_cli_rs"`,
				tc.wantLogPart,
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("expected access log to include %s, got: %s", want, out)
				}
			}
		})
	}
}

func captureGinLoggerOutput(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()

	logger := log.StandardLogger()
	prevOut := logger.Out
	prevFormatter := logger.Formatter
	prevLevel := logger.Level
	prevReportCaller := logger.ReportCaller

	logger.SetOutput(io.Writer(buf))
	logger.SetFormatter(&LogFormatter{})
	logger.SetLevel(log.InfoLevel)
	logger.SetReportCaller(true)

	return func() {
		logger.SetOutput(prevOut)
		logger.SetFormatter(prevFormatter)
		logger.SetLevel(prevLevel)
		logger.SetReportCaller(prevReportCaller)
	}
}

func TestIsAIAPIPathIncludesImages(t *testing.T) {
	if !isAIAPIPath("/v1/images/generations") {
		t.Fatalf("expected /v1/images/generations to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/images/edits") {
		t.Fatalf("expected /v1/images/edits to be treated as AI API path")
	}
}
