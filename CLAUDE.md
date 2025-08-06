# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a WebRTC SFU (Selective Forwarding Unit) server built in Go that extends the pion-webrtc minimal SFU example. It provides video broadcasting capabilities where one publisher can stream to multiple clients through a centralized server.

## Development Commands

### Using Just (Recommended)
This project includes a `justfile` for convenient development commands:

- `just` - Show all available commands
- `just run` - Run the server directly without installing
- `just run-port PORT` - Run with custom port (e.g., `just run-port 9000`)
- `just install` - Build and install the executable
- `just run-installed` - Run the server locally (after install)
- `just docker-build` - Build Docker image
- `just docker-up` - Run containerized server
- `just docker-up-d` - Run containerized server in detached mode
- `just docker-down` - Stop Docker containers
- `just docker-rebuild` - Full Docker rebuild and run
- `just mod-tidy` - Clean up Go modules
- `just fmt` - Format Go code
- `just vet` - Run Go vet
- `just clean` - Clean built binaries
- `just dev` - Development setup (install and run)
- `just test-run` - Quick test server startup with timeout
- `just urls` - Show server URLs (publisher, viewer, SDP API)

### Manual Commands (Alternative)
- `go install` - Build and install the executable
- `Go-WebRTC-GStreamer` - Run the server locally (after install)
- `go run main.go` - Run directly without installing
- `docker build -t webrtc .` - Build Docker image
- `docker-compose up` - Run containerized server

### Environment Configuration
- Copy `.env` file and modify `LISTENINGADDR` for custom port (default: 8588)
- Server listens on the port specified in `LISTENINGADDR` environment variable

## Architecture

### Core Components

**Server Architecture (main.go)**:
- `sdpServer` struct manages WebRTC peer connections and signaling
- Uses pion-webrtc library with VP8 codec only for simplicity
- Maintains a single shared track that publishers write to and clients read from
- Implements automatic recovery with panic handling and reconnection logic

**HTTP Endpoints**:
- `/publish` - Publisher webpage (captures webcam, sends video)
- `/join` - Client webpage (receives and displays video)
- `/sdp` - WebRTC signaling endpoint for SDP exchange
- `/static/` - Static file serving for JS/CSS

**WebRTC Flow**:
1. Publishers connect and add their video track to the server's shared track
2. Clients connect and receive the shared track
3. Server forwards RTP packets from publisher to all connected clients
4. Uses PLI (Picture Loss Indication) every second to request keyframes

### Client-Side (static/js/connect.js)

**Publisher Flow**:
- Captures webcam using `getUserMedia()`
- Creates WebRTC offer and exchanges SDP through `/sdp` endpoint
- Automatically handles ICE candidate gathering

**Client Flow**:
- Creates receive-only transceiver for video
- Exchanges SDP with server to receive video stream
- Displays received video in HTML video element

### Key Technical Details

- **Codec**: VP8 only (simplifies proxy implementation)
- **Media**: Video only, no audio
- **STUN Server**: Uses Google's public STUN server
- **Sample Building**: Uses pion's samplebuilder with VP8 packets
- **Recovery**: Server automatically recovers from panics up to 1 retry
- **Timeouts**: HTTP server has 10s read/write timeouts

### File Structure
- `main.go` - Main server logic and WebRTC handling
- `handler/respond.go` - HTTP response utilities and template rendering
- `template/` - HTML templates for publish/join pages
- `static/js/connect.js` - Shared WebRTC client-side logic
- `.env` - Environment configuration (port setting)

## Usage

1. Start server: `go run main.go` or `docker-compose up`
2. Publisher: Navigate to `localhost:8588/publish` to start broadcasting
3. Viewers: Navigate to `localhost:8588/join` to watch the stream

The server can handle multiple simultaneous viewers of a single publisher's stream.