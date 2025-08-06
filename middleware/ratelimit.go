package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Adaickalavan/Go-WebRTC-GStreamer/errors"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
	"golang.org/x/time/rate"
)

// RateLimiter interface for different rate limiting strategies
type RateLimiter interface {
	Allow(key string) (bool, time.Duration)
	Cleanup()
}

// TokenBucketLimiter implements token bucket rate limiting
type TokenBucketLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rate     rate.Limit
	burst    int
	cleanup  time.Duration
	lastSeen map[string]time.Time
}

// NewTokenBucketLimiter creates a new token bucket rate limiter
func NewTokenBucketLimiter(rps rate.Limit, burst int, cleanupInterval time.Duration) *TokenBucketLimiter {
	limiter := &TokenBucketLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rps,
		burst:    burst,
		cleanup:  cleanupInterval,
		lastSeen: make(map[string]time.Time),
	}

	// Start cleanup goroutine
	go limiter.cleanupRoutine()

	return limiter
}

// Allow checks if a request should be allowed for the given key
func (tbl *TokenBucketLimiter) Allow(key string) (bool, time.Duration) {
	tbl.mu.RLock()
	limiter, exists := tbl.limiters[key]
	tbl.mu.RUnlock()

	if !exists {
		tbl.mu.Lock()
		// Double-check after acquiring write lock
		limiter, exists = tbl.limiters[key]
		if !exists {
			limiter = rate.NewLimiter(tbl.rate, tbl.burst)
			tbl.limiters[key] = limiter
		}
		tbl.lastSeen[key] = time.Now()
		tbl.mu.Unlock()
	} else {
		tbl.mu.Lock()
		tbl.lastSeen[key] = time.Now()
		tbl.mu.Unlock()
	}

	reservation := limiter.Reserve()
	if !reservation.OK() {
		return false, time.Hour // Return a large delay for impossible requests
	}

	delay := reservation.Delay()
	if delay > 0 {
		reservation.Cancel() // Cancel the reservation since we're denying the request
		return false, delay
	}

	return true, 0
}

// Cleanup removes old limiters to prevent memory leaks
func (tbl *TokenBucketLimiter) Cleanup() {
	tbl.mu.Lock()
	defer tbl.mu.Unlock()

	cutoff := time.Now().Add(-tbl.cleanup)
	for key, lastSeen := range tbl.lastSeen {
		if lastSeen.Before(cutoff) {
			delete(tbl.limiters, key)
			delete(tbl.lastSeen, key)
		}
	}
}

// cleanupRoutine runs periodic cleanup
func (tbl *TokenBucketLimiter) cleanupRoutine() {
	ticker := time.NewTicker(tbl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		tbl.Cleanup()
	}
}

// RateLimitConfig holds rate limiting configuration
type RateLimitConfig struct {
	RequestsPerSecond float64
	Burst             int
	CleanupInterval   time.Duration
	KeyGenerator      func(*http.Request) string
	SkipPaths         []string
}

// DefaultRateLimitConfig returns default rate limiting configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		RequestsPerSecond: 10.0, // 10 requests per second
		Burst:             20,   // Allow burst of 20 requests
		CleanupInterval:   5 * time.Minute,
		KeyGenerator:      DefaultKeyGenerator,
		SkipPaths: []string{
			"/static/",
			"/favicon.ico",
		},
	}
}

