package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManager_ReconcileRegistryModelStates_PreservesCooldownForSupportedModel(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &FillFirstSelector{}, nil)

	nextRecoverAt := time.Now().Add(24 * time.Hour).UTC()
	badAuth := &Auth{
		ID:             "no-quote.json",
		Provider:       "codex",
		Status:         StatusError,
		StatusMessage:  "usage_limit_reached",
		Unavailable:    true,
		NextRetryAfter: nextRecoverAt,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRecoverAt,
		},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Status:         StatusError,
				StatusMessage:  "usage_limit_reached",
				Unavailable:    true,
				NextRetryAfter: nextRecoverAt,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRecoverAt,
				},
				UpdatedAt: time.Now().UTC(),
			},
		},
	}
	goodAuth := &Auth{
		ID:       "good.json",
		Provider: "codex",
		Status:   StatusActive,
	}

	if _, err := manager.Register(ctx, badAuth); err != nil {
		t.Fatalf("Register(badAuth) error = %v", err)
	}
	if _, err := manager.Register(ctx, goodAuth); err != nil {
		t.Fatalf("Register(goodAuth) error = %v", err)
	}

	registerSchedulerModels(t, "codex", "gpt-5.4", badAuth.ID, goodAuth.ID)

	manager.ReconcileRegistryModelStates(ctx, badAuth.ID)

	updatedBadAuth, ok := manager.GetByID(badAuth.ID)
	if !ok || updatedBadAuth == nil {
		t.Fatalf("GetByID(%q) returned nil", badAuth.ID)
	}
	if !updatedBadAuth.Quota.Exceeded {
		t.Fatalf("updatedBadAuth.Quota.Exceeded = false, want true")
	}
	if updatedBadAuth.Quota.NextRecoverAt.IsZero() {
		t.Fatal("updatedBadAuth.Quota.NextRecoverAt = zero, want future value")
	}
	if !updatedBadAuth.NextRetryAfter.Equal(nextRecoverAt) {
		t.Fatalf("updatedBadAuth.NextRetryAfter = %v, want %v", updatedBadAuth.NextRetryAfter, nextRecoverAt)
	}
	modelState := updatedBadAuth.ModelStates["gpt-5.4"]
	if modelState == nil {
		t.Fatal("updatedBadAuth.ModelStates[gpt-5.4] = nil, want cooldown state")
	}
	if !modelState.Quota.Exceeded {
		t.Fatalf("modelState.Quota.Exceeded = false, want true")
	}

	picked, err := (&FillFirstSelector{}).Pick(
		ctx,
		"codex",
		"gpt-5.4",
		executor.Options{},
		[]*Auth{updatedBadAuth, goodAuth},
	)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if picked == nil {
		t.Fatal("Pick() returned nil auth")
	}
	if picked.ID != goodAuth.ID {
		t.Fatalf("picked.ID = %q, want %q", picked.ID, goodAuth.ID)
	}
}
