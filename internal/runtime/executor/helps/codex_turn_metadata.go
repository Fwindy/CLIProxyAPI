package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SanitizeCodexTurnMetadata removes workspace details while preserving the
// rest of the per-turn metadata header.
func SanitizeCodexTurnMetadata(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !gjson.Valid(trimmed) {
		return trimmed
	}
	if !gjson.Get(trimmed, "workspaces").Exists() {
		return trimmed
	}
	sanitized, err := sjson.Delete(trimmed, "workspaces")
	if err != nil {
		return trimmed
	}
	return sanitized
}
