package auth

import (
	"encoding/json"
	"strings"
	"time"
)

const persistedRuntimeMetadataKey = "_cliproxy_runtime"

type persistedRuntimeState struct {
	Status         Status                 `json:"status,omitempty"`
	StatusMessage  string                 `json:"status_message,omitempty"`
	Unavailable    bool                   `json:"unavailable,omitempty"`
	Quota          QuotaState             `json:"quota,omitempty"`
	LastError      *Error                 `json:"last_error,omitempty"`
	NextRetryAfter time.Time              `json:"next_retry_after,omitempty"`
	ModelStates    map[string]*ModelState `json:"model_states,omitempty"`
}

// MetadataForPersistence returns a metadata copy augmented with persisted runtime state.
func MetadataForPersistence(auth *Auth) map[string]any {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	metadata := make(map[string]any, len(auth.Metadata)+2)
	for key, value := range auth.Metadata {
		metadata[key] = value
	}
	metadata["disabled"] = auth.Disabled
	if state := buildPersistedRuntimeState(auth); state != nil {
		metadata[persistedRuntimeMetadataKey] = state
	} else {
		delete(metadata, persistedRuntimeMetadataKey)
	}
	return metadata
}

// ApplyPersistedRuntimeFromMetadata restores persisted runtime state from metadata.
func ApplyPersistedRuntimeFromMetadata(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}
	raw, ok := auth.Metadata[persistedRuntimeMetadataKey]
	if !ok || raw == nil {
		return
	}
	state, ok := decodePersistedRuntimeState(raw)
	if !ok || state == nil {
		return
	}
	if state.Status != "" {
		auth.Status = state.Status
	}
	if strings.TrimSpace(state.StatusMessage) != "" {
		auth.StatusMessage = state.StatusMessage
	}
	if state.Unavailable {
		auth.Unavailable = true
	}
	if quotaStateNeedsPersistence(state.Quota) {
		auth.Quota = cloneQuotaState(state.Quota)
	}
	if state.LastError != nil {
		auth.LastError = clonePersistedError(state.LastError)
	}
	if !state.NextRetryAfter.IsZero() {
		auth.NextRetryAfter = state.NextRetryAfter
	}
	if len(state.ModelStates) > 0 {
		auth.ModelStates = make(map[string]*ModelState, len(state.ModelStates))
		for model, modelState := range state.ModelStates {
			if modelState == nil {
				continue
			}
			auth.ModelStates[model] = modelState.Clone()
		}
	}
}

func buildPersistedRuntimeState(auth *Auth) *persistedRuntimeState {
	if auth == nil {
		return nil
	}
	state := &persistedRuntimeState{}
	if auth.Status != "" && auth.Status != StatusActive {
		state.Status = auth.Status
	}
	if strings.TrimSpace(auth.StatusMessage) != "" {
		state.StatusMessage = auth.StatusMessage
	}
	if auth.Unavailable {
		state.Unavailable = true
	}
	if quotaStateNeedsPersistence(auth.Quota) {
		state.Quota = cloneQuotaState(auth.Quota)
	}
	if auth.LastError != nil {
		state.LastError = clonePersistedError(auth.LastError)
	}
	if !auth.NextRetryAfter.IsZero() {
		state.NextRetryAfter = auth.NextRetryAfter
	}
	if filtered := filterPersistedModelStates(auth.ModelStates); len(filtered) > 0 {
		state.ModelStates = filtered
	}
	if !persistedRuntimeStateNeedsPersistence(state) {
		return nil
	}
	return state
}

func decodePersistedRuntimeState(raw any) (*persistedRuntimeState, bool) {
	bytes, err := json.Marshal(raw)
	if err != nil || len(bytes) == 0 {
		return nil, false
	}
	var state persistedRuntimeState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return nil, false
	}
	return &state, true
}

func persistedRuntimeStateNeedsPersistence(state *persistedRuntimeState) bool {
	if state == nil {
		return false
	}
	if state.Status != "" && state.Status != StatusActive {
		return true
	}
	if strings.TrimSpace(state.StatusMessage) != "" {
		return true
	}
	if state.Unavailable {
		return true
	}
	if quotaStateNeedsPersistence(state.Quota) {
		return true
	}
	if state.LastError != nil {
		return true
	}
	if !state.NextRetryAfter.IsZero() {
		return true
	}
	return len(state.ModelStates) > 0
}

func filterPersistedModelStates(states map[string]*ModelState) map[string]*ModelState {
	if len(states) == 0 {
		return nil
	}
	filtered := make(map[string]*ModelState, len(states))
	for model, state := range states {
		if !modelStateNeedsPersistence(state) {
			continue
		}
		filtered[model] = state.Clone()
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func modelStateNeedsPersistence(state *ModelState) bool {
	if state == nil {
		return false
	}
	if state.Status == StatusDisabled || (state.Status != "" && state.Status != StatusActive) {
		return true
	}
	if strings.TrimSpace(state.StatusMessage) != "" {
		return true
	}
	if state.Unavailable {
		return true
	}
	if !state.NextRetryAfter.IsZero() {
		return true
	}
	if quotaStateNeedsPersistence(state.Quota) {
		return true
	}
	return state.LastError != nil
}

func quotaStateNeedsPersistence(quota QuotaState) bool {
	return quota.Exceeded || strings.TrimSpace(quota.Reason) != "" || !quota.NextRecoverAt.IsZero() || quota.BackoffLevel != 0
}

func cloneQuotaState(quota QuotaState) QuotaState {
	return QuotaState{
		Exceeded:      quota.Exceeded,
		Reason:        quota.Reason,
		NextRecoverAt: quota.NextRecoverAt,
		BackoffLevel:  quota.BackoffLevel,
	}
}

func clonePersistedError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}
