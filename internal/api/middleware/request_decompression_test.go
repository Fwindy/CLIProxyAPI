package middleware

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestRequestBodyDecompressionMiddleware_DecodesSupportedCompressedJSONBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	wantBody := []byte(`{"model":"test-model","input":"hello"}`)
	tests := []struct {
		name            string
		contentEncoding string
		compress        func([]byte) ([]byte, error)
	}{
		{
			name:            "gzip",
			contentEncoding: "gzip",
			compress: func(body []byte) ([]byte, error) {
				var buf bytes.Buffer
				writer := gzip.NewWriter(&buf)
				if _, err := writer.Write(body); err != nil {
					return nil, err
				}
				if err := writer.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
		{
			name:            "deflate",
			contentEncoding: "deflate",
			compress: func(body []byte) ([]byte, error) {
				var buf bytes.Buffer
				writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
				if err != nil {
					return nil, err
				}
				if _, err = writer.Write(body); err != nil {
					return nil, err
				}
				if err = writer.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
		{
			name:            "brotli",
			contentEncoding: "br",
			compress: func(body []byte) ([]byte, error) {
				var buf bytes.Buffer
				writer := brotli.NewWriter(&buf)
				if _, err := writer.Write(body); err != nil {
					return nil, err
				}
				if err := writer.Close(); err != nil {
					return nil, err
				}
				return buf.Bytes(), nil
			},
		},
		{
			name:            "zstd",
			contentEncoding: "zstd",
			compress: func(body []byte) ([]byte, error) {
				var buf bytes.Buffer
				writer, err := zstd.NewWriter(&buf)
				if err != nil {
					return nil, err
				}
				if _, err = writer.Write(body); err != nil {
					return nil, err
				}
				writer.Close()
				return buf.Bytes(), nil
			},
		},
		{
			name:            "multi-encoding reverse decode order",
			contentEncoding: "gzip, zstd",
			compress: func(body []byte) ([]byte, error) {
				var gzipBuf bytes.Buffer
				gzipWriter := gzip.NewWriter(&gzipBuf)
				if _, err := gzipWriter.Write(body); err != nil {
					return nil, err
				}
				if err := gzipWriter.Close(); err != nil {
					return nil, err
				}

				var zstdBuf bytes.Buffer
				zstdWriter, err := zstd.NewWriter(&zstdBuf)
				if err != nil {
					return nil, err
				}
				if _, err = zstdWriter.Write(gzipBuf.Bytes()); err != nil {
					return nil, err
				}
				zstdWriter.Close()
				return zstdBuf.Bytes(), nil
			},
		},
	}

	router := gin.New()
	router.Use(RequestBodyDecompressionMiddleware())
	router.POST("/decode", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.String(http.StatusInternalServerError, "read error: %v", errRead)
			return
		}
		c.Header("X-Seen-Content-Encoding", c.Request.Header.Get("Content-Encoding"))
		c.Header("X-Seen-Content-Length", c.Request.Header.Get("Content-Length"))
		if c.Request.GetBody == nil {
			c.String(http.StatusInternalServerError, "GetBody missing")
			return
		}
		cloned, errClone := c.Request.GetBody()
		if errClone != nil {
			c.String(http.StatusInternalServerError, "GetBody error: %v", errClone)
			return
		}
		defer func() { _ = cloned.Close() }()
		clonedBody, errCloneRead := io.ReadAll(cloned)
		if errCloneRead != nil {
			c.String(http.StatusInternalServerError, "GetBody read error: %v", errCloneRead)
			return
		}
		c.Header("X-Seen-GetBody-Length", strconv.Itoa(len(clonedBody)))
		c.String(http.StatusOK, string(body))
	})

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			compressed, err := tc.compress(wantBody)
			if err != nil {
				t.Fatalf("compress() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/decode", bytes.NewReader(compressed))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", tc.contentEncoding)
			req.Header.Set("Content-Length", "999")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if got := recorder.Body.Bytes(); !bytes.Equal(got, wantBody) {
				t.Fatalf("body = %s, want %s", got, wantBody)
			}
			if got := recorder.Header().Get("X-Seen-Content-Encoding"); got != "" {
				t.Fatalf("handler saw Content-Encoding = %q, want empty", got)
			}
			if got := recorder.Header().Get("X-Seen-Content-Length"); got != strconv.Itoa(len(wantBody)) {
				t.Fatalf("handler saw Content-Length = %q, want %q", got, strconv.Itoa(len(wantBody)))
			}
			if got := recorder.Header().Get("X-Seen-GetBody-Length"); got != strconv.Itoa(len(wantBody)) {
				t.Fatalf("handler saw GetBody length = %q, want %q", got, strconv.Itoa(len(wantBody)))
			}
		})
	}
}
