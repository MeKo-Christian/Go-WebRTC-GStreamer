// Package middleware provides HTTP middleware for the WebRTC SFU server.
// It includes correlation ID tracking, rate limiting, and request validation.
package middleware

import (
	"net/http"
	"time"

	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
)

// CorrelationIDMiddleware adds correlation ID to requests
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := request.Context()

		// Check if correlation ID already exists in headers
		correlationID := request.Header.Get("X-Correlation-ID")
		if correlationID == "" {
			// Generate new correlation ID if not provided
			correlationID = logging.GenerateCorrelationID()
		}

		// Add correlation ID to context
		ctx = logging.AddCorrelationIDToContext(ctx, correlationID)

		// Add remote address to context for logging
		ctx = logging.AddRemoteAddrToContext(ctx, request.RemoteAddr)

		// Set correlation ID in response header
		writer.Header().Set("X-Correlation-ID", correlationID)

		// Update request with new context
		request = request.WithContext(ctx)

		next.ServeHTTP(writer, request)
	})
}

// LoggingMiddleware logs HTTP requests
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		start := time.Now()
		ctx := req.Context()
		logger := logging.WithContext(ctx)

		// Create a wrapper to capture status code
		wrapper := &responseWriter{
			ResponseWriter: writer,
			statusCode:     http.StatusOK,
		}

		// Log incoming request
		logger.WithFields(map[string]interface{}{
			"method":     req.Method,
			"path":       req.URL.Path,
			"user_agent": req.Header.Get("User-Agent"),
			"event_type": "http_request_start",
		}).Info("HTTP request started")

		next.ServeHTTP(wrapper, req)

		// Log completed request
		duration := time.Since(start)
		correlationID := logging.GetCorrelationID(ctx)

		logging.GetLogger().LogHTTPRequest(
			req.Method,
			req.URL.Path,
			req.Header.Get("User-Agent"),
			req.RemoteAddr,
			wrapper.statusCode,
			duration,
			correlationID,
		)
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
