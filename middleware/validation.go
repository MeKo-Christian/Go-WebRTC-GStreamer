package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Adaickalavan/Go-WebRTC-GStreamer/errors"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
	"github.com/pion/webrtc/v3"
)

const (
	maxRequestSize = 1 << 20 // 1MB maximum request size
	requestTimeout = 30 * time.Second
)

// ValidationConfig holds validation configuration
type ValidationConfig struct {
	MaxRequestSize  int64
	Timeout         time.Duration
	RequiredHeaders []string
}

// DefaultValidationConfig returns default validation configuration
func DefaultValidationConfig() *ValidationConfig {
	return &ValidationConfig{
		MaxRequestSize: maxRequestSize,
		Timeout:        requestTimeout,
		RequiredHeaders: []string{
			"Content-Type",
		},
	}
}

// ValidateRequestMiddleware validates incoming HTTP requests
func ValidateRequestMiddleware(config *ValidationConfig) func(http.Handler) http.Handler {
	if config == nil {
		config = DefaultValidationConfig()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx := request.Context()
			logger := logging.WithContext(ctx)
			correlationID := logging.GetCorrelationID(ctx)

			// Validate request size
			if request.ContentLength > config.MaxRequestSize {
				err := errors.NewValidationError(
					errors.ValidationRequestBad,
					"Request body too large",
					"content-length",
					fmt.Sprintf("max %d bytes", config.MaxRequestSize),
					request.ContentLength,
					nil,
				)
				err = errors.AddCorrelationID(err, correlationID).(*errors.ValidationError)

				logger.WithError(err).Warn("Request validation failed: body too large")
				http.Error(writer, "Request body too large", http.StatusRequestEntityTooLarge)
				return
			}

			// Validate required headers for JSON endpoints
			if request.URL.Path == "/sdp" {
				contentType := request.Header.Get("Content-Type")
				if !strings.Contains(contentType, "application/json") {
					err := errors.NewValidationError(
						errors.ValidationRequestBad,
						"Invalid content type",
						"content-type",
						"application/json required",
						contentType,
						nil,
					)
					err = errors.AddCorrelationID(err, correlationID).(*errors.ValidationError)

					logger.WithError(err).Warn("Request validation failed: invalid content type")
					http.Error(writer, "Content-Type must be application/json", http.StatusBadRequest)
					return
				}
			}

			// Add timeout context
			ctx, cancel := context.WithTimeout(ctx, config.Timeout)
			defer cancel()

			request = request.WithContext(ctx)
			next.ServeHTTP(writer, request)
		})
	}
}

// ValidateSDPOffer validates WebRTC SDP offers
func ValidateSDPOffer(offer webrtc.SessionDescription, correlationID string) error {
	// Validate SDP type
	if offer.Type != webrtc.SDPTypeOffer {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationSDPInvalid,
				"SDP must be an offer",
				"type",
				"offer",
				offer.Type.String(),
				nil,
			),
			correlationID,
		)
	}

	// Validate SDP is not empty
	if strings.TrimSpace(offer.SDP) == "" {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationSDPInvalid,
				"SDP content cannot be empty",
				"sdp",
				"non-empty",
				"",
				nil,
			),
			correlationID,
		)
	}

	// Basic SDP format validation
	sdpLines := strings.Split(offer.SDP, "\n")
	hasVersion := false
	hasOrigin := false
	hasMedia := false

	for _, line := range sdpLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "v="):
			hasVersion = true
		case strings.HasPrefix(line, "o="):
			hasOrigin = true
		case strings.HasPrefix(line, "m="):
			hasMedia = true
			// Validate video media type for our use case
			if !strings.HasPrefix(line, "m=video") {
				return errors.AddCorrelationID(
					errors.NewValidationError(
						errors.ValidationSDPInvalid,
						"Only video media is supported",
						"media",
						"m=video",
						line,
						nil,
					),
					correlationID,
				)
			}
		}
	}

	// Check required SDP fields
	if !hasVersion {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationSDPInvalid,
				"SDP missing version field",
				"sdp",
				"v= field required",
				"missing",
				nil,
			),
			correlationID,
		)
	}

	if !hasOrigin {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationSDPInvalid,
				"SDP missing origin field",
				"sdp",
				"o= field required",
				"missing",
				nil,
			),
			correlationID,
		)
	}

	if !hasMedia {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationSDPInvalid,
				"SDP missing media field",
				"sdp",
				"m= field required",
				"missing",
				nil,
			),
			correlationID,
		)
	}

	return nil
}

