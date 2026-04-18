package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func logResponsesRequest(c *gin.Context, message string, fields log.Fields) {
	log.WithFields(buildResponsesRequestLogFields(c, fields)).Info(message)
}

func buildResponsesRequestLogFields(c *gin.Context, extra log.Fields) log.Fields {
	fields := log.Fields{}
	for key, value := range extra {
		fields[key] = value
	}
	if c == nil || c.Request == nil {
		return fields
	}
	if method := strings.TrimSpace(c.Request.Method); method != "" {
		fields["method"] = method
	}
	if c.Request.URL != nil {
		if path := strings.TrimSpace(c.Request.URL.Path); path != "" {
			fields["path"] = path
		}
	}
	if userAgent := strings.TrimSpace(responsesClientUserAgent(c)); userAgent != "" {
		fields["user_agent"] = userAgent
	}
	if remote := strings.TrimSpace(c.ClientIP()); remote != "" {
		fields["remote"] = remote
	}
	return fields
}

func responsesClientUserAgent(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.Request.UserAgent())
}
