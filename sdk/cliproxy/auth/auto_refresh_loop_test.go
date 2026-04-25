package auth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

type recordingRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan time.Time
}

func (e recordingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	select {
	case e.started <- time.Now():
	default:
	}
	return auth, nil
}

type failingRefreshExecutor struct {
	schedulerProviderTestExecutor
	refreshErr error
}

func (e failingRefreshExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, e.refreshErr
}

type recordingPersistStore struct {
	mu        sync.Mutex
	saveCount int
	lastSaved *Auth
}

func (s *recordingPersistStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *recordingPersistStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCount++
	s.lastSaved = auth.Clone()
	return "", nil
}

func (s *recordingPersistStore) Delete(context.Context, string) error { return nil }

func (s *recordingPersistStore) snapshot() (int, *Auth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastSaved == nil {
		return s.saveCount, nil
	}
	return s.saveCount, s.lastSaved.Clone()
}

func setRefreshLeadFactory(t *testing.T, provider string, factory func() *time.Duration) {
	t.Helper()
	key := strings.ToLower(strings.TrimSpace(provider))
	refreshLeadMu.Lock()
	prev, hadPrev := refreshLeadFactories[key]
	if factory == nil {
		delete(refreshLeadFactories, key)
	} else {
		refreshLeadFactories[key] = factory
	}
	refreshLeadMu.Unlock()
	t.Cleanup(func() {
		refreshLeadMu.Lock()
		if hadPrev {
			refreshLeadFactories[key] = prev
		} else {
			delete(refreshLeadFactories, key)
		}
		refreshLeadMu.Unlock()
	})
}

func TestNextRefreshCheckAt_DisabledUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Disabled: true}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_APIKeyUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Attributes: map[string]string{"api_key": "k"}}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_RefreshDisabledUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:       "a1",
		Provider: "codex",
		Metadata: map[string]any{
			"email":            "x@example.com",
			"refresh_disabled": true,
		},
	}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_NextRefreshAfterGate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(30 * time.Minute)
	auth := &Auth{
		ID:               "a1",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAt_PreferredInterval_PicksEarliestCandidate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:              "a1",
		Provider:        "test",
		LastRefreshedAt: now,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"expires_at":               expiry.Format(time.RFC3339),
			"refresh_interval_seconds": 900, // 15m
		},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-15 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_ProviderLead_Expiry(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_RefreshEvaluatorFallback(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Metadata: map[string]any{"email": "x@example.com"},
		Runtime:  testRefreshEvaluator{},
	}
	got, ok := nextRefreshCheckAt(now, auth, interval)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := now.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestManager_MarkRefreshPending_BlocksDuplicateWhileRefreshStillInFlight(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["a1"] = &Auth{
		ID:       "a1",
		Provider: "codex",
		Metadata: map[string]any{"email": "x@example.com"},
	}

	if ok := manager.markRefreshPending("a1", now); !ok {
		t.Fatal("markRefreshPending() first call = false, want true")
	}

	later := now.Add(refreshPendingBackoff + time.Second)
	if ok := manager.markRefreshPending("a1", later); ok {
		t.Fatal("markRefreshPending() second call = true, want false while first refresh is still in flight")
	}
}

func TestManager_RefreshAuth_ClearsInFlightMarker(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["a1"] = &Auth{
		ID:       "a1",
		Provider: "codex",
		Metadata: map[string]any{"email": "x@example.com", "refresh_token": "token-1"},
	}
	manager.executors["codex"] = schedulerProviderTestExecutor{provider: "codex"}

	if ok := manager.markRefreshPending("a1", now); !ok {
		t.Fatal("markRefreshPending() first call = false, want true")
	}

	manager.refreshAuth(context.Background(), "a1")

	later := now.Add(refreshPendingBackoff + time.Second)
	if ok := manager.markRefreshPending("a1", later); !ok {
		t.Fatal("markRefreshPending() after refreshAuth = false, want true")
	}
}

func TestManager_RefreshAuth_RefreshTokenReusedDisablesFutureRefreshAndPersists(t *testing.T) {
	store := &recordingPersistStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.auths["a1"] = &Auth{
		ID:       "a1",
		Provider: "codex",
		Metadata: map[string]any{
			"email":         "x@example.com",
			"refresh_token": "refresh-token-1",
		},
	}
	manager.executors["codex"] = failingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		refreshErr:                    &Error{Message: "token refresh failed with status 401: {\"error\":{\"code\":\"refresh_token_reused\"}}"},
	}

	manager.refreshAuth(context.Background(), "a1")

	updated, ok := manager.GetByID("a1")
	if !ok || updated == nil {
		t.Fatal("GetByID() ok = false, want true")
	}
	if got := updated.Metadata["refresh_disabled"]; got != true {
		t.Fatalf("refresh_disabled metadata = %v, want true", got)
	}
	if got := updated.Metadata["refresh_disabled_reason"]; got != "refresh_token_reused" {
		t.Fatalf("refresh_disabled_reason metadata = %v, want %q", got, "refresh_token_reused")
	}
	if _, scheduled := nextRefreshCheckAt(time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC), updated, 15*time.Minute); scheduled {
		t.Fatal("nextRefreshCheckAt() ok = true, want false after refresh_token_reused")
	}

	saveCount, saved := store.snapshot()
	if saveCount != 1 {
		t.Fatalf("persist Save count = %d, want 1", saveCount)
	}
	if saved == nil {
		t.Fatal("persisted auth = nil, want non-nil")
	}
	if got := saved.Metadata["refresh_disabled"]; got != true {
		t.Fatalf("persisted refresh_disabled metadata = %v, want true", got)
	}
	if got := saved.Metadata["refresh_disabled_reason"]; got != "refresh_token_reused" {
		t.Fatalf("persisted refresh_disabled_reason metadata = %v, want %q", got, "refresh_token_reused")
	}
}

func TestAuthAutoRefreshLoop_Run_ConsumesQueuedRefreshesWithoutArtificialDelay(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["a1"] = &Auth{ID: "a1", Provider: "codex", Metadata: map[string]any{"email": "one@example.com"}}
	manager.auths["a2"] = &Auth{ID: "a2", Provider: "codex", Metadata: map[string]any{"email": "two@example.com"}}

	started := make(chan time.Time, 4)
	manager.executors["codex"] = recordingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       started,
	}

	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 8)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		loop.run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	loop.jobs <- "a1"
	loop.jobs <- "a2"

	<-started
	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second refresh did not start within 100ms; worker appears artificially throttled")
	}
}

func TestAuthAutoRefreshLoop_RebuildAfterRestartSchedulesDueAuths(t *testing.T) {
	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.auths["a1"] = &Auth{
		ID:       "a1",
		Provider: "codex",
		Metadata: map[string]any{
			"email":                    "restart@example.com",
			"last_refresh":             now.Add(-2 * time.Minute).Format(time.RFC3339),
			"refresh_interval_seconds": 30,
		},
	}

	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
	loop.rebuild(now)

	got, ok := loop.peek()
	if !ok {
		t.Fatal("peek() ok = false, want true after restart rebuild")
	}
	if got.After(now) {
		t.Fatalf("peek() = %s, want due now or earlier", got)
	}
}
