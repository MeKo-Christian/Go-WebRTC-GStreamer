# WebRTC SFU Server - Development Commands

# Default port from .env
PORT := env_var_or_default('LISTENINGADDR', '8588')

# Show available recipes
default:
    @just --list

# Build and install the executable
install:
    go install

# Run the server locally (after install)
run-installed:
    Go-WebRTC-GStreamer

# Run the server directly without installing
run:
    go run main.go

# Run with custom port
run-port PORT:
    LISTENINGADDR={{PORT}} go run main.go

# Build Docker image
docker-build:
    docker build -t webrtc .

# Run containerized server
docker-up:
    docker-compose up

# Run containerized server in detached mode
docker-up-d:
    docker-compose up -d

# Stop Docker containers
docker-down:
    docker-compose down

# Clean up Go modules
mod-tidy:
    go mod tidy

# Format all code using treefmt
fmt:
    treefmt --allow-missing-formatter

# Test if code is formatted
test-formatted:
    treefmt --fail-on-change --allow-missing-formatter

# Format Go code only
fmt-go:
    go fmt ./...

# Run Go vet
vet:
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run

# Run golangci-lint
lint-fix:
    golangci-lint run --fix

# Run all checks (format, lint, vet)
check: test-formatted lint vet

# Clean built binaries
clean:
    go clean

# Development setup - install and run
dev: install run-installed

# Full Docker rebuild and run
docker-rebuild: docker-down docker-build docker-up

# Quick test - build and run briefly with timeout
test-run:
    @echo "Testing server startup..."
    timeout 5s go run main.go || true
    @echo "Server test completed"

# Show server URLs
urls:
    @echo "Publisher: http://localhost:{{PORT}}/publish"
    @echo "Viewer:    http://localhost:{{PORT}}/join"
    @echo "SDP API:   http://localhost:{{PORT}}/sdp"