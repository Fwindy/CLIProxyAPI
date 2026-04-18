package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractResponseBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	wrapper.body.WriteString("original-response")

	body := wrapper.extractResponseBody(c)
	if string(body) != "original-response" {
		t.Fatalf("response body = %q, want %q", string(body), "original-response")
	}

	c.Set(responseBodyOverrideContextKey, []byte("override-response"))
	body = wrapper.extractResponseBody(c)
	if string(body) != "override-response" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response")
	}

	body[0] = 'X'
	if got := wrapper.extractResponseBody(c); string(got) != "override-response" {
		t.Fatalf("response override should be cloned, got %q", string(got))
	}
}

func TestExtractResponseBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(responseBodyOverrideContextKey, "override-response-as-string")

	body := wrapper.extractResponseBody(c)
	if string(body) != "override-response-as-string" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response-as-string")
	}
}

func TestExtractBodyOverrideClonesBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	override := []byte("body-override")
	c.Set(requestBodyOverrideContextKey, override)

	body := extractBodyOverride(c, requestBodyOverrideContextKey)
	if !bytes.Equal(body, override) {
		t.Fatalf("body override = %q, want %q", string(body), string(override))
	}

	body[0] = 'X'
	if !bytes.Equal(override, []byte("body-override")) {
		t.Fatalf("override mutated: %q", string(override))
	}
}

func TestExtractWebsocketTimelineUsesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	if got := wrapper.extractWebsocketTimeline(c); got != nil {
		t.Fatalf("expected nil websocket timeline, got %q", string(got))
	}

	c.Set(websocketTimelineOverrideContextKey, []byte("timeline"))
	body := wrapper.extractWebsocketTimeline(c)
	if string(body) != "timeline" {
		t.Fatalf("websocket timeline = %q, want %q", string(body), "timeline")
	}
}

func TestFinalizeStreamingWritesAPIWebsocketTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	streamWriter := &testStreamingLogWriter{}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         &testRequestLogger{enabled: true},
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-1",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:  true,
		streamWriter: streamWriter,
	}

	c.Set("API_WEBSOCKET_TIMELINE", []byte("Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if string(streamWriter.apiWebsocketTimeline) != "Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}" {
		t.Fatalf("stream writer websocket timeline = %q", string(streamWriter.apiWebsocketTimeline))
	}
	if !streamWriter.closed {
		t.Fatal("expected stream writer to be closed")
	}
}

func TestFinalizeForceLogsAPIResponseErrorWhenLoggerDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logging.NewFileRequestLogger(false, logDir, "", 10),
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			Body:      []byte(`{"model":"codex-mini-latest","stream":true}`),
			RequestID: "req-response-error",
			Timestamp: time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC),
		},
		logOnErrorOnly: true,
		isStreaming:    true,
	}
	c.Writer = wrapper

	ctx := context.WithValue(context.Background(), "gin", c)
	helps.RecordAPIResponseError(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, errors.New("upstream stream disconnected"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}

	logPath := singleErrorLogPath(t, logDir)
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "=== API RESPONSE ===") {
		t.Fatalf("log missing API response section: %s", string(content))
	}
	if !strings.Contains(string(content), "Error: upstream stream disconnected") {
		t.Fatalf("log missing upstream error: %s", string(content))
	}
}

func TestFinalizeForceLogsAPIWebsocketErrorWhenLoggerDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logging.NewFileRequestLogger(false, logDir, "", 10),
		requestInfo: &RequestInfo{
			URL:    "/v1/responses",
			Method: "GET",
			Headers: map[string][]string{
				"Connection": {"Upgrade"},
				"Upgrade":    {"websocket"},
			},
			RequestID: "req-websocket-error",
			Timestamp: time.Date(2026, time.April, 14, 12, 1, 0, 0, time.UTC),
		},
		logOnErrorOnly: true,
		isStreaming:    true,
	}
	c.Writer = wrapper

	ctx := context.WithValue(context.Background(), "gin", c)
	helps.RecordAPIWebsocketError(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, "read", errors.New("upstream websocket closed"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}

	logPath := singleErrorLogPath(t, logDir)
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "=== API WEBSOCKET TIMELINE ===") {
		t.Fatalf("log missing websocket timeline: %s", string(content))
	}
	if !strings.Contains(string(content), "Event: api.websocket.error") {
		t.Fatalf("log missing websocket error event: %s", string(content))
	}
	if !strings.Contains(string(content), "Error: upstream websocket closed") {
		t.Fatalf("log missing websocket error detail: %s", string(content))
	}
}

func singleErrorLogPath(t *testing.T, logDir string) string {
	t.Helper()

	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}

	var errorLogs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			errorLogs = append(errorLogs, filepath.Join(logDir, entry.Name()))
		}
	}
	if len(errorLogs) != 1 {
		t.Fatalf("error log count = %d, want 1", len(errorLogs))
	}
	return errorLogs[0]
}

type testRequestLogger struct {
	enabled bool
}

func (l *testRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	return nil
}

func (l *testRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *testRequestLogger) IsEnabled() bool {
	return l.enabled
}

type testStreamingLogWriter struct {
	apiWebsocketTimeline []byte
	closed               bool
}

func (w *testStreamingLogWriter) WriteChunkAsync([]byte) {}

func (w *testStreamingLogWriter) WriteStatus(int, map[string][]string) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIRequest([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponse([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *testStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *testStreamingLogWriter) Close() error {
	w.closed = true
	return nil
}
