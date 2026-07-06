package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestGeminiExecutorRecordsSuccessfulZeroUsageInStatistics(t *testing.T) {
	model := fmt.Sprintf("gemini-2.5-flash-zero-usage-%d", time.Now().UnixNano())
	// For an API-key auth the usage source resolves to the API key (via Auth.AccountInfo),
	// so drive and look up statistics by that key rather than an email.
	source := fmt.Sprintf("zero-usage-key-%d", time.Now().UnixNano())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/" + model + ":generateContent"
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`))
	}))
	defer server.Close()

	store, err := internalusage.NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restoreStore := internalusage.SetDefaultStoreForTest(store)
	t.Cleanup(restoreStore)

	executor := runtimeexecutor.NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  source,
			"base_url": server.URL,
		},
	}

	prevStatsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(prevStatsEnabled)
	})

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	detail := waitForStatisticsDetail(t, store, "gemini", model, source)
	if detail.ID == "" {
		t.Fatalf("detail ID is empty")
	}
	if detail.LatencyMs < 0 || detail.FirstByteLatencyMs < 0 || detail.GenerationMs < 0 {
		t.Fatalf("latency fields must be non-negative: latency=%d first_byte=%d generation=%d", detail.LatencyMs, detail.FirstByteLatencyMs, detail.GenerationMs)
	}
	if detail.Failed {
		t.Fatalf("detail failed = true, want false")
	}
	if detail.Tokens.TotalTokens != 0 {
		t.Fatalf("total tokens = %d, want 0", detail.Tokens.TotalTokens)
	}
}

func waitForStatisticsDetail(t *testing.T, store internalusage.Store, apiName, model, source string) internalusage.RequestDetail {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		usageByAPI, err := store.Query(context.Background(), internalusage.QueryRange{})
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		for _, detail := range usageByAPI[apiName][model] {
			if detail.Source == source {
				return detail
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for statistics detail for api=%q model=%q source=%q", apiName, model, source)
	return internalusage.RequestDetail{}
}
