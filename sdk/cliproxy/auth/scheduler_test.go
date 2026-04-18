package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type schedulerTestGinContext struct {
	values  map[string]any
	headers map[string]string
}

func (c *schedulerTestGinContext) Get(key string) (any, bool) {
	if c == nil || c.values == nil {
		return nil, false
	}
	value, ok := c.values[key]
	return value, ok
}

func (c *schedulerTestGinContext) GetHeader(key string) string {
	if c == nil || c.headers == nil {
		return ""
	}
	return c.headers[http.CanonicalHeaderKey(key)]
}

type schedulerTestExecutor struct{}

func (schedulerTestExecutor) Identifier() string { return "test" }

func (schedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (schedulerTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (schedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type trackingSelector struct {
	calls      int
	lastAuthID []string
}

func (s *trackingSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	s.lastAuthID = s.lastAuthID[:0]
	for _, auth := range auths {
		s.lastAuthID = append(s.lastAuthID, auth.ID)
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[len(auths)-1], nil
}

func newSchedulerForTest(selector Selector, auths ...*Auth) *authScheduler {
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild(auths)
	return scheduler
}

func contextWithAPIKey(apiKey string) context.Context {
	if apiKey == "" {
		return context.Background()
	}
	return context.WithValue(context.Background(), "gin", &schedulerTestGinContext{
		values: map[string]any{"apiKey": apiKey},
	})
}

func contextWithAPIKeyAndSession(apiKey, sessionID string) context.Context {
	ginCtx := &schedulerTestGinContext{
		values:  map[string]any{"apiKey": apiKey},
		headers: map[string]string{},
	}
	if sessionID != "" {
		ginCtx.headers[http.CanonicalHeaderKey("Session_id")] = sessionID
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func contextWithAPIKeyAndTurnSession(apiKey, sessionID string) context.Context {
	ginCtx := &schedulerTestGinContext{
		values:  map[string]any{"apiKey": apiKey},
		headers: map[string]string{},
	}
	if sessionID != "" {
		ginCtx.headers[http.CanonicalHeaderKey("X-Codex-Turn-Metadata")] = `{"session_id":"` + sessionID + `"}`
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func executeSelectedAuthIDForTest(t *testing.T, manager *Manager, ctx context.Context, provider string) string {
	t.Helper()

	selectedAuthID := ""
	_, errExec := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
				selectedAuthID = authID
			},
		},
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if selectedAuthID == "" {
		t.Fatalf("selectedAuthID = empty")
	}
	return selectedAuthID
}

func sessionBindingAuthIDForTest(t *testing.T, scheduler *authScheduler, ctx context.Context) string {
	t.Helper()

	key := codexSessionAffinityKey(ctx, "codex", "")
	if key == "" {
		t.Fatalf("codexSessionAffinityKey() = empty")
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.codexSessionAffinity[key]
}

func registerSchedulerModels(t *testing.T, provider string, model string, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
}

func TestSchedulerPick_RoundRobinHighestPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "high-b", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "high-a", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
	)

	want := []string{"high-a", "high-b", "high-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_FillFirstSticksToFirstReady(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
		&Auth{ID: "c", Provider: "gemini"},
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != "a" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "a")
		}
	}
}

func TestManager_Execute_CodexAPIKeyAffinity_ReusesAssignedAuth(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	ctx := contextWithAPIKey("key-a")
	firstAuthID := executeSelectedAuthIDForTest(t, manager, ctx, "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, ctx, "codex")
	if secondAuthID != firstAuthID {
		t.Fatalf("secondAuthID = %q, want %q", secondAuthID, firstAuthID)
	}
}

func TestManager_Execute_CodexAPIKeyAffinity_SpreadsAcrossDifferentAPIKeys(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKey("key-a"), "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKey("key-b"), "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from %q", secondAuthID, firstAuthID)
	}
}

func TestManager_Execute_CodexAPIKeyAffinity_FallsBackToSharedWhenAuthsExhausted(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKey("key-a"), "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKey("key-b"), "codex")
	thirdAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKey("key-c"), "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from %q", secondAuthID, firstAuthID)
	}
	if thirdAuthID != firstAuthID && thirdAuthID != secondAuthID {
		t.Fatalf("thirdAuthID = %q, want one of %q or %q", thirdAuthID, firstAuthID, secondAuthID)
	}
}

func TestManager_Execute_CodexAPIKeyAffinity_RebindsWhenPreferredAuthBecomesUnavailable(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-c", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-c) error = %v", errRegister)
	}

	ctx := contextWithAPIKey("key-a")
	firstAuthID := executeSelectedAuthIDForTest(t, manager, ctx, "codex")
	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: firstAuthID, Provider: "codex", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(%s disabled) error = %v", firstAuthID, errUpdate)
	}

	secondAuthID := executeSelectedAuthIDForTest(t, manager, ctx, "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from disabled auth %q", secondAuthID, firstAuthID)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: firstAuthID, Provider: "codex", Disabled: false}); errUpdate != nil {
		t.Fatalf("Update(%s enabled) error = %v", firstAuthID, errUpdate)
	}

	thirdAuthID := executeSelectedAuthIDForTest(t, manager, ctx, "codex")
	if thirdAuthID != secondAuthID {
		t.Fatalf("thirdAuthID = %q, want rebound auth %q", thirdAuthID, secondAuthID)
	}
}

