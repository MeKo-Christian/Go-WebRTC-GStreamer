// Package logging provides structured logging capabilities for the WebRTC SFU server.
// It includes context-aware logging, correlation IDs, and configurable output options.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// contextKey represents a context key type to avoid collisions
type contextKey string

const (
	// ContextKeyCorrelationID is the context key for correlation IDs
	ContextKeyCorrelationID contextKey = "correlation_id"
	// ContextKeyConnectionID is the context key for connection IDs
	ContextKeyConnectionID contextKey = "connection_id"
	// ContextKeyClientType is the context key for client type
	ContextKeyClientType contextKey = "client_type"
	// ContextKeyRemoteAddr is the context key for remote address
	ContextKeyRemoteAddr contextKey = "remote_addr"
)

// Logger wraps logrus.Logger with additional functionality
type Logger struct {
	*logrus.Logger
}

// NewLogger creates a new structured logger
func NewLogger() *Logger {
	logger := logrus.New()

	// Set JSON formatter for structured logging
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "level",
			logrus.FieldKeyMsg:   "message",
		},
	})

	// Set log level based on environment
	logLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch logLevel {
	case "debug":
		logger.SetLevel(logrus.DebugLevel)
	case "info":
		logger.SetLevel(logrus.InfoLevel)
	case "warn", "warning":
		logger.SetLevel(logrus.WarnLevel)
	case "error":
		logger.SetLevel(logrus.ErrorLevel)
	default:
		logger.SetLevel(logrus.InfoLevel) // Default to info
	}

	// Set output based on environment
	if os.Getenv("LOG_OUTPUT") == "file" {
		file, err := os.OpenFile("webrtc-sfu.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			logger.Warning("Failed to open log file, using stdout")
		} else {
			logger.SetOutput(file)
		}
	}

	return &Logger{Logger: logger}
}

// WithContext creates a logger with context values
func (l *Logger) WithContext(ctx context.Context) *logrus.Entry {
	entry := l.WithFields(logrus.Fields{})

	if correlationID := ctx.Value(ContextKeyCorrelationID); correlationID != nil {
		entry = entry.WithField("correlation_id", correlationID)
	}

	if connectionID := ctx.Value(ContextKeyConnectionID); connectionID != nil {
		entry = entry.WithField("connection_id", connectionID)
	}

	if clientType := ctx.Value(ContextKeyClientType); clientType != nil {
		entry = entry.WithField("client_type", clientType)
	}

	if remoteAddr := ctx.Value(ContextKeyRemoteAddr); remoteAddr != nil {
		entry = entry.WithField("remote_addr", remoteAddr)
	}

	return entry
}

// WithConnectionID creates a logger with connection ID
func (l *Logger) WithConnectionID(connectionID string) *logrus.Entry {
	return l.WithField("connection_id", connectionID)
}

// WithClientType creates a logger with client type
func (l *Logger) WithClientType(clientType string) *logrus.Entry {
	return l.WithField("client_type", clientType)
}

// WithCorrelationID creates a logger with correlation ID
func (l *Logger) WithCorrelationID(correlationID string) *logrus.Entry {
	return l.WithField("correlation_id", correlationID)
}

// WithError creates a logger with error information
func (l *Logger) WithError(err error) *logrus.Entry {
	entry := l.Logger.WithError(err)

	// Add custom error information if it's our custom error type
	if customErr, ok := err.(interface{ Error() string }); ok {
		entry = entry.WithField("error_type", fmt.Sprintf("%T", customErr))
	}

	return entry
}

// WithWebRTCState creates a logger with WebRTC state information
func (l *Logger) WithWebRTCState(connectionState, iceState string) *logrus.Entry {
	return l.WithFields(logrus.Fields{
		"webrtc_connection_state": connectionState,
		"webrtc_ice_state":        iceState,
	})
}

// WithDuration creates a logger with duration information
func (l *Logger) WithDuration(duration time.Duration) *logrus.Entry {
	return l.WithFields(logrus.Fields{
		"duration_ms": duration.Milliseconds(),
		"duration":    duration.String(),
	})
}