// DefaultKeyGenerator generates rate limiting keys based on client IP
func DefaultKeyGenerator(req *http.Request) string {
	// Try to get real IP from headers (for proxies)
	forwarded := req.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		// Take the first IP in the comma-separated list
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	realIP := req.Header.Get("X-Real-IP")
	if realIP != "" && net.ParseIP(realIP) != nil {
		return realIP
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// RateLimitMiddleware creates rate limiting middleware
func RateLimitMiddleware(config *RateLimitConfig) func(http.Handler) http.Handler {
	if config == nil {
		config = DefaultRateLimitConfig()
	}

	limiter := NewTokenBucketLimiter(
		rate.Limit(config.RequestsPerSecond),
		config.Burst,
		config.CleanupInterval,
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx := request.Context()
			logger := logging.WithContext(ctx)
			correlationID := logging.GetCorrelationID(ctx)

			// Skip rate limiting for certain paths
			for _, skipPath := range config.SkipPaths {
				if strings.HasPrefix(request.URL.Path, skipPath) {
					next.ServeHTTP(writer, request)
					return
				}
			}

			// Generate key for rate limiting
			key := config.KeyGenerator(request)

			// Check rate limit
			allowed, retryAfter := limiter.Allow(key)
			if !allowed {
				err := errors.NewRateLimitError(
					config.Burst,
					retryAfter,
					key,
				)
				err = errors.AddCorrelationID(err, correlationID).(*errors.RateLimitError)

				// Set retry-after header
				writer.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
				writer.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", config.RequestsPerSecond))
				writer.Header().Set("X-RateLimit-Remaining", "0")

				logger.WithError(err).WithFields(map[string]interface{}{
					"client_ip":   key,
					"path":        request.URL.Path,
					"retry_after": retryAfter.Seconds(),
				}).Warn("Rate limit exceeded")

				http.Error(writer, "Rate limit exceeded. Please slow down.", http.StatusTooManyRequests)
				return
			}

			// Set rate limit headers for successful requests
			writer.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", config.RequestsPerSecond))

			next.ServeHTTP(writer, request)
		})
	}
}

// PathSpecificRateLimitConfig allows different rate limits for different paths
type PathSpecificRateLimitConfig struct {
	Paths   map[string]*RateLimitConfig
	Default *RateLimitConfig
}

// PathSpecificRateLimitMiddleware creates path-specific rate limiting middleware
func PathSpecificRateLimitMiddleware(config *PathSpecificRateLimitConfig) func(http.Handler) http.Handler {
	if config == nil || config.Default == nil {
		config = &PathSpecificRateLimitConfig{
			Default: DefaultRateLimitConfig(),
			Paths: map[string]*RateLimitConfig{
				"/sdp": {
					RequestsPerSecond: 2.0, // Stricter limit for SDP endpoint
					Burst:             5,
					CleanupInterval:   5 * time.Minute,
					KeyGenerator:      DefaultKeyGenerator,
				},
			},
		}
	}

	// Create limiters for each path
	limiters := make(map[string]*TokenBucketLimiter)

	for path, pathConfig := range config.Paths {
		limiters[path] = NewTokenBucketLimiter(
			rate.Limit(pathConfig.RequestsPerSecond),
			pathConfig.Burst,
			pathConfig.CleanupInterval,
		)
	}

	// Default limiter
	defaultLimiter := NewTokenBucketLimiter(
		rate.Limit(config.Default.RequestsPerSecond),
		config.Default.Burst,
		config.Default.CleanupInterval,
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			logger := logging.WithContext(ctx)
			correlationID := logging.GetCorrelationID(ctx)

			// Determine which limiter to use
			var limiter *TokenBucketLimiter
			var limitConfig *RateLimitConfig

			if pathLimiter, exists := limiters[req.URL.Path]; exists {
				limiter = pathLimiter
				limitConfig = config.Paths[req.URL.Path]
			} else {
				limiter = defaultLimiter
				limitConfig = config.Default
			}

			// Skip rate limiting for certain paths
			for _, skipPath := range limitConfig.SkipPaths {
				if strings.HasPrefix(req.URL.Path, skipPath) {
					next.ServeHTTP(writer, req)
					return
				}
			}

			// Generate key for rate limiting
			key := limitConfig.KeyGenerator(req)

			// Check rate limit
			allowed, retryAfter := limiter.Allow(key)
			if !allowed {
				err := errors.NewRateLimitError(
					limitConfig.Burst,
					retryAfter,
					key,
				)
				err = errors.AddCorrelationID(err, correlationID).(*errors.RateLimitError)

				// Set retry-after header
				writer.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
				writer.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", limitConfig.RequestsPerSecond))
				writer.Header().Set("X-RateLimit-Remaining", "0")

				logger.WithError(err).WithFields(map[string]interface{}{
					"client_ip":   key,
					"path":        req.URL.Path,
					"retry_after": retryAfter.Seconds(),
				}).Warn("Rate limit exceeded")

				http.Error(writer, "Rate limit exceeded. Please slow down.", http.StatusTooManyRequests)
				return
			}

			// Set rate limit headers for successful requests
			writer.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", limitConfig.RequestsPerSecond))

			next.ServeHTTP(writer, req)
		})
	}
}