func TestManager_Execute_CodexSessionAffinity_SpreadsAcrossSessionsWithinAPIKey(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstCtx := contextWithAPIKeyAndSession("key-a", "sess-1")
	secondCtx := contextWithAPIKeyAndSession("key-a", "sess-2")

	firstAuthID := executeSelectedAuthIDForTest(t, manager, firstCtx, "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, secondCtx, "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from %q", secondAuthID, firstAuthID)
	}

	reusedAuthID := executeSelectedAuthIDForTest(t, manager, firstCtx, "codex")
	if reusedAuthID != firstAuthID {
		t.Fatalf("reusedAuthID = %q, want %q", reusedAuthID, firstAuthID)
	}
}

func TestManager_Execute_CodexSessionAffinity_FallbackShareDoesNotCreateBinding(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstCtx := contextWithAPIKeyAndSession("key-a", "sess-1")
	secondCtx := contextWithAPIKeyAndSession("key-a", "sess-2")
	overflowCtx := contextWithAPIKeyAndSession("key-a", "sess-3")

	firstAuthID := executeSelectedAuthIDForTest(t, manager, firstCtx, "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, secondCtx, "codex")
	overflowAuthID := executeSelectedAuthIDForTest(t, manager, overflowCtx, "codex")

	if overflowAuthID != firstAuthID && overflowAuthID != secondAuthID {
		t.Fatalf("overflowAuthID = %q, want one of %q or %q", overflowAuthID, firstAuthID, secondAuthID)
	}
	if gotBinding := sessionBindingAuthIDForTest(t, manager.scheduler, overflowCtx); gotBinding != "" {
		t.Fatalf("overflow binding = %q, want empty", gotBinding)
	}
}

func TestScheduler_CodexSessionAffinity_ExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "codex-a", Provider: "codex"},
		&Auth{ID: "codex-b", Provider: "codex"},
	)
	firstCtx := contextWithAPIKeyAndSession("key-a", "sess-1")
	secondCtx := contextWithAPIKeyAndSession("key-a", "sess-2")
	now := time.Now()

	scheduler.rememberCodexSessionAffinity(firstCtx, "codex", "codex-a", false, now)
	scheduler.rememberCodexSessionAffinity(secondCtx, "codex", "codex-b", false, now)

	sessionKey := codexSessionAffinityKey(firstCtx, "codex", "")
	if sessionKey == "" {
		t.Fatal("sessionKey = empty")
	}

	scheduler.mu.Lock()
	owner := scheduler.codexSessionOwners["codex-a"]
	owner.lastRequestAt = now.Add(-codexSessionAffinityTTL - time.Minute)
	scheduler.codexSessionOwners["codex-a"] = owner
	scheduler.cleanupCodexAffinityAtLocked(now)
	gotBinding := scheduler.codexSessionAffinity[sessionKey]
	scheduler.mu.Unlock()

	if gotBinding != "" {
		t.Fatalf("expired binding = %q, want empty", gotBinding)
	}
}

func TestManager_Execute_CodexSessionAffinity_SameSessionIDDifferentAPIKeysDoNotShare(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKeyAndSession("key-a", "shared-session"), "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, contextWithAPIKeyAndSession("key-b", "shared-session"), "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from %q", secondAuthID, firstAuthID)
	}
}

func TestManager_Execute_CodexSessionAffinity_UsesTurnMetadataWhenSessionHeaderMissing(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(codex-b) error = %v", errRegister)
	}

	firstCtx := contextWithAPIKeyAndTurnSession("key-a", "sess-1")
	secondCtx := contextWithAPIKeyAndTurnSession("key-a", "sess-2")

	firstAuthID := executeSelectedAuthIDForTest(t, manager, firstCtx, "codex")
	secondAuthID := executeSelectedAuthIDForTest(t, manager, secondCtx, "codex")
	if secondAuthID == firstAuthID {
		t.Fatalf("secondAuthID = %q, want auth different from %q", secondAuthID, firstAuthID)
	}
}

