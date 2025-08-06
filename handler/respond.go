// Package handler provides HTTP response handling utilities for the WebRTC SFU server.
// It includes structured error responses, JSON marshaling, and template rendering.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	appErrors "github.com/Adaickalavan/Go-WebRTC-GStreamer/errors"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
)

// ErrorResponse represents a structured error response
type ErrorResponse struct {
	Error         ErrorDetail `json:"error"`
	CorrelationID string      `json:"correlation_id,omitempty"`
	Timestamp     time.Time   `json:"timestamp"`
}

// ErrorDetail contains detailed error information
type ErrorDetail struct {
	Code        string                 `json:"code"`
	Message     string                 `json:"message"`
	Description string                 `json:"description,omitempty"`
	Context     map[string]interface{} `json:"context,omitempty"`
	IsRetryable bool                   `json:"is_retryable"`
	RetryAfter  *int                   `json:"retry_after_seconds,omitempty"`
}

// SuccessResponse represents a structured success response
type SuccessResponse struct {
	Success       bool                   `json:"success"`
	Data          interface{}            `json:"data,omitempty"`
	Message       string                 `json:"message,omitempty"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
	Timestamp     time.Time              `json:"timestamp"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// RespondWithError sends a structured error response
func RespondWithError(w http.ResponseWriter, code int, msg string) {
	RespondWithCustomError(w, code, msg, "", nil)
}

// RespondWithCustomError sends a custom error response with correlation ID
func RespondWithCustomError(writer http.ResponseWriter, code int, msg, correlationID string, context map[string]interface{}) {
	errorResponse := ErrorResponse{
		Error: ErrorDetail{
			Code:        fmt.Sprintf("HTTP_%d", code),
			Message:     msg,
			Description: http.StatusText(code),
			Context:     context,
			IsRetryable: isHTTPErrorRetryable(code),
		},
		CorrelationID: correlationID,
		Timestamp:     time.Now(),
	}

	if retryAfter := getRetryAfterSeconds(code); retryAfter > 0 {
		errorResponse.Error.RetryAfter = &retryAfter
	}

	RespondWithJSON(writer, code, errorResponse)
}

// RespondWithAppError sends a response for application-specific errors
func RespondWithAppError(writer http.ResponseWriter, err error, correlationID string) {
	logger := logging.GetLogger()

	// Handle different types of application errors
	switch errType := err.(type) {
	case *appErrors.WebRTCError:
		httpCode := appErrors.HTTPStatusCode(errType.Code)
		errorResponse := ErrorResponse{
			Error: ErrorDetail{
				Code:        string(errType.Code),
				Message:     errType.Message,
				Description: appErrors.ErrorDescription(errType.Code),
				Context:     errType.Context,
				IsRetryable: appErrors.IsRetryable(errType.Code),
			},
			CorrelationID: correlationID,
			Timestamp:     time.Now(),
		}

		if errType.IsTemporary {
			retryDelay := appErrors.RetryDelay(errType.Code)
			errorResponse.Error.RetryAfter = &retryDelay
		}

		// Add WebRTC-specific context
		if errorResponse.Error.Context == nil {
			errorResponse.Error.Context = make(map[string]interface{})
		}
		if errType.ConnectionID != "" {
			errorResponse.Error.Context["connection_id"] = errType.ConnectionID
		}
		if errType.ClientType != "" {
			errorResponse.Error.Context["client_type"] = errType.ClientType
		}
		if errType.ICEState != "" {
			errorResponse.Error.Context["ice_state"] = errType.ICEState
		}

		logger.WithError(errType).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"http_status":    httpCode,
		}).Error("WebRTC error occurred")

		RespondWithJSON(writer, httpCode, errorResponse)

	case *appErrors.ValidationError:
		httpCode := appErrors.HTTPStatusCode(errType.Code)
		errorResponse := ErrorResponse{
			Error: ErrorDetail{
				Code:        string(errType.Code),
				Message:     errType.Message,
				Description: appErrors.ErrorDescription(errType.Code),
				Context:     errType.Context,
				IsRetryable: false, // Validation errors are not retryable
			},
			CorrelationID: correlationID,
			Timestamp:     time.Now(),
		}

		// Add validation-specific context
		if errorResponse.Error.Context == nil {
			errorResponse.Error.Context = make(map[string]interface{})
		}
		if errType.Field != "" {
			errorResponse.Error.Context["field"] = errType.Field
		}
		if errType.Constraint != "" {
			errorResponse.Error.Context["constraint"] = errType.Constraint
		}
		if errType.Value != nil {
			errorResponse.Error.Context["invalid_value"] = errType.Value
		}

		logger.WithError(errType).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"http_status":    httpCode,
			"field":          errType.Field,
		}).Warn("Validation error occurred")

		RespondWithJSON(writer, httpCode, errorResponse)

	case *appErrors.ConnectionError:
		httpCode := appErrors.HTTPStatusCode(errType.Code)
		errorResponse := ErrorResponse{
			Error: ErrorDetail{
				Code:        string(errType.Code),
				Message:     errType.Message,
				Description: appErrors.ErrorDescription(errType.Code),
				Context:     errType.Context,
				IsRetryable: appErrors.IsRetryable(errType.Code),
			},
			CorrelationID: correlationID,
			Timestamp:     time.Now(),
		}

		if errType.IsTemporary {
			retryDelay := appErrors.RetryDelay(errType.Code)
			errorResponse.Error.RetryAfter = &retryDelay
		}

		// Add connection-specific context
		if errorResponse.Error.Context == nil {
			errorResponse.Error.Context = make(map[string]interface{})
		}
		if errType.ConnectionID != "" {
			errorResponse.Error.Context["connection_id"] = errType.ConnectionID
		}
		if errType.RemoteAddr != "" {
			errorResponse.Error.Context["remote_addr"] = errType.RemoteAddr
		}
		if errType.Duration > 0 {
			errorResponse.Error.Context["duration_ms"] = errType.Duration.Milliseconds()
		}

		logger.WithError(errType).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"http_status":    httpCode,
			"connection_id":  errType.ConnectionID,
		}).Error("Connection error occurred")

		RespondWithJSON(writer, httpCode, errorResponse)

	case *appErrors.RateLimitError:
		httpCode := appErrors.HTTPStatusCode(appErrors.RateLimitExceeded)

		// Set rate limit headers
		writer.Header().Set("Retry-After", fmt.Sprintf("%.0f", errType.RetryAfter.Seconds()))
		writer.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", errType.Limit))
		writer.Header().Set("X-RateLimit-Remaining", "0")

		retryAfterSeconds := int(errType.RetryAfter.Seconds())
		errorResponse := ErrorResponse{
			Error: ErrorDetail{
				Code:        string(errType.Code),
				Message:     errType.Message,
				Description: appErrors.ErrorDescription(errType.Code),
				Context:     errType.Context,
				IsRetryable: true,
				RetryAfter:  &retryAfterSeconds,
			},
			CorrelationID: correlationID,
			Timestamp:     time.Now(),
		}

		// Add rate limit specific context
		if errorResponse.Error.Context == nil {
			errorResponse.Error.Context = make(map[string]interface{})
		}
		errorResponse.Error.Context["limit"] = errType.Limit
		errorResponse.Error.Context["remote_addr"] = errType.RemoteAddr

		logger.WithError(errType).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"remote_addr":    errType.RemoteAddr,
			"limit":          errType.Limit,
		}).Warn("Rate limit error occurred")

		RespondWithJSON(writer, httpCode, errorResponse)

	case *appErrors.ServerError:
		httpCode := appErrors.HTTPStatusCode(errType.Code)
		errorResponse := ErrorResponse{
			Error: ErrorDetail{
				Code:        string(errType.Code),
				Message:     errType.Message,
				Description: appErrors.ErrorDescription(errType.Code),
				Context:     errType.Context,
				IsRetryable: appErrors.IsRetryable(errType.Code),
			},
			CorrelationID: correlationID,
			Timestamp:     time.Now(),
		}

		if errType.IsTemporary {
			retryDelay := appErrors.RetryDelay(errType.Code)
			errorResponse.Error.RetryAfter = &retryDelay
		}

		// Add server-specific context
		if errorResponse.Error.Context == nil {
			errorResponse.Error.Context = make(map[string]interface{})
		}
		if errType.Component != "" {
			errorResponse.Error.Context["component"] = errType.Component
		}

		logger.WithError(errType).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"http_status":    httpCode,
			"component":      errType.Component,
		}).Error("Server error occurred")

		RespondWithJSON(writer, httpCode, errorResponse)

	default:
		// Handle unknown error types
		logger.WithError(err).WithFields(map[string]interface{}{
			"correlation_id": correlationID,
			"error_type":     fmt.Sprintf("%T", err),
		}).Error("Unknown error type occurred")

		RespondWithCustomError(writer, http.StatusInternalServerError, err.Error(), correlationID, nil)
	}
}

