package management

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type deleteUsageRequest struct {
	IDs []string `json:"ids"`
}

type importUsageResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

type legacyUsageImportPayload struct {
	Version int                 `json:"version"`
	Usage   legacyUsageSnapshot `json:"usage"`
}

type legacyUsageSnapshot struct {
	APIs map[string]legacyAPISnapshot `json:"apis"`
}

type legacyAPISnapshot struct {
	Models map[string]legacyModelSnapshot `json:"models"`
}

type legacyModelSnapshot struct {
	Details []legacyRequestDetail `json:"details"`
}

type legacyRequestDetail struct {
	Timestamp          time.Time        `json:"timestamp"`
	LatencyMs          int64            `json:"latency_ms"`
	FirstByteLatencyMs int64            `json:"first_byte_latency_ms"`
	GenerationMs       int64            `json:"generation_ms"`
	Source             string           `json:"source"`
	AuthIndex          string           `json:"auth_index"`
	ThinkingEffort     string           `json:"thinking_effort"`
	Tokens             usage.TokenStats `json:"tokens"`
	Failed             bool             `json:"failed"`
}

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageStatistics returns persisted request usage grouped by API key and model.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	rng, ok := parseUsageRange(c)
	if !ok {
		return
	}

	store := h.currentUsageStore()
	if store == nil {
		c.JSON(http.StatusOK, usage.APIUsage{})
		return
	}

	result, err := store.Query(c.Request.Context(), rng)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query usage"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ImportUsageStatistics imports legacy in-memory usage export JSON into the sqlite usage store.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	store := h.currentUsageStore()
	if store == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	snapshot, err := parseLegacyUsageSnapshot(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := importLegacyUsageSnapshot(c.Request.Context(), store, snapshot)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to import usage"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// DeleteUsageRecords removes persisted usage records by record ID.
func (h *Handler) DeleteUsageRecords(c *gin.Context) {
	store := h.currentUsageStore()
	if store == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store unavailable"})
		return
	}

	var body deleteUsageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	ids := make([]string, 0, len(body.IDs))
	seen := make(map[string]struct{}, len(body.IDs))
	for _, id := range body.IDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		ids = append(ids, trimmed)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}

	result, err := store.Delete(c.Request.Context(), ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete usage records"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseLegacyUsageSnapshot(data []byte) (legacyUsageSnapshot, error) {
	var payload legacyUsageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return legacyUsageSnapshot{}, errors.New("invalid json")
	}
	if payload.Usage.APIs != nil {
		if payload.Version != 0 && payload.Version != 1 {
			return legacyUsageSnapshot{}, errors.New("unsupported version")
		}
		return payload.Usage, nil
	}

	var snapshot legacyUsageSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return legacyUsageSnapshot{}, errors.New("invalid json")
	}
	if snapshot.APIs == nil {
		return legacyUsageSnapshot{}, errors.New("unsupported usage import format")
	}
	return snapshot, nil
}

func importLegacyUsageSnapshot(ctx context.Context, store usage.Store, snapshot legacyUsageSnapshot) (importUsageResult, error) {
	result := importUsageResult{}
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for i, detail := range modelSnapshot.Details {
				record := usage.Record{
					ID:                 legacyUsageRecordID(apiName, modelName, i, detail),
					Timestamp:          detail.Timestamp,
					APIKey:             apiName,
					Model:              modelName,
					Source:             detail.Source,
					AuthIndex:          detail.AuthIndex,
					LatencyMs:          detail.LatencyMs,
					FirstByteLatencyMs: detail.FirstByteLatencyMs,
					GenerationMs:       detail.GenerationMs,
					ThinkingEffort:     detail.ThinkingEffort,
					Tokens:             detail.Tokens,
					Failed:             detail.Failed,
				}
				if err := store.Insert(ctx, record); err != nil {
					if isDuplicateUsageRecordError(err) {
						result.Skipped++
						continue
					}
					return result, err
				}
				result.Added++
			}
		}
	}
	return result, nil
}

func legacyUsageRecordID(apiName, modelName string, index int, detail legacyRequestDetail) string {
	payload := struct {
		APIName   string              `json:"api_name"`
		ModelName string              `json:"model_name"`
		Index     int                 `json:"index"`
		Detail    legacyRequestDetail `json:"detail"`
	}{
		APIName:   apiName,
		ModelName: modelName,
		Index:     index,
		Detail:    detail,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("legacy-%x", sum[:16])
}

func isDuplicateUsageRecordError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") && strings.Contains(msg, "usage_records.id")
}

func parseUsageRange(c *gin.Context) (usage.QueryRange, bool) {
	var rng usage.QueryRange

	if rawStart := strings.TrimSpace(c.Query("start")); rawStart != "" {
		start, err := time.Parse(time.RFC3339, rawStart)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start"})
			return rng, false
		}
		start = start.UTC()
		rng.Start = &start
	}

	if rawEnd := strings.TrimSpace(c.Query("end")); rawEnd != "" {
		end, err := time.Parse(time.RFC3339, rawEnd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end"})
			return rng, false
		}
		end = end.UTC()
		rng.End = &end
	}

	return rng, true
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}
