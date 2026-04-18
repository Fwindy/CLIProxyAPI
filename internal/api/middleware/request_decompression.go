package middleware

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// RequestBodyDecompressionMiddleware decodes supported compressed request bodies
// before later middleware and handlers inspect or consume the payload.
func RequestBodyDecompressionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		req := c.Request
		if req == nil || req.Body == nil {
			c.Next()
			return
		}

		decoded, handled, err := decodeCompressedRequestBody(req.Body, req.Header.Get("Content-Encoding"))
		if !handled {
			c.Next()
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to decode request body"})
			return
		}

		req.Body = io.NopCloser(bytes.NewReader(decoded))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(decoded)), nil
		}
		req.ContentLength = int64(len(decoded))
		req.Header.Del("Content-Encoding")
		req.Header.Set("Content-Length", strconv.Itoa(len(decoded)))

		c.Next()
	}
}

func decodeCompressedRequestBody(body io.ReadCloser, contentEncoding string) ([]byte, bool, error) {
	encodings, supported := supportedRequestEncodings(contentEncoding)
	if !supported || len(encodings) == 0 {
		return nil, false, nil
	}

	reader := io.Reader(body)
	closeFns := []func() error{body.Close}
	for i := len(encodings) - 1; i >= 0; i-- {
		switch encodings[i] {
		case "gzip":
			gzipReader, err := gzip.NewReader(reader)
			if err != nil {
				closeRequestDecodeClosers(closeFns)
				return nil, true, fmt.Errorf("create gzip reader: %w", err)
			}
			reader = gzipReader
			closeFns = append(closeFns, gzipReader.Close)
		case "deflate":
			deflateReader := flate.NewReader(reader)
			reader = deflateReader
			closeFns = append(closeFns, deflateReader.Close)
		case "br":
			reader = brotli.NewReader(reader)
		case "zstd":
			decoder, err := zstd.NewReader(reader)
			if err != nil {
				closeRequestDecodeClosers(closeFns)
				return nil, true, fmt.Errorf("create zstd reader: %w", err)
			}
			reader = decoder
			closeFns = append(closeFns, func() error {
				decoder.Close()
				return nil
			})
		}
	}

	decoded, err := io.ReadAll(reader)
	closeRequestDecodeClosers(closeFns)
	if err != nil {
		return nil, true, fmt.Errorf("read decoded body: %w", err)
	}
	return decoded, true, nil
}

func supportedRequestEncodings(contentEncoding string) ([]string, bool) {
	parts := strings.Split(contentEncoding, ",")
	encodings := make([]string, 0, len(parts))
	for _, raw := range parts {
		encoding := strings.TrimSpace(strings.ToLower(raw))
		switch encoding {
		case "", "identity":
			continue
		case "gzip", "deflate", "br", "zstd":
			encodings = append(encodings, encoding)
		default:
			return nil, false
		}
	}
	return encodings, true
}

func closeRequestDecodeClosers(closeFns []func() error) {
	for i := len(closeFns) - 1; i >= 0; i-- {
		_ = closeFns[i]()
	}
}
