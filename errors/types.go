package errors

import (
	"fmt"
	"time"
)

// ErrorCode represents different types of errors in the system
type ErrorCode string

const (
	// WebRTCConnectionFailed indicates a WebRTC connection could not be established
	WebRTCConnectionFailed ErrorCode = "WEBRTC_CONNECTION_FAILED"
	// WebRTCOfferInvalid indicates the WebRTC offer received was invalid
	WebRTCOfferInvalid ErrorCode = "WEBRTC_OFFER_INVALID"
	// WebRTCAnswerFailed indicates a WebRTC answer could not be created
	WebRTCAnswerFailed ErrorCode = "WEBRTC_ANSWER_FAILED"
	// WebRTCTrackFailed indicates a WebRTC track operation failed
	WebRTCTrackFailed ErrorCode = "WEBRTC_TRACK_FAILED"

	// ValidationSDPInvalid indicates the SDP (Session Description Protocol) data was invalid
	ValidationSDPInvalid    ErrorCode = "VALIDATION_SDP_INVALID"
	ValidationRequestBad    ErrorCode = "VALIDATION_REQUEST_BAD"
	ValidationJSONMalformed ErrorCode = "VALIDATION_JSON_MALFORMED"

	// ConnectionTimeout indicates a connection operation timed out
	ConnectionTimeout   ErrorCode = "CONNECTION_TIMEOUT"
	ConnectionLost      ErrorCode = "CONNECTION_LOST"
	ConnectionLimit     ErrorCode = "CONNECTION_LIMIT"
	ConnectionICEFailed ErrorCode = "CONNECTION_ICE_FAILED"

	// ServerInternal indicates an internal server error occurred
	ServerInternal    ErrorCode = "SERVER_INTERNAL"
	ServerOverloaded  ErrorCode = "SERVER_OVERLOADED"
	ServerUnavailable ErrorCode = "SERVER_UNAVAILABLE"

	// RateLimitExceeded indicates the client has exceeded the rate limit
	RateLimitExceeded ErrorCode = "RATE_LIMIT_EXCEEDED"

	// TemplateParseError indicates a template could not be parsed
	TemplateParseError  ErrorCode = "TEMPLATE_PARSE_ERROR"
	TemplateRenderError ErrorCode = "TEMPLATE_RENDER_ERROR"
)

// BaseError represents the base error structure with common fields
type BaseError struct {
	Code          ErrorCode              `json:"code"`
	Message       string                 `json:"message"`
	Context       map[string]interface{} `json:"context,omitempty"`
	Timestamp     time.Time              `json:"timestamp"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
	IsTemporary   bool                   `json:"is_temporary"`
	OriginalError error                  `json:"-"`
}

func (e *BaseError) Error() string {
	if e.OriginalError != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.OriginalError)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *BaseError) Unwrap() error {
	return e.OriginalError
}

// WebRTCError represents WebRTC-specific errors
type WebRTCError struct {
	*BaseError
	ConnectionID string `json:"connection_id,omitempty"`
	ClientType   string `json:"client_type,omitempty"`
	ICEState     string `json:"ice_state,omitempty"`
}

// NewWebRTCError creates a new WebRTC-specific error with connection details
func NewWebRTCError(code ErrorCode, message string, connectionID, clientType string, originalErr error) *WebRTCError {
	return &WebRTCError{
		BaseError: &BaseError{
			Code:          code,
			Message:       message,
			Timestamp:     time.Now(),
			IsTemporary:   isTemporary(code),
			OriginalError: originalErr,
			Context:       make(map[string]interface{}),
		},
		ConnectionID: connectionID,
		ClientType:   clientType,
	}
}

// ValidationError represents input validation errors
type ValidationError struct {
	*BaseError
	Field      string      `json:"field,omitempty"`
	Value      interface{} `json:"value,omitempty"`
	Constraint string      `json:"constraint,omitempty"`
}

// NewValidationError creates a new validation error with field-specific details
func NewValidationError(code ErrorCode, message, field, constraint string, value interface{}, originalErr error) *ValidationError {
	return &ValidationError{
		BaseError: &BaseError{
			Code:          code,
			Message:       message,
			Timestamp:     time.Now(),
			IsTemporary:   false, // Validation errors are not temporary
			OriginalError: originalErr,
			Context:       make(map[string]interface{}),
		},
		Field:      field,
		Value:      value,
		Constraint: constraint,
	}
}

// ConnectionError represents connection-related errors
type ConnectionError struct {
	*BaseError
	ConnectionID string        `json:"connection_id,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	RemoteAddr   string        `json:"remote_addr,omitempty"`
}