// ValidateJSONRequest validates and parses JSON request bodies
func ValidateJSONRequest(r *http.Request, target interface{}, correlationID string) error {
	logger := logging.WithContext(r.Context())

	// Read body with size limit
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestSize))
	if err != nil {
		validationErr := errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationRequestBad,
				"Failed to read request body",
				"body",
				"readable",
				"",
				err,
			),
			correlationID,
		)

		logger.WithError(validationErr).Error("Failed to read request body")
		return validationErr
	}

	// Validate JSON is not empty
	if len(body) == 0 {
		return errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationJSONMalformed,
				"Request body cannot be empty",
				"body",
				"non-empty JSON",
				"",
				nil,
			),
			correlationID,
		)
	}

	// Parse JSON
	if err := json.Unmarshal(body, target); err != nil {
		validationErr := errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationJSONMalformed,
				"Invalid JSON format",
				"body",
				"valid JSON",
				string(body[:minInt(100, len(body))]), // Log first 100 chars for debugging
				err,
			),
			correlationID,
		)

		logger.WithError(validationErr).Error("Failed to parse JSON request")
		return validationErr
	}

	return nil
}

// SanitizeInput sanitizes string inputs to prevent injection attacks
func SanitizeInput(input string) string {
	// Remove potentially dangerous characters
	replacements := map[string]string{
		"<":    "&lt;",
		">":    "&gt;",
		"&":    "&amp;",
		"\"":   "&quot;",
		"'":    "&#39;",
		"\x00": "", // Remove null bytes
	}

	sanitized := input
	for old, new := range replacements {
		sanitized = strings.ReplaceAll(sanitized, old, new)
	}

	// Limit length to prevent DoS
	if len(sanitized) > 1000 {
		sanitized = sanitized[:1000]
	}

	return sanitized
}

// ValidateConnectionName validates connection names from client requests
func ValidateConnectionName(name string, correlationID string) (string, string, error) {
	if strings.TrimSpace(name) == "" {
		return "", "", errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationRequestBad,
				"Connection name cannot be empty",
				"name",
				"non-empty",
				name,
				nil,
			),
			correlationID,
		)
	}

	// Split connection type and identifier
	parts := strings.Split(name, ":")
	if len(parts) != 2 {
		return "", "", errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationRequestBad,
				"Connection name must be in format 'type:id'",
				"name",
				"type:id format",
				name,
				nil,
			),
			correlationID,
		)
	}

	connType := strings.TrimSpace(parts[0])
	connID := strings.TrimSpace(parts[1])

	// Validate connection type
	validTypes := map[string]bool{
		"Publisher": true,
		"Client":    true,
	}

	if !validTypes[connType] {
		return "", "", errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationRequestBad,
				"Invalid connection type",
				"connection_type",
				"Publisher or Client",
				connType,
				nil,
			),
			correlationID,
		)
	}

	// Sanitize connection ID
	connID = SanitizeInput(connID)
	if len(connID) == 0 {
		return "", "", errors.AddCorrelationID(
			errors.NewValidationError(
				errors.ValidationRequestBad,
				"Connection ID cannot be empty after sanitization",
				"connection_id",
				"non-empty after sanitization",
				parts[1],
				nil,
			),
			correlationID,
		)
	}

	return connType, connID, nil
}

// Helper function for minimum value
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