// RespondWithSuccess sends a structured success response
func RespondWithSuccess(writer http.ResponseWriter, data interface{}, message, correlationID string, metadata map[string]interface{}) {
	successResponse := SuccessResponse{
		Success:       true,
		Data:          data,
		Message:       message,
		CorrelationID: correlationID,
		Timestamp:     time.Now(),
		Metadata:      metadata,
	}

	RespondWithJSON(writer, http.StatusOK, successResponse)
}

// RespondWithJSON sends a JSON response
func RespondWithJSON(writer http.ResponseWriter, code int, payload interface{}) {
	response, err := json.MarshalIndent(payload, "", " ")
	if err != nil {
		// Fallback error response
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusInternalServerError)
		if _, writeErr := writer.Write([]byte(`{"error": {"code": "ENCODING_ERROR", "message": "Failed to encode response"}}`)); writeErr != nil {
			// Log the write error but cannot do much else at this point
			logging.GetLogger().WithError(writeErr).Error("Failed to write error response")
		}
		return
	}

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(code)
	if _, err := writer.Write(response); err != nil {
		logging.GetLogger().WithError(err).Error("Failed to write JSON response")
	}
}

// Push the given resource to the client
func Push(writer http.ResponseWriter, resource string) {
	logger := logging.GetLogger()
	err := errors.New("Push is not supported")
	pusher, ok := writer.(http.Pusher)
	if ok {
		if err = pusher.Push(resource, nil); err == nil {
			return
		}
	}
	logger.WithError(err).WithField("resource", resource).Debug("HTTP/2 push not supported or failed")
}

