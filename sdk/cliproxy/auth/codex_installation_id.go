package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

const (
	CodexInstallationIDHeaderName              = "X-Codex-Installation-Id"
	codexInstallationIDMetadataKey             = "codex_installation_id"
	codexInstallationIDClientMetadataFieldName = "x-codex-installation-id"
)

type codexInstallationIDHeaderGetter interface {
	GetHeader(string) string
}

// CodexInstallationID returns the persisted Codex installation id when present and valid.
func CodexInstallationID(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	raw, _ := auth.Metadata[codexInstallationIDMetadataKey].(string)
	raw = strings.TrimSpace(raw)
	if !validCodexInstallationID(raw) {
		return ""
	}
	return raw
}

// EnsureCodexInstallationID returns the auth-specific Codex installation id, generating
// and storing a new random UUIDv4 when the existing metadata is missing or invalid.
func EnsureCodexInstallationID(auth *Auth) (string, bool) {
	if auth == nil {
		return "", false
	}
	if existing := CodexInstallationID(auth); existing != "" {
		return existing, false
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	generated := strings.ToLower(uuid.NewString())
	auth.Metadata[codexInstallationIDMetadataKey] = generated
	return generated, true
}

// CodexInstallationIDRequested reports whether the inbound request asked for a
// Codex installation id, either through legacy headers or the request body's
// client_metadata.x-codex-installation-id field.
func CodexInstallationIDRequested(ctx context.Context, body []byte) bool {
	if codexInstallationIDRequestedInPayload(body) {
		return true
	}
	return codexInstallationIDRequestedInHeaders(ctx)
}

func codexInstallationIDRequestedInHeaders(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value("gin")
	getter, ok := raw.(codexInstallationIDHeaderGetter)
	if !ok || getter == nil {
		return false
	}
	return strings.TrimSpace(getter.GetHeader(CodexInstallationIDHeaderName)) != ""
}

func codexInstallationIDRequestedInPayload(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return false
	}
	var payload struct {
		ClientMetadata map[string]json.RawMessage `json:"client_metadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.ClientMetadata) == 0 {
		return false
	}
	raw, ok := payload.ClientMetadata[codexInstallationIDClientMetadataFieldName]
	if !ok {
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value) != ""
	}
	return true
}

func validCodexInstallationID(value string) bool {
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
