// Package errors provides structured error handling for the WebRTC SFU server.
// It defines error codes, HTTP status mapping, and utilities for error management.
package errors

import "net/http"

// HTTPStatusCode maps error codes to appropriate HTTP status codes
func HTTPStatusCode(code ErrorCode) int {
	statusMap := map[ErrorCode]int{
		// WebRTC errors - mostly internal server errors
		WebRTCConnectionFailed: http.StatusInternalServerError,
		WebRTCOfferInvalid:     http.StatusBadRequest,
		WebRTCAnswerFailed:     http.StatusInternalServerError,
		WebRTCTrackFailed:      http.StatusInternalServerError,

		// Validation errors - bad request
		ValidationSDPInvalid:    http.StatusBadRequest,
		ValidationRequestBad:    http.StatusBadRequest,
		ValidationJSONMalformed: http.StatusBadRequest,

		// Connection errors
		ConnectionTimeout:   http.StatusRequestTimeout,
		ConnectionLost:      http.StatusServiceUnavailable,
		ConnectionLimit:     http.StatusServiceUnavailable,
		ConnectionICEFailed: http.StatusInternalServerError,

		// Server errors
		ServerInternal:    http.StatusInternalServerError,
		ServerOverloaded:  http.StatusServiceUnavailable,
		ServerUnavailable: http.StatusServiceUnavailable,

		// Rate limiting
		RateLimitExceeded: http.StatusTooManyRequests,

		// Template errors
		TemplateParseError:  http.StatusInternalServerError,
		TemplateRenderError: http.StatusInternalServerError,
	}

	if status, exists := statusMap[code]; exists {
		return status
	}
	return http.StatusInternalServerError // Default fallback
}

// ErrorDescription provides user-friendly descriptions for error codes
func ErrorDescription(code ErrorCode) string {
	descriptions := map[ErrorCode]string{
		// WebRTC errors
		WebRTCConnectionFailed: "Failed to establish WebRTC connection",
		WebRTCOfferInvalid:     "Invalid WebRTC offer received",
		WebRTCAnswerFailed:     "Failed to create WebRTC answer",
		WebRTCTrackFailed:      "Failed to manage WebRTC track",

		// Validation errors
		ValidationSDPInvalid:    "Invalid Session Description Protocol data",
		ValidationRequestBad:    "Request validation failed",
		ValidationJSONMalformed: "Malformed JSON in request body",

		// Connection errors
		ConnectionTimeout:   "Connection timed out",
		ConnectionLost:      "Connection lost unexpectedly",
		ConnectionLimit:     "Maximum number of connections reached",
		ConnectionICEFailed: "ICE connection failed",

		// Server errors
		ServerInternal:    "Internal server error occurred",
		ServerOverloaded:  "Server is currently overloaded",
		ServerUnavailable: "Server is temporarily unavailable",

		// Rate limiting
		RateLimitExceeded: "Rate limit exceeded, please slow down",

		// Template errors
		TemplateParseError:  "Failed to parse template",
		TemplateRenderError: "Failed to render template",
	}

	if desc, exists := descriptions[code]; exists {
		return desc
	}
	return "An unknown error occurred"
}

// IsRetryable determines if an error with the given code should be retried
func IsRetryable(code ErrorCode) bool {
	retryableCodes := map[ErrorCode]bool{
		WebRTCConnectionFailed: true,
		ConnectionTimeout:      true,
		ConnectionLost:         true,
		ConnectionICEFailed:    true,
		ServerOverloaded:       true,
		ServerUnavailable:      true,
		RateLimitExceeded:      true,
	}
	return retryableCodes[code]
}

// RetryDelay provides suggested retry delays for different error types
func RetryDelay(code ErrorCode) int {
	// Returns delay in seconds
	delayMap := map[ErrorCode]int{
		WebRTCConnectionFailed: 5,
		ConnectionTimeout:      3,
		ConnectionLost:         2,
		ConnectionICEFailed:    10,
		ServerOverloaded:       30,
		ServerUnavailable:      60,
		RateLimitExceeded:      60,
	}

	if delay, exists := delayMap[code]; exists {
		return delay
	}
	return 5 // Default delay
}