func TestSchedulerPick_PromotesExpiredCooldownBeforePick(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "cooldown-expired")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "cooldown-expired",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(-1 * time.Second),
				},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() auth = nil")
	}
	if got.ID != "cooldown-expired" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "cooldown-expired")
	}
}

func TestSchedulerPick_GeminiVirtualParentUsesTwoLevelRotation(t *testing.T) {
	t.Parallel()

	registerSchedulerModels(t, "gemini-cli", "gemini-2.5-pro", "cred-a::proj-1", "cred-a::proj-2", "cred-b::proj-1", "cred-b::proj-2")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "cred-a::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-a::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-b::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
		&Auth{ID: "cred-b::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
	)

	wantParents := []string{"cred-a", "cred-b", "cred-a", "cred-b"}
	wantIDs := []string{"cred-a::proj-1", "cred-b::proj-1", "cred-a::proj-2", "cred-b::proj-2"}
	for index := range wantIDs {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini-cli", "gemini-2.5-pro", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
		if got.Attributes["gemini_virtual_parent"] != wantParents[index] {
			t.Fatalf("pickSingle() #%d parent = %q, want %q", index, got.Attributes["gemini_virtual_parent"], wantParents[index])
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledAcrossPriorities(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_MixedProvidersUsesWeightedProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersPrefersHighestPriorityTier(t *testing.T) {
	t.Parallel()

	model := "gpt-default"
	registerSchedulerModels(t, "provider-low", model, "low")
	registerSchedulerModels(t, "provider-high-a", model, "high-a")
	registerSchedulerModels(t, "provider-high-b", model, "high-b")

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "provider-low", Attributes: map[string]string{"priority": "4"}},
		&Auth{ID: "high-a", Provider: "provider-high-a", Attributes: map[string]string{"priority": "7"}},
		&Auth{ID: "high-b", Provider: "provider-high-b", Attributes: map[string]string{"priority": "7"}},
	)

	providers := []string{"provider-low", "provider-high-a", "provider-high-b"}
	wantProviders := []string{"provider-high-a", "provider-high-b", "provider-high-a", "provider-high-b"}
	wantIDs := []string{"high-a", "high-b", "high-a", "high-b"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_UsesWeightedProviderRotationBeforeCredentialRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_DisallowFreeAuthSkipsCodexFreePlan(t *testing.T) {
	t.Parallel()

	model := "gpt-5.4-mini"
	registerSchedulerModels(t, "codex", model, "codex-a-free", "codex-b-plus")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}}); errRegister != nil {
		t.Fatalf("Register(codex-a-free) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}}); errRegister != nil {
		t.Fatalf("Register(codex-b-plus) error = %v", errRegister)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true},
	}
	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "codex" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "codex")
	}
	if got.ID != "codex-b-plus" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "codex-b-plus")
	}
}

func TestManagerCustomSelector_FallsBackToLegacyPath(t *testing.T) {
	t.Parallel()

	selector := &trackingSelector{}
	manager := NewManager(nil, selector, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.auths["auth-a"] = &Auth{ID: "auth-a", Provider: "gemini"}
	manager.auths["auth-b"] = &Auth{ID: "auth-b", Provider: "gemini"}

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want %d", selector.calls, 1)
	}
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("len(selector.lastAuthID) = %d, want %d", len(selector.lastAuthID), 2)
	}
	if got.ID != selector.lastAuthID[len(selector.lastAuthID)-1] {
		t.Fatalf("pickNext() auth.ID = %q, want selector-picked %q", got.ID, selector.lastAuthID[len(selector.lastAuthID)-1])
	}
}

func TestManager_InitializesSchedulerForBuiltInSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyRoundRobin)
	}

	manager.SetSelector(&FillFirstSelector{})
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
}

func TestManager_SchedulerTracksRegisterAndUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-a" {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-a", got)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(auth-a) error = %v", errUpdate)
	}

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after update error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after update auth = %v, want auth-b", got)
	}
}

func TestManager_PickNextMixed_UsesSchedulerRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_SkipsProvidersWithoutExecutors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "claude")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "claude-a")
	}
}

func TestManager_SchedulerTracksMarkResultCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-a", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient("auth-b", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-a")
		reg.UnregisterClient("auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after cooldown auth = %v, want auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seen := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d auth = nil", index)
		}
		seen[got.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want %d", len(seen), 2)
	}
}
