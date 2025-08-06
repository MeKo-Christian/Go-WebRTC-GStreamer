# Introduction

This is an extension of the SFU-Minimal code example from [github.com/pion/webrtc](https://github.com/pion/webrtc/tree/master/examples/sfu-minimal).

In the original SFU-Minimal code example, the `Publish` and `Join` webpages are hosted on jsfiddle.net. We need to manually transfer the SDPs between the webpage and our server.

In this extension, we strive to build a signalling server and automate the exchange of SDPs between the webpages and the server. Moreover, the webpages `Publish` and `Join` are hosted on the same server as the signalling server.

Additionally, we try to remember the publisher and client so as to reconnect upon a loss of connection.

# Usage

## Quick Start with Just

This project includes a `justfile` for convenient development commands. Install [just](https://github.com/casey/just) and run:

```bash
# Show all available commands
just

# Run the server locally
just run

# Run with custom port
just run-port 9000

# Build and run with Docker
just docker-up

# Show server URLs
just urls
```

## Manual Setup

1. Download the code using `git clone https://github.com/Adaickalavan/Go-WebRTC-GStreamer.git`
1. Ensure `GO111MODULE=on` in your terminal.
1. To run locally:
   - Run `go install` in the project folder.
   - Then run the executable, i.e., `Go-WebRTC-GStreamer`.
1. To run the code in Docker, do the following:
   - Run `docker build -t webrtc .` in the project folder.
   - Then run `docker-compose up`.
1. Go to `localhost:8588/publish` web page which will start capturing video using your webcam. This video will be broadcast to multiple clients.
1. Then open another tab in your browser, and go to `localhost:8588/join` to see the broadcasted video.

## Available Commands

See the `justfile` for all available development commands, including:
- `just run` - Run server directly
- `just install` - Build and install executable  
- `just docker-up` - Run with Docker Compose
- `just mod-tidy` - Clean Go modules
- `just fmt` - Format code
- `just urls` - Show server endpoints