// NewConnectionError creates a new connection-related error with timing and address details
func NewConnectionError(code ErrorCode, message, connectionID, remoteAddr string, duration time.Duration, originalErr error) *ConnectionError {
	return &ConnectionError{
		BaseError: &BaseError{
			Code:          code,
			Message:       message,
			Timestamp:     time.Now(),
			IsTemporary:   isTemporary(code),
			OriginalError: originalErr,
			Context:       make(map[string]interface{}),
		},
		ConnectionID: connectionID,
		Duration:     duration,
		RemoteAddr:   remoteAddr,
	}
}

// ServerError represents server-side errors
type ServerError struct {
	*BaseError
	Component string `json:"component,omitempty"`
}

// NewServerError creates a new server error with component information
func NewServerError(code ErrorCode, message, component string, originalErr error) *ServerError {
	return &ServerError{
		BaseError: &BaseError{
			Code:          code,
			Message:       message,
			Timestamp:     time.Now(),
			IsTemporary:   isTemporary(code),
			OriginalError: originalErr,
			Context:       make(map[string]interface{}),
		},
		Component: component,
	}
}

// RateLimitError represents rate limiting errors
type RateLimitError struct {
	*BaseError
	Limit      int           `json:"limit"`
	RetryAfter time.Duration `json:"retry_after"`
	RemoteAddr string        `json:"remote_addr,omitempty"`
}

// NewRateLimitError creates a new rate limiting error with retry timing
func NewRateLimitError(limit int, retryAfter time.Duration, remoteAddr string) *RateLimitError {
	return &RateLimitError{
		BaseError: &BaseError{
			Code:        RateLimitExceeded,
			Message:     fmt.Sprintf("Rate limit exceeded: %d requests", limit),
			Timestamp:   time.Now(),
			IsTemporary: true, // Rate limit errors are temporary
			Context:     make(map[string]interface{}),
		},
		Limit:      limit,
		RetryAfter: retryAfter,
		RemoteAddr: remoteAddr,
	}
}

// isTemporary determines if an error type is temporary based on its code
func isTemporary(code ErrorCode) bool {
	temporaryCodes := map[ErrorCode]bool{
		WebRTCConnectionFailed: true,
		ConnectionTimeout:      true,
		ConnectionLost:         true,
		ConnectionICEFailed:    true,
		ServerOverloaded:       true,
		ServerUnavailable:      true,
		RateLimitExceeded:      true,
	}
	return temporaryCodes[code]
}

// AddContext adds contextual information to any error type
func AddContext(err error, key string, value interface{}) error {
	switch errType := err.(type) {
	case *WebRTCError:
		errType.Context[key] = value
		return errType
	case *ValidationError:
		errType.Context[key] = value
		return errType
	case *ConnectionError:
		errType.Context[key] = value
		return errType
	case *ServerError:
		errType.Context[key] = value
		return errType
	case *RateLimitError:
		errType.Context[key] = value
		return errType
	default:
		return err
	}
}

// AddCorrelationID adds a correlation ID to any error type
func AddCorrelationID(err error, correlationID string) error {
	switch errType := err.(type) {
	case *WebRTCError:
		errType.CorrelationID = correlationID
		return errType
	case *ValidationError:
		errType.CorrelationID = correlationID
		return errType
	case *ConnectionError:
		errType.CorrelationID = correlationID
		return errType
	case *ServerError:
		errType.CorrelationID = correlationID
		return errType
	case *RateLimitError:
		errType.CorrelationID = correlationID
		return errType
	default:
		return err
	}
}
