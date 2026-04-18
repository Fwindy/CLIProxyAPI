package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	executorpkg "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type codexInstallationIDStore struct {
	saveCount atomic.Int32
	saved     []*cliproxyauth.Auth
}

func (s *codexInstallationIDStore) List(context.Context) ([]*cliproxyauth.Auth, error) {
	return nil, nil
}

func (s *codexInstallationIDStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	s.saveCount.Add(1)
	s.saved = append(s.saved, auth.Clone())
	return "", nil
}

func (s *codexInstallationIDStore) Delete(context.Context, string) error { return nil }

func contextWithRequestBody(body string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	ginCtx.Request.Header.Set("Content-Type", "application/json")
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func validInstallationID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 || value != strings.ToLower(value) {
		return false
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return false
	}
	return parsed.Version() == 4 && parsed.String() == value
}

func TestEnsureCodexInstallationID_ReplacesInvalidExistingValue(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"codex_installation_id": "11111111-1111-1111-1111-111111111111",
		},
	}

	got, changed := cliproxyauth.EnsureCodexInstallationID(auth)
	if !changed {
		t.Fatalf("EnsureCodexInstallationID() changed = false, want true")
	}
	if got == "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("EnsureCodexInstallationID() reused invalid value %q", got)
	}
	if !validInstallationID(got) {
		t.Fatalf("EnsureCodexInstallationID() = %q, want valid v4 installation id", got)
	}
}

func TestManagerExecute_AssignsAndPersistsCodexInstallationID(t *testing.T) {
	t.Parallel()

	var upstreamInstallationID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamInstallationID = r.Header.Get(cliproxyauth.CodexInstallationIDHeaderName)
		body, _ := io.ReadAll(r.Body)
		_ = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	store := &codexInstallationIDStore{}
	manager := cliproxyauth.NewManager(store, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executorpkg.NewCodexExecutor(&config.Config{}))

	auth := &cliproxyauth.Auth{
		ID:       "codex-a.json",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test-api-key",
		},
		Metadata: map[string]any{"type": "codex"},
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, err := manager.Execute(
		contextWithRequestBody(`{"model":"gpt-5.4","input":"hello","client_metadata":{"x-codex-installation-id":"client-installation-id"}}`),
		[]string{"codex"},
		cliproxyexecutor.Request{
			Payload: []byte(`{"model":"gpt-5.4","input":"hello","client_metadata":{"x-codex-installation-id":"client-installation-id"}}`),
		},
		cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       false,
		},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if upstreamInstallationID == "" {
		t.Fatalf("upstream installation id = empty")
	}
	if upstreamInstallationID == "client-installation-id" {
		t.Fatalf("upstream installation id reused downstream value %q", upstreamInstallationID)
	}
	if !validInstallationID(upstreamInstallationID) {
		t.Fatalf("upstream installation id = %q, want valid v4 installation id", upstreamInstallationID)
	}
	if got := store.saveCount.Load(); got < 1 {
		t.Fatalf("saveCount = %d, want >= 1", got)
	}
	foundPersistedID := false
	for _, saved := range store.saved {
		if saved == nil {
			continue
		}
		if got := cliproxyauth.CodexInstallationID(saved); got == upstreamInstallationID {
			foundPersistedID = true
			break
		}
	}
	if !foundPersistedID {
		t.Fatalf("persisted installation id %q not found in saved auth snapshots", upstreamInstallationID)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) missing", auth.ID)
	}
	if got := cliproxyauth.CodexInstallationID(current); got != upstreamInstallationID {
		t.Fatalf("manager installation id = %q, want %q", got, upstreamInstallationID)
	}
}

func TestManagerNewHttpRequest_AssignsCodexInstallationID(t *testing.T) {
	t.Parallel()

	store := &codexInstallationIDStore{}
	manager := cliproxyauth.NewManager(store, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executorpkg.NewCodexExecutor(&config.Config{}))

	auth := &cliproxyauth.Auth{
		ID:       "codex-b.json",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "test-api-key",
		},
		Metadata: map[string]any{"type": "codex"},
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	req, err := manager.NewHttpRequest(
		context.Background(),
		auth,
		http.MethodPost,
		"https://example.com/v1/test",
		[]byte(`{"client_metadata":{"x-codex-installation-id":"client-installation-id"}}`),
		nil,
	)
	if err != nil {
		t.Fatalf("NewHttpRequest() error = %v", err)
	}

	got := req.Header.Get(cliproxyauth.CodexInstallationIDHeaderName)
	if got == "" {
		t.Fatalf("X-Codex-Installation-Id = empty")
	}
	if got == "client-installation-id" {
		t.Fatalf("X-Codex-Installation-Id reused downstream value %q", got)
	}
	if !validInstallationID(got) {
		t.Fatalf("X-Codex-Installation-Id = %q, want valid v4 installation id", got)
	}
}
