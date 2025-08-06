// Package main implements a WebRTC SFU (Selective Forwarding Unit) server.
// It provides video broadcasting capabilities for real-time communication.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
	"time"

	appErrors "github.com/Adaickalavan/Go-WebRTC-GStreamer/errors"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/handler"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/middleware"
	"github.com/joho/godotenv"
	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/samplebuilder"
)

var peerConnectionConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
}

const (
	rtcpPLIInterval = time.Second
	// mode for frames width per timestamp from a 30 second capture
	rtpAverageFrameWidth = 7
)

func init() {
	// Initialize structured logging
	logging.Init()
	logger := logging.GetLogger()

	// Load .env file
	err := godotenv.Load()
	if err != nil {
		logger.WithError(err).Warn("Failed to load .env file, using environment variables")
	}

	logger.Info("Application initialized successfully")
}

func main() {
	logger := logging.GetLogger()
	logger.Info("Starting WebRTC SFU server")

	// Everything below is the pion-WebRTC API, thanks for using it ❤️.
	// Create a MediaEngine object to configure the supported codec
	mediaEngine := &webrtc.MediaEngine{}

	// Setup the codecs you want to use.
	// Only support VP8, this makes our proxying code simpler
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeVP8,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		codecErr := appErrors.NewServerError(
			appErrors.ServerInternal,
			"Failed to register VP8 codec",
			"webrtc_media_engine",
			err,
		)
		logger.WithError(codecErr).Fatal("Failed to initialize WebRTC codec")
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	// Create a track that we can write to
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeVP8,
	}, "pion", "video")
	if err != nil {
		trackErr := appErrors.NewServerError(
			appErrors.ServerInternal,
			"Failed to create local track",
			"webrtc_track",
			err,
		)
		logger.WithError(trackErr).Fatal("Failed to initialize WebRTC track")
	}

	// Run SDP server
	s := newSDPServer(api, track)
	s.run(os.Getenv("LISTENINGADDR"))
}

type connectionInfo struct {
	pc        *webrtc.PeerConnection
	connType  string // "Publisher" or "Client"
	createdAt time.Time
	cancel    context.CancelFunc
}

type sdpServer struct {
	recoverCount int
	api          *webrtc.API
	track        *webrtc.TrackLocalStaticRTP
	mux          *http.ServeMux
	connections  map[string]*connectionInfo
	connMutex    sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
}

func newSDPServer(api *webrtc.API, track *webrtc.TrackLocalStaticRTP) *sdpServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &sdpServer{
		api:         api,
		track:       track,
		connections: make(map[string]*connectionInfo),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (s *sdpServer) addConnection(connID string, peerConnection *webrtc.PeerConnection, connType string) context.CancelFunc {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	_, cancel := context.WithCancel(s.ctx)
	s.connections[connID] = &connectionInfo{
		pc:        peerConnection,
		connType:  connType,
		createdAt: time.Now(),
		cancel:    cancel,
	}

	log.Printf("Added %s connection: %s (total: %d)", connType, connID, len(s.connections))
	return cancel
}

func (s *sdpServer) removeConnection(connID string) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if conn, exists := s.connections[connID]; exists {
		conn.cancel()
		delete(s.connections, connID)
		log.Printf("Removed %s connection: %s (total: %d)", conn.connType, connID, len(s.connections))
	}
}

func (s *sdpServer) getConnectionCount() int {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	return len(s.connections)
}

func (s *sdpServer) cleanup() {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	for connID, conn := range s.connections {
		conn.cancel()
		if conn.pc != nil {
			if err := conn.pc.Close(); err != nil {
				log.Printf("Error closing peer connection %s: %v", connID, err)
			}
		}
		delete(s.connections, connID)
	}
	s.cancel()
	log.Printf("Cleaned up all connections")
}

func generateConnectionID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to time-based ID if random fails
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405")))
	}
	return hex.EncodeToString(bytes)
}

