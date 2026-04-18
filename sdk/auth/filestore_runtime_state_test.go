package auth

import (
	"context"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestFileTokenStore_SaveAndList_PreservesRuntimeState(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	store.SetBaseDir(t.TempDir())

	nextRetry := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	modelState := &cliproxyauth.ModelState{
		Status:         cliproxyauth.StatusError,
		StatusMessage:  "usage_limit_reached",
		Unavailable:    true,
		NextRetryAfter: nextRetry,
		LastError: &cliproxyauth.Error{
			Code:       "rate_limit",
			Message:    "usage_limit_reached",
			HTTPStatus: 429,
		},
		Quota: cliproxyauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRetry,
			BackoffLevel:  2,
		},
		UpdatedAt: nextRetry.Add(-time.Minute),
	}
	auth := &cliproxyauth.Auth{
		ID:             "codex-a.json",
		FileName:       "codex-a.json",
		Provider:       "codex",
		Status:         cliproxyauth.StatusError,
		StatusMessage:  "usage_limit_reached",
		Unavailable:    true,
		NextRetryAfter: nextRetry,
		Quota: cliproxyauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRetry,
			BackoffLevel:  2,
		},
		LastError: &cliproxyauth.Error{
			Code:       "rate_limit",
			Message:    "usage_limit_reached",
			HTTPStatus: 429,
		},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gpt-5": modelState,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "codex@example.com",
		},
	}

	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("len(List()) = %d, want 1", len(auths))
	}

	got := auths[0]
	if !got.Unavailable {
		t.Fatalf("Unavailable = false, want true")
	}
	if !got.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("NextRetryAfter = %v, want %v", got.NextRetryAfter, nextRetry)
	}
	if !got.Quota.Exceeded {
		t.Fatalf("Quota.Exceeded = false, want true")
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
	if !state.Quota.Exceeded {
		t.Fatalf("ModelStates[gpt-5].Quota.Exceeded = false, want true")
	}
	if state.LastError == nil || state.LastError.HTTPStatus != 429 {
		t.Fatalf("ModelStates[gpt-5].LastError = %#v, want HTTPStatus 429", state.LastError)
	}
}
