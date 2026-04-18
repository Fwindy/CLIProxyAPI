// Package logging provides Gin middleware for HTTP request logging and panic recovery.
// It integrates Gin web framework with logrus for structured logging of HTTP requests,
// responses, and error handling with panic recovery capabilities.
package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// aiAPIPrefixes defines path prefixes for AI API requests that should have request ID tracking.
var aiAPIPrefixes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/images",
	"/v1/messages",
	"/v1/responses",
	"/v1beta/models/",
	"/api/provider/",
}

const (
	skipGinLogKey  = "__gin_skip_request_logging__"
	creditsUsedKey = "__antigravity_credits_used__"
)

// GinLogrusLogger returns a Gin middleware handler that logs HTTP requests and responses
// using logrus. It captures request details including method, path, status code, latency,
// client IP, and any error messages. Request ID is only added for AI API requests.
//
// Output format (AI API): [2025-12-23 20:14:10] [info ] | a1b2c3d4 | 200 |       23.559s | ...
// Output format (others): [2025-12-23 20:14:10] [info ] | -------- | 200 |       23.559s | ...
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for request logging
func GinLogrusLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
		requestBodySnapshot := snapshotAccessLogRequestBody(c.Request, path)

		// Only generate request ID for AI API paths
		var requestID string
		if isAIAPIPath(path) {
			requestID = GenerateRequestID()
			SetGinRequestID(c, requestID)
			ctx := WithRequestID(c.Request.Context(), requestID)
			c.Request = c.Request.WithContext(ctx)
		}

		c.Next()

		if shouldSkipGinRequestLogging(c) {
			return
		}

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		if requestID == "" {
			requestID = "--------"
		}
		logLine := fmt.Sprintf("%3d | %13v | %15s | %-7s \"%s\"", statusCode, latency, clientIP, method, path)
		if creditsUsed(c) {
			logLine += " [credits]"
		}
		if userAgent := strings.TrimSpace(c.Request.UserAgent()); userAgent != "" {
			logLine += " | ua=" + strconv.Quote(userAgent)
		}
		if originator := strings.TrimSpace(c.Request.Header.Get("originator")); originator != "" {
			logLine += " | originator=" + strconv.Quote(originator)
		}
		if instructionsCodex, ok := accessLogInstructionsCodex(c.Request, path, requestBodySnapshot); ok {
			logLine += fmt.Sprintf(" | instructions_codex=%t", instructionsCodex)
		}
		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

		entry := log.WithField("request_id", requestID)

		switch {
		case statusCode >= http.StatusInternalServerError:
			entry.Error(logLine)
		case statusCode >= http.StatusBadRequest:
			entry.Warn(logLine)
		default:
			entry.Info(logLine)
		}
	}
}

func snapshotAccessLogRequestBody(req *http.Request, path string) []byte {
	if !shouldLogInstructionsCodex(req, path) || req == nil || req.Body == nil {
		return nil
	}
	if req.GetBody != nil {
		return nil
	}
	if strings.TrimSpace(req.Header.Get("Content-Encoding")) != "" {
		return nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return body
}

func accessLogInstructionsCodex(req *http.Request, path string, requestBodySnapshot []byte) (bool, bool) {
	if !shouldLogInstructionsCodex(req, path) {
		return false, false
	}

	body := requestBodySnapshot
	if len(body) == 0 {
		body = requestBodyClone(req)
	}
	if len(body) == 0 {
		return false, true
	}

	instructions := gjson.GetBytes(body, "instructions")
	if instructions.Type != gjson.String {
		return false, true
	}
	return strings.HasPrefix(instructions.String(), "You are Codex"), true
}

func shouldLogInstructionsCodex(req *http.Request, path string) bool {
	if req == nil || req.Method != http.MethodPost {
		return false
	}
	switch path {
	case "/v1/responses", "/responses":
		return true
	default:
		return false
	}
}

func requestBodyClone(req *http.Request) []byte {
	if req == nil || req.GetBody == nil {
		return nil
	}
	bodyReader, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer func() {
		_ = bodyReader.Close()
	}()

	body, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil
	}
	return body
}

// isAIAPIPath checks if the given path is an AI API endpoint that should have request ID tracking.
func isAIAPIPath(path string) bool {
	for _, prefix := range aiAPIPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// GinLogrusRecovery returns a Gin middleware handler that recovers from panics and logs
// them using logrus. When a panic occurs, it captures the panic value, stack trace,
// and request path, then returns a 500 Internal Server Error response to the client.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for panic recovery
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			// Let net/http handle ErrAbortHandler so the connection is aborted without noisy stack logs.
			panic(http.ErrAbortHandler)
		}

		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// SkipGinRequestLogging marks the provided Gin context so that GinLogrusLogger
// will skip emitting a log line for the associated request.
func SkipGinRequestLogging(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(skipGinLogKey, true)
}

func shouldSkipGinRequestLogging(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(skipGinLogKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}

func creditsUsed(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(creditsUsedKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}