func (s *sdpServer) connectionTimeoutMonitor() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	const connectionTimeout = 5 * time.Minute // 5 minute timeout

	log.Println("Starting connection timeout monitor")

	for {
		select {
		case <-s.ctx.Done():
			log.Println("Connection timeout monitor stopped")
			return
		case <-ticker.C:
			s.connMutex.RLock()
			var expiredConnections []string
			now := time.Now()

			for connID, conn := range s.connections {
				if now.Sub(conn.createdAt) > connectionTimeout {
					// Check if connection is still active
					if conn.pc.ConnectionState() == webrtc.PeerConnectionStateConnected ||
						conn.pc.ICEConnectionState() == webrtc.ICEConnectionStateConnected {
						// Connection is active, update created time
						conn.createdAt = now
					} else {
						// Connection is inactive and old, mark for cleanup
						expiredConnections = append(expiredConnections, connID)
					}
				}
			}
			s.connMutex.RUnlock()

			// Clean up expired connections
			for _, connID := range expiredConnections {
				log.Printf("Connection %s timed out, cleaning up", connID)
				s.removeConnection(connID)
			}

			if len(expiredConnections) > 0 {
				log.Printf("Cleaned up %d expired connections", len(expiredConnections))
			}
		}
	}
}

func (s *sdpServer) makeMux() {
	mux := http.NewServeMux()
	mux.HandleFunc("/sdp", handlerSDP(s))
	mux.HandleFunc("/join", handlerJoin)
	mux.HandleFunc("/publish", handlerPublish)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

	// Apply middleware chain
	var handler http.Handler = mux

	// Add rate limiting middleware with path-specific limits
	rateLimitConfig := &middleware.PathSpecificRateLimitConfig{
		Default: middleware.DefaultRateLimitConfig(),
		Paths: map[string]*middleware.RateLimitConfig{
			"/sdp": {
				RequestsPerSecond: 2.0, // Stricter limit for SDP endpoint
				Burst:             5,
				CleanupInterval:   5 * time.Minute,
				KeyGenerator:      middleware.DefaultKeyGenerator,
			},
		},
	}
	handler = middleware.PathSpecificRateLimitMiddleware(rateLimitConfig)(handler)

	// Add validation middleware
	handler = middleware.ValidateRequestMiddleware(nil)(handler)

	// Add logging middleware
	handler = middleware.LoggingMiddleware(handler)

	// Add correlation ID middleware (should be first)
	handler = middleware.CorrelationIDMiddleware(handler)

	// Store the handler - we'll use the middleware-wrapped handler directly in the server
	if muxHandler, ok := handler.(*http.ServeMux); ok {
		s.mux = muxHandler
	} else {
		// Store in a temporary field to use in server
		newMux := http.NewServeMux()
		newMux.Handle("/", handler)
		s.mux = newMux
	}
}

func (s *sdpServer) run(port string) {
	logger := logging.GetLogger()

	defer func() {
		s.cleanup() // Clean up all connections on shutdown
		s.recoverCount++
		if s.recoverCount > 1 {
			serverErr := appErrors.NewServerError(
				appErrors.ServerInternal,
				"Server failed to recover after maximum retries",
				"sdp_server",
				nil,
			)
			logger.WithError(serverErr).Fatal("Server failed to run after recovery attempts")
		}
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.LogPanic(r, stackTrace)

			panicErr := appErrors.NewServerError(
				appErrors.ServerInternal,
				"Server panicked and recovered",
				"sdp_server",
				nil,
			)
			logger.WithError(panicErr).Error("Server panic recovered, attempting restart")

			// Restart server after brief delay
			time.Sleep(5 * time.Second)
			go s.run(port)
		}
	}()

	// Start connection timeout monitor
	go s.connectionTimeoutMonitor()

	s.makeMux()

	server := &http.Server{
		Addr:           ":" + port,
		Handler:        s.mux,
		ReadTimeout:    30 * time.Second, // Increased timeout for WebRTC
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	logger.WithFields(map[string]interface{}{
		"port":       port,
		"event_type": "server_start",
	}).Info("Starting WebRTC SFU server")

	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		serverErr := appErrors.NewServerError(
			appErrors.ServerUnavailable,
			"HTTP server failed to start or serve",
			"http_server",
			err,
		)
		logger.WithError(serverErr).WithField("port", port).Fatal("Failed to start HTTP server")
	}
}