// Render a template with enhanced error handling
func Render(writer http.ResponseWriter, r *http.Request, tpl *template.Template, data interface{}) {
	ctx := r.Context()
	logger := logging.WithContext(ctx)
	correlationID := logging.GetCorrelationID(ctx)

	buf := new(bytes.Buffer)
	if err := tpl.Execute(buf, data); err != nil {
		templateErr := appErrors.NewServerError(
			appErrors.TemplateRenderError,
			"Failed to render template",
			"template_engine",
			err,
		)
		templateErr = appErrors.AddCorrelationID(templateErr, correlationID).(*appErrors.ServerError)

		logger.WithError(templateErr).Error("Template rendering failed")
		RespondWithAppError(writer, templateErr, correlationID)
		return
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := writer.Write(buf.Bytes()); err != nil {
		logging.WithContext(ctx).WithError(err).Error("Failed to write rendered template")
	}
}

// RenderWithCorrelationID renders a template with correlation ID context
func RenderWithCorrelationID(w http.ResponseWriter, r *http.Request, tpl *template.Template, data interface{}, correlationID string) {
	ctx := logging.AddCorrelationIDToContext(r.Context(), correlationID)
	r = r.WithContext(ctx)
	Render(w, r, tpl, data)
}

// Helper functions

// isHTTPErrorRetryable determines if an HTTP error code indicates a retryable error
func isHTTPErrorRetryable(code int) bool {
	retryableCodes := map[int]bool{
		http.StatusRequestTimeout:      true,
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
		http.StatusGatewayTimeout:      true,
	}
	return retryableCodes[code]
}

// getRetryAfterSeconds returns suggested retry delay for HTTP error codes
func getRetryAfterSeconds(code int) int {
	retryDelays := map[int]int{
		http.StatusRequestTimeout:      5,
		http.StatusTooManyRequests:     60,
		http.StatusInternalServerError: 10,
		http.StatusBadGateway:          30,
		http.StatusServiceUnavailable:  60,
		http.StatusGatewayTimeout:      15,
	}

	if delay, exists := retryDelays[code]; exists {
		return delay
	}
	return 0
}

// ExtractCorrelationID extracts correlation ID from request context
func ExtractCorrelationID(ctx context.Context) string {
	return logging.GetCorrelationID(ctx)
}
