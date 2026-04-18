package synthesizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestSynthesizeAuthFile_RestoresPersistedRuntimeState(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)

	nextRetry := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{
		ID:             "codex-a.json",
		FileName:       "codex-a.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "usage_limit_reached",
		Unavailable:    true,
		NextRetryAfter: nextRetry,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRetry,
			BackoffLevel:  2,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5": {
				Status:         coreauth.StatusError,
				StatusMessage:  "usage_limit_reached",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				LastError: &coreauth.Error{
					Code:       "rate_limit",
					Message:    "usage_limit_reached",
					HTTPStatus: 429,
				},
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
					BackoffLevel:  2,
				},
				UpdatedAt: nextRetry.Add(-time.Minute),
			},
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "codex@example.com",
		},
	}

	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}

	synthesized := SynthesizeAuthFile(&SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     authDir,
		Now:         nextRetry.Add(-2 * time.Hour),
		IDGenerator: NewStableIDGenerator(),
	}, filepath.Join(authDir, "codex-a.json"), data)
	if len(synthesized) != 1 {
		t.Fatalf("len(SynthesizeAuthFile()) = %d, want 1", len(synthesized))
	}

	got := synthesized[0]
	if !got.Unavailable {
		t.Fatalf("Unavailable = false, want true")
	}
	if !got.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("NextRetryAfter = %v, want %v", got.NextRetryAfter, nextRetry)
	}
	state := got.ModelStates["gpt-5"]
	if state == nil {
		t.Fatalf("ModelStates[gpt-5] = nil")
	}
	if !state.Unavailable {
		t.Fatalf("ModelStates[gpt-5].Unavailable = false, want true")
	}
	if !state.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("ModelStates[gpt-5].NextRetryAfter = %v, want %v", state.NextRetryAfter, nextRetry)
	}
	if state.LastError == nil || state.LastError.HTTPStatus != 429 {
		t.Fatalf("ModelStates[gpt-5].LastError = %#v, want HTTPStatus 429", state.LastError)
	}
}