type message struct {
	Name string                    `json:"name"`
	SD   webrtc.SessionDescription `json:"sd"`
}

func handlerSDP(s *sdpServer) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		ctx := request.Context()
		logger := logging.WithContext(ctx)
		correlationID := logging.GetCorrelationID(ctx)
		defer func() {
			if err := request.Body.Close(); err != nil {
				logger.WithError(err).Debug("Error closing request body")
			}
		}()

		var offer message
		if err := middleware.ValidateJSONRequest(request, &offer, correlationID); err != nil {
			logger.WithError(err).Error("Failed to validate SDP request")
			handler.RespondWithAppError(writer, err, correlationID)
			return
		}

		// Validate connection name format
		connType, _, err := middleware.ValidateConnectionName(offer.Name, correlationID)
		if err != nil {
			logger.WithError(err).Error("Invalid connection name")
			handler.RespondWithAppError(writer, err, correlationID)
			return
		}

		// Validate SDP offer
		if err := middleware.ValidateSDPOffer(offer.SD, correlationID); err != nil {
			logger.WithError(err).Error("Invalid SDP offer")
			handler.RespondWithAppError(writer, err, correlationID)
			return
		}

		// Create a new RTCPeerConnection
		peerConnection, err := s.api.NewPeerConnection(peerConnectionConfig)
		if err != nil {
			webrtcErr := appErrors.NewWebRTCError(
				appErrors.WebRTCConnectionFailed,
				"Failed to create peer connection",
				"", // Connection ID not yet generated
				connType,
				err,
			)
			webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
			logger.WithError(webrtcErr).Error("Failed to create WebRTC peer connection")
			handler.RespondWithAppError(writer, webrtcErr, correlationID)
			return
		}

		// Generate unique connection ID
		connID := generateConnectionID()

		// Add connection ID to context for logging
		ctx = logging.AddConnectionIDToContext(ctx, connID)
		ctx = logging.AddClientTypeToContext(ctx, connType)
		logger = logging.WithContext(ctx)

		// Add connection to registry and set up monitoring
		cancel := s.addConnection(connID, peerConnection, connType)

		// Set up ICE connection state monitoring
		peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
			connLogger := logging.WithConnectionID(connID)
			connLogger.WithField("ice_state", state.String()).Info("ICE connection state changed")

			switch state {
			case webrtc.ICEConnectionStateDisconnected, webrtc.ICEConnectionStateFailed, webrtc.ICEConnectionStateClosed:
				connLogger.Warn("Connection failed/disconnected, cleaning up")
				s.removeConnection(connID)
			case webrtc.ICEConnectionStateConnected:
				connLogger.Info("ICE connection established successfully")
			}
		})

		// Set up connection state monitoring
		peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			connLogger := logging.WithConnectionID(connID)
			connLogger.WithField("connection_state", state.String()).Info("Peer connection state changed")

			if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
				connLogger.Warn("Peer connection failed/closed, cleaning up")
				s.removeConnection(connID)
			}
		})

		switch connType {
		case "Publisher":
			// Allow us to receive 1 video track
			if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
				webrtcErr := appErrors.NewWebRTCError(
					appErrors.WebRTCTrackFailed,
					"Failed to add video transceiver",
					connID,
					connType,
					err,
				)
				webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
				logger.WithError(webrtcErr).Error("Failed to configure video reception")
				s.removeConnection(connID)
				handler.RespondWithAppError(writer, webrtcErr, correlationID)
				return
			}

			// Set a handler for when a new remote track starts
			// Add the incoming track to the list of tracks maintained in the server
			addOnTrack(peerConnection, s.track, connID, cancel)

			logger.WithFields(map[string]interface{}{
				"event_type":    "publisher_connected",
				"connection_id": connID,
			}).Info("New publisher connected")
		case "Client":
			_, err = peerConnection.AddTrack(s.track)
			if err != nil {
				webrtcErr := appErrors.NewWebRTCError(
					appErrors.WebRTCTrackFailed,
					"Failed to add track to client connection",
					connID,
					connType,
					err,
				)
				webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
				logger.WithError(webrtcErr).Error("Failed to add local track to peer connection")
				s.removeConnection(connID)
				handler.RespondWithAppError(writer, webrtcErr, correlationID)
				return
			}

			logger.WithFields(map[string]interface{}{
				"event_type":    "client_connected",
				"connection_id": connID,
			}).Info("New client connected")
		default:
			validationErr := appErrors.NewValidationError(
				appErrors.ValidationRequestBad,
				"Invalid connection type",
				"connection_type",
				"Publisher or Client",
				connType,
				nil,
			)
			validationErr = appErrors.AddCorrelationID(validationErr, correlationID).(*appErrors.ValidationError)
			logger.WithError(validationErr).Error("Invalid connection type")
			s.removeConnection(connID)
			handler.RespondWithAppError(writer, validationErr, correlationID)
			return
		}

		// Set the remote SessionDescription
		err = peerConnection.SetRemoteDescription(offer.SD)
		if err != nil {
			webrtcErr := appErrors.NewWebRTCError(
				appErrors.WebRTCOfferInvalid,
				"Failed to set remote description",
				connID,
				connType,
				err,
			)
			webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
			logger.WithError(webrtcErr).Error("Failed to set remote SDP description")
			s.removeConnection(connID)
			handler.RespondWithAppError(writer, webrtcErr, correlationID)
			return
		}

		// Create answer
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			webrtcErr := appErrors.NewWebRTCError(
				appErrors.WebRTCAnswerFailed,
				"Failed to create SDP answer",
				connID,
				connType,
				err,
			)
			webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
			logger.WithError(webrtcErr).Error("Failed to create SDP answer")
			s.removeConnection(connID)
			handler.RespondWithAppError(writer, webrtcErr, correlationID)
			return
		}

		// Sets the LocalDescription, and starts our UDP listeners
		err = peerConnection.SetLocalDescription(answer)
		if err != nil {
			webrtcErr := appErrors.NewWebRTCError(
				appErrors.WebRTCAnswerFailed,
				"Failed to set local description",
				connID,
				connType,
				err,
			)
			webrtcErr = appErrors.AddCorrelationID(webrtcErr, correlationID).(*appErrors.WebRTCError)
			logger.WithError(webrtcErr).Error("Failed to set local SDP description")
			s.removeConnection(connID)
			handler.RespondWithAppError(writer, webrtcErr, correlationID)
			return
		}

		// Send success response
		responseData := map[string]interface{}{
			"connection_id": connID,
			"client_type":   connType,
			"sdp":           answer,
		}

		metadata := map[string]interface{}{
			"total_connections": s.getConnectionCount(),
		}

		handler.RespondWithSuccess(writer, responseData, "WebRTC connection established successfully", correlationID, metadata)

		logger.WithFields(map[string]interface{}{
			"event_type":        "webrtc_connection_established",
			"connection_id":     connID,
			"client_type":       connType,
			"total_connections": s.getConnectionCount(),
		}).Info("WebRTC connection established successfully")
	}
}