// LogWebRTCEvent logs WebRTC-specific events
func (l *Logger) LogWebRTCEvent(level logrus.Level, connectionID, clientType, event, message string, fields map[string]interface{}) {
	entry := l.WithFields(logrus.Fields{
		"connection_id": connectionID,
		"client_type":   clientType,
		"event_type":    "webrtc",
		"event":         event,
	})

	for key, value := range fields {
		entry = entry.WithField(key, value)
	}

	entry.Log(level, message)
}

// LogConnectionEvent logs connection-related events
func (l *Logger) LogConnectionEvent(level logrus.Level, connectionID, remoteAddr, event, message string, fields map[string]interface{}) {
	entry := l.WithFields(logrus.Fields{
		"connection_id": connectionID,
		"remote_addr":   remoteAddr,
		"event_type":    "connection",
		"event":         event,
	})

	for key, value := range fields {
		entry = entry.WithField(key, value)
	}

	entry.Log(level, message)
}

// LogHTTPRequest logs HTTP request information
func (l *Logger) LogHTTPRequest(method, path, userAgent, remoteAddr string, statusCode int, duration time.Duration, correlationID string) {
	l.Logger.WithFields(logrus.Fields{
		"event_type":     "http_request",
		"method":         method,
		"path":           path,
		"status_code":    statusCode,
		"duration_ms":    duration.Milliseconds(),
		"user_agent":     userAgent,
		"remote_addr":    remoteAddr,
		"correlation_id": correlationID,
	}).Info("HTTP request processed")
}

// LogError logs an error with appropriate context
func (l *Logger) LogError(err error, message string, fields map[string]interface{}) {
	entry := l.WithError(err)

	for key, value := range fields {
		entry = entry.WithField(key, value)
	}

	entry.Error(message)
}

// LogPanic logs panic recovery information
func (l *Logger) LogPanic(recovered interface{}, stackTrace string) {
	l.Logger.WithFields(logrus.Fields{
		"event_type":  "panic_recovery",
		"panic_value": recovered,
		"stack_trace": stackTrace,
	}).Error("Panic recovered")
}

// GenerateCorrelationID generates a unique correlation ID
func GenerateCorrelationID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID if random fails
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(bytes)
}

// AddCorrelationIDToContext adds a correlation ID to context
func AddCorrelationIDToContext(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, ContextKeyCorrelationID, correlationID)
}

// AddConnectionIDToContext adds a connection ID to context
func AddConnectionIDToContext(ctx context.Context, connectionID string) context.Context {
	return context.WithValue(ctx, ContextKeyConnectionID, connectionID)
}

// AddClientTypeToContext adds a client type to context
func AddClientTypeToContext(ctx context.Context, clientType string) context.Context {
	return context.WithValue(ctx, ContextKeyClientType, clientType)
}

// AddRemoteAddrToContext adds a remote address to context
func AddRemoteAddrToContext(ctx context.Context, remoteAddr string) context.Context {
	return context.WithValue(ctx, ContextKeyRemoteAddr, remoteAddr)
}

// GetCorrelationID extracts correlation ID from context
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(ContextKeyCorrelationID).(string); ok {
		return id
	}
	return ""
}

// SetLogOutput sets the output destination for the logger
func (l *Logger) SetLogOutput(output io.Writer) {
	l.SetOutput(output)
}

// SetLogLevel sets the log level
func (l *Logger) SetLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		l.SetLevel(logrus.DebugLevel)
	case "info":
		l.SetLevel(logrus.InfoLevel)
	case "warn", "warning":
		l.SetLevel(logrus.WarnLevel)
	case "error":
		l.SetLevel(logrus.ErrorLevel)
	default:
		l.SetLevel(logrus.InfoLevel)
	}
}

// Global logger instance
var defaultLogger *Logger

// Init initializes the global logger
func Init() {
	defaultLogger = NewLogger()
}

// GetLogger returns the global logger instance
func GetLogger() *Logger {
	if defaultLogger == nil {
		Init()
	}
	return defaultLogger
}

// WithContext returns a logger with context (convenience function)
func WithContext(ctx context.Context) *logrus.Entry {
	return GetLogger().WithContext(ctx)
}

// WithConnectionID returns a logger with connection ID (convenience function)
func WithConnectionID(connectionID string) *logrus.Entry {
	return GetLogger().WithConnectionID(connectionID)
}

// WithError returns a logger with error (convenience function)
func WithError(err error) *logrus.Entry {
	return GetLogger().WithError(err)
}
