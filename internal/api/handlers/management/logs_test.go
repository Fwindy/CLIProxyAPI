package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestParseLimitUsesDefaultAndHardCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantLimit int
		wantErr   string
	}{
		{
			name:      "empty uses default limit",
			raw:       "",
			wantLimit: 200,
		},
		{
			name:      "small explicit limit preserved",
			raw:       "50",
			wantLimit: 50,
		},
		{
			name:      "large explicit limit capped",
			raw:       "5000",
			wantLimit: 1000,
		},
		{
			name:    "zero rejected",
			raw:     "0",
			wantErr: "must be greater than zero",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseLimit(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("parseLimit(%q) error = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLimit(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.wantLimit {
				t.Fatalf("parseLimit(%q) = %d, want %d", tc.raw, got, tc.wantLimit)
			}
		})
	}
}

func TestGetLogsAppliesDefaultAndHardCappedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logDir := t.TempDir()
	lines := make([]byte, 0, 32*1024)
	for i := 1; i <= 1200; i++ {
		lines = append(lines, fmt.Sprintf("line-%04d\n", i)...)
	}
	if err := os.WriteFile(filepath.Join(logDir, defaultLogFileName), lines, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	tests := []struct {
		name          string
		rawQuery      string
		wantLines     int
		wantFirstLine string
		wantLastLine  string
	}{
		{
			name:          "default limit when omitted",
			rawQuery:      "",
			wantLines:     200,
			wantFirstLine: "line-1001",
			wantLastLine:  "line-1200",
		},
		{
			name:          "hard cap when explicit limit is too large",
			rawQuery:      "limit=5000",
			wantLines:     1000,
			wantFirstLine: "line-0201",
			wantLastLine:  "line-1200",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{
				cfg: &config.Config{
					LoggingToFile: true,
				},
				logDir: logDir,
			}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			target := "/v0/management/logs"
			if tc.rawQuery != "" {
				target += "?" + tc.rawQuery
			}
			ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)

			h.GetLogs(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var body struct {
				Lines     []string `json:"lines"`
				LineCount int      `json:"line-count"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("json.Unmarshal() error = %v, body=%s", err, rec.Body.String())
			}
			if body.LineCount != 1200 {
				t.Fatalf("line-count = %d, want 1200", body.LineCount)
			}
			if len(body.Lines) != tc.wantLines {
				t.Fatalf("len(lines) = %d, want %d", len(body.Lines), tc.wantLines)
			}
			if body.Lines[0] != tc.wantFirstLine {
				t.Fatalf("first line = %q, want %q", body.Lines[0], tc.wantFirstLine)
			}
			if body.Lines[len(body.Lines)-1] != tc.wantLastLine {
				t.Fatalf("last line = %q, want %q", body.Lines[len(body.Lines)-1], tc.wantLastLine)
			}
		})
	}
}