func addOnTrack(peerconnection *webrtc.PeerConnection, localTrack *webrtc.TrackLocalStaticRTP, connID string, _ context.CancelFunc) {
	// Set a handler for when a new remote track starts, this just distributes all our packets
	// to connected peers
	peerconnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("Track acquired for connection %s: kind=%s, codec=%s, SSRC=%d", connID, remoteTrack.Kind(), remoteTrack.Codec().MimeType, remoteTrack.SSRC())

		// Create context for this track's goroutines
		ctx, trackCancel := context.WithCancel(context.Background())

		// Send a PLI on an interval so that the publisher is pushing a keyframe every rtcpPLIInterval
		// This can be less wasteful by processing incoming RTCP events, then we would emit a NACK/PLI when a viewer requests it
		go func() {
			defer trackCancel() // Cancel context when PLI ticker stops
			log.Printf("Starting PLI ticker goroutine for connection %s", connID)

			ticker := time.NewTicker(rtcpPLIInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Printf("PLI ticker goroutine stopped for connection %s: %v", connID, ctx.Err())
					return
				case <-ticker.C:
					rtcpSendErr := peerconnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remoteTrack.SSRC())}})
					if rtcpSendErr != nil {
						if rtcpSendErr == io.ErrClosedPipe {
							log.Printf("Connection %s closed, stopping PLI ticker", connID)
							return
						}
						log.Printf("PLI error for connection %s: %v", connID, rtcpSendErr)
					}
				}
			}
		}()

		// Stream processing goroutine
		go func() {
			defer trackCancel() // Cancel context when stream processing stops
			log.Printf("Starting stream processing goroutine for connection %s", connID)

			builder := samplebuilder.New(rtpAverageFrameWidth*5, &codecs.VP8Packet{}, uint32(remoteTrack.SSRC()))

			for {
				select {
				case <-ctx.Done():
					log.Printf("Stream processing goroutine stopped for connection %s: %v", connID, ctx.Err())
					return
				default:
					// Non-blocking check
				}

				rtpPacket, _, err := remoteTrack.ReadRTP()
				if err != nil {
					if err == io.EOF {
						log.Printf("Remote track ended (EOF) for connection %s", connID)
						return
					}
					log.Printf("Error reading RTP packet for connection %s: %v", connID, err)
					return
				}

				builder.Push(rtpPacket)
				for s := builder.Pop(); s != nil; s = builder.Pop() {
					if _, err := localTrack.Write(s.Data); err != nil {
						if err == io.ErrClosedPipe {
							log.Printf("Local track closed for connection %s, stopping stream", connID)
							return
						}
						log.Printf("Error writing to local track for connection %s: %v", connID, err)
						return
					}
				}
			}
		}()
	})
}

func handlerJoin(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	logger := logging.WithContext(ctx)
	correlationID := logging.GetCorrelationID(ctx)

	handler.Push(writer, "./static/js/connect.js")
	tpl, err := template.ParseFiles("./template/join.html")
	if err != nil {
		templateErr := appErrors.NewServerError(
			appErrors.TemplateParseError,
			"Failed to parse join template",
			"template_engine",
			err,
		)
		templateErr = appErrors.AddCorrelationID(templateErr, correlationID).(*appErrors.ServerError)
		logger.WithError(templateErr).Error("Template parsing failed")
		handler.RespondWithAppError(writer, templateErr, correlationID)
		return
	}
	handler.Render(writer, request, tpl, nil)
}

func handlerPublish(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	logger := logging.WithContext(ctx)
	correlationID := logging.GetCorrelationID(ctx)

	handler.Push(writer, "./static/js/connect.js")
	tpl, err := template.ParseFiles("./template/publish.html")
	if err != nil {
		templateErr := appErrors.NewServerError(
			appErrors.TemplateParseError,
			"Failed to parse publish template",
			"template_engine",
			err,
		)
		templateErr = appErrors.AddCorrelationID(templateErr, correlationID).(*appErrors.ServerError)
		logger.WithError(templateErr).Error("Template parsing failed")
		handler.RespondWithAppError(writer, templateErr, correlationID)
		return
	}
	handler.Render(writer, request, tpl, nil)
}
