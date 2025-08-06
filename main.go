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
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	appErrors "github.com/Adaickalavan/Go-WebRTC-GStreamer/errors"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/handler"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/logging"
	"github.com/Adaickalavan/Go-WebRTC-GStreamer/middleware"
	"github.com/joho/godotenv"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/samplebuilder"
)

// buildICEServerConfig creates ICE server configuration from environment variables
func buildICEServerConfig() webrtc.Configuration {
	var iceServers []webrtc.ICEServer
	logger := logging.GetLogger()

	// Helper function to add STUN servers
	addSTUNServers := func(envVar, description string) {
		stunServers := os.Getenv(envVar)
		if stunServers != "" {
			stunURLs := strings.Split(stunServers, ",")
			for i, url := range stunURLs {
				stunURLs[i] = strings.TrimSpace(url)
			}

			if len(stunURLs) > 0 && stunURLs[0] != "" {
				iceServers = append(iceServers, webrtc.ICEServer{
					URLs: stunURLs,
				})
				logger.WithField("stun_servers", stunURLs).Info(description + " STUN servers configured")
			}
		}
	}

	// Helper function to add TURN servers
	addTURNServers := func(serverEnvVar, usernameEnvVar, credentialEnvVar, description string) {
		turnServers := os.Getenv(serverEnvVar)
		turnUsername := os.Getenv(usernameEnvVar)
		turnCredential := os.Getenv(credentialEnvVar)

		if turnServers != "" {
			turnURLs := strings.Split(turnServers, ",")
			for i, url := range turnURLs {
				turnURLs[i] = strings.TrimSpace(url)
			}

			turnServer := webrtc.ICEServer{
				URLs: turnURLs,
			}

			// Add credentials if provided
			if turnUsername != "" && turnCredential != "" {
				turnServer.Username = turnUsername
				turnServer.Credential = turnCredential
				logger.WithFields(map[string]interface{}{
					"turn_servers": turnURLs,
					"username":     turnUsername,
				}).Info(description + " TURN servers configured with authentication")
			} else {
				logger.WithField("turn_servers", turnURLs).Info(description + " TURN servers configured without authentication")
			}

			iceServers = append(iceServers, turnServer)
		}
	}

	// Add primary STUN servers
	addSTUNServers("STUN_SERVERS", "Primary")

	// Add fallback STUN servers
	addSTUNServers("FALLBACK_STUN_SERVERS", "Fallback")

	// If no STUN servers configured, add defaults
	if len(iceServers) == 0 {
		defaultSTUNURLs := []string{
			"stun:stun.l.google.com:19302",
			"stun:stun1.l.google.com:19302",
			"stun:stun2.l.google.com:19302",
		}
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs: defaultSTUNURLs,
		})
		logger.WithField("stun_servers", defaultSTUNURLs).Info("Default STUN servers configured")
	}

	// Add primary TURN servers
	addTURNServers("TURN_SERVERS", "TURN_USERNAME", "TURN_CREDENTIAL", "Primary")

	// Add fallback TURN servers
	addTURNServers("FALLBACK_TURN_SERVERS", "FALLBACK_TURN_USERNAME", "FALLBACK_TURN_CREDENTIAL", "Fallback")

	config := webrtc.Configuration{
		ICEServers: iceServers,
	}

	// Add additional WebRTC configuration for better connectivity
	config.ICETransportPolicy = webrtc.ICETransportPolicyAll
	config.BundlePolicy = webrtc.BundlePolicyMaxBundle
	config.RTCPMuxPolicy = webrtc.RTCPMuxPolicyRequire

	logger.WithField("total_ice_servers", len(iceServers)).Info("ICE server configuration completed")

	return config
}

var peerConnectionConfig webrtc.Configuration

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

	// Initialize WebRTC configuration with ICE servers
	peerConnectionConfig = buildICEServerConfig()

	logger.Info("Application initialized successfully")
}

func main() {
	logger := logging.GetLogger()
	logger.Info("Starting WebRTC SFU server")

	// Everything below is the pion-WebRTC API, thanks for using it ❤️.
	// Create a MediaEngine object to configure the supported codec
	mediaEngine := &webrtc.MediaEngine{}

	// Register multiple video codecs for negotiation
	videoCodecs := []struct {
		params      webrtc.RTPCodecParameters
		description string
	}{
		{
			params: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeVP8,
					ClockRate:   90000,
					Channels:    0,
					SDPFmtpLine: "",
					RTCPFeedback: []webrtc.RTCPFeedback{
						{Type: "goog-remb", Parameter: ""},
						{Type: "ccm", Parameter: "fir"},
						{Type: "nack", Parameter: ""},
						{Type: "nack", Parameter: "pli"},
					},
				},
				PayloadType: 96,
			},
			description: "VP8",
		},
		{
			params: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeVP9,
					ClockRate:   90000,
					Channels:    0,
					SDPFmtpLine: "profile-id=0",
					RTCPFeedback: []webrtc.RTCPFeedback{
						{Type: "goog-remb", Parameter: ""},
						{Type: "ccm", Parameter: "fir"},
						{Type: "nack", Parameter: ""},
						{Type: "nack", Parameter: "pli"},
					},
				},
				PayloadType: 98,
			},
			description: "VP9",
		},
		{
			params: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeH264,
					ClockRate:   90000,
					Channels:    0,
					SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
					RTCPFeedback: []webrtc.RTCPFeedback{
						{Type: "goog-remb", Parameter: ""},
						{Type: "ccm", Parameter: "fir"},
						{Type: "nack", Parameter: ""},
						{Type: "nack", Parameter: "pli"},
					},
				},
				PayloadType: 102,
			},
			description: "H264",
		},
	}

	// Register all video codecs
	for _, codec := range videoCodecs {
		if err := mediaEngine.RegisterCodec(codec.params, webrtc.RTPCodecTypeVideo); err != nil {
			// Log error but continue with other codecs
			logger.WithError(err).WithField("codec", codec.description).Warn("Failed to register video codec, continuing with others")
		} else {
			logger.WithField("codec", codec.description).Info("Registered video codec successfully")
		}
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

type videoQuality string

const (
	QualityLow    videoQuality = "low"    // 480p
	QualityMedium videoQuality = "medium" // 720p
	QualityHigh   videoQuality = "high"   // 1080p
)

type qualityProfile struct {
	width    int
	height   int
	bitrate  int // kbps
	fps      int
	priority int // Lower number = higher priority
}

var qualityProfiles = map[videoQuality]qualityProfile{
	QualityLow: {
		width:    640,
		height:   480,
		bitrate:  500,
		fps:      15,
		priority: 3,
	},
	QualityMedium: {
		width:    1280,
		height:   720,
		bitrate:  1500,
		fps:      24,
		priority: 2,
	},
	QualityHigh: {
		width:    1920,
		height:   1080,
		bitrate:  3000,
		fps:      30,
		priority: 1,
	},
}

type rtpPacketBuffer struct {
	packets    chan *rtp.Packet
	bufferSize int
	active     bool
	mutex      sync.RWMutex
}

type trackInfo struct {
	track       *webrtc.TrackLocalStaticRTP
	publisherID string
	createdAt   time.Time
	subscribers map[string]bool // Set of client connection IDs
	metadata    map[string]interface{}
	// RTP optimization
	packetBuffer *rtpPacketBuffer
	codecType    string
	// Quality management
	quality          videoQuality
	qualityProfile   qualityProfile
	adaptiveBitrate  bool
	currentBitrate   int
	targetBitrate    int
	bitrateHistory   []int // For adaptive adjustments
	lastQualityCheck time.Time
}

type connectionInfo struct {
	pc            *webrtc.PeerConnection
	connType      string // "Publisher" or "Client"
	createdAt     time.Time
	lastActivity  time.Time
	cancel        context.CancelFunc
	retryCount    int
	maxRetries    int
	isRecovering  bool
	qualityCtx    context.Context
	qualityCancel context.CancelFunc
	// Track management for publishers
	publishedTracks map[string]*trackInfo
	// Subscribed tracks for clients
	subscribedTracks map[string]*trackInfo
}

type resourcePool struct {
	// Pre-allocated buffers for RTP packet handling
	packetPool    sync.Pool
	sampleBuilder sync.Pool
	mutex         sync.RWMutex
}

func newResourcePool() *resourcePool {
	return &resourcePool{
		packetPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 1500) // Standard MTU size
			},
		},
		sampleBuilder: sync.Pool{
			New: func() interface{} {
				return samplebuilder.New(rtpAverageFrameWidth*5, &codecs.VP8Packet{}, 0)
			},
		},
	}
}

func (p *resourcePool) getPacketBuffer() []byte {
	return p.packetPool.Get().([]byte)
}

func (p *resourcePool) putPacketBuffer(buf []byte) {
	// Reset buffer before putting back
	if cap(buf) >= 1500 {
		buf = buf[:0]
		p.packetPool.Put(buf)
	}
}

func (p *resourcePool) getSampleBuilder() *samplebuilder.SampleBuilder {
	return p.sampleBuilder.Get().(*samplebuilder.SampleBuilder)
}

func (p *resourcePool) putSampleBuilder(sb *samplebuilder.SampleBuilder) {
	// Reset builder before putting back
	p.sampleBuilder.Put(sb)
}

type sdpServer struct {
	recoverCount int
	api          *webrtc.API
	// Legacy single track - kept for backwards compatibility
	track        *webrtc.TrackLocalStaticRTP
	// New per-client track management
	tracks       map[string]*trackInfo // trackID -> trackInfo
	trackMutex   sync.RWMutex
	mux          *http.ServeMux
	connections  map[string]*connectionInfo
	connMutex    sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	// Configuration
	maxConnections        int
	maxTracksPerPublisher int
	// Resource management
	resourcePool *resourcePool
}

func newSDPServer(api *webrtc.API, track *webrtc.TrackLocalStaticRTP) *sdpServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &sdpServer{
		api:                   api,
		track:                 track,
		tracks:                make(map[string]*trackInfo),
		connections:           make(map[string]*connectionInfo),
		ctx:                   ctx,
		cancel:                cancel,
		maxConnections:        100, // Default limit
		maxTracksPerPublisher: 5,   // Default limit
		resourcePool:          newResourcePool(),
	}
}

func (s *sdpServer) addConnection(connID string, peerConnection *webrtc.PeerConnection, connType string) context.CancelFunc {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	_, cancel := context.WithCancel(s.ctx)
	qualityCtx, qualityCancel := context.WithCancel(s.ctx)
	now := time.Now()

	connInfo := &connectionInfo{
		pc:               peerConnection,
		connType:         connType,
		createdAt:        now,
		lastActivity:     now,
		cancel:           cancel,
		retryCount:       0,
		maxRetries:       3, // Allow up to 3 recovery attempts
		isRecovering:     false,
		qualityCtx:       qualityCtx,
		qualityCancel:    qualityCancel,
		publishedTracks:  make(map[string]*trackInfo),
		subscribedTracks: make(map[string]*trackInfo),
	}

	s.connections[connID] = connInfo

	// Start connection quality monitoring
	go s.monitorConnectionQuality(connID)

	log.Printf("Added %s connection: %s (total: %d)", connType, connID, len(s.connections))
	return cancel
}

func (s *sdpServer) updateConnectionActivity(connID string) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if conn, exists := s.connections[connID]; exists {
		conn.lastActivity = time.Now()
	}
}

// createPacketBuffer creates a new RTP packet buffer
func createPacketBuffer(bufferSize int) *rtpPacketBuffer {
	return &rtpPacketBuffer{
		packets:    make(chan *rtp.Packet, bufferSize),
		bufferSize: bufferSize,
		active:     true,
	}
}

// startPacketForwarder starts a goroutine to forward packets to the track
func (t *trackInfo) startPacketForwarder(ctx context.Context) {
	go func() {
		logger := logging.GetLogger()
		defer logger.Debug("Packet forwarder stopped for track")

		for {
			select {
			case <-ctx.Done():
				return
			case packet, ok := <-t.packetBuffer.packets:
				if !ok {
					return
				}

				// Forward packet to track
				payload, err := packet.Marshal()
				if err != nil {
					logger.WithError(err).Error("Failed to marshal RTP packet")
					continue
				}

				if _, err := t.track.Write(payload); err != nil {
					if err == io.ErrClosedPipe {
						logger.Debug("Track closed, stopping packet forwarder")
						return
					}
					logger.WithError(err).Error("Failed to write packet to track")
				}
			}
		}
	}()
}

// selectOptimalQuality selects the best quality based on connection count and constraints
func (s *sdpServer) selectOptimalQuality(metadata map[string]interface{}) videoQuality {
	connectionCount := len(s.connections)

	// Quality degradation based on server load
	if connectionCount > 80 {
		return QualityLow
	} else if connectionCount > 40 {
		return QualityMedium
	}

	// Check if quality was explicitly requested
	if requestedQuality, ok := metadata["quality"].(string); ok {
		if quality := videoQuality(requestedQuality); quality != "" {
			if _, exists := qualityProfiles[quality]; exists {
				return quality
			}
		}
	}

	// Default to medium quality
	return QualityMedium
}

// adaptBitrate adjusts bitrate based on connection performance
func (t *trackInfo) adaptBitrate() {
	if !t.adaptiveBitrate {
		return
	}

	now := time.Now()
	if now.Sub(t.lastQualityCheck) < 5*time.Second {
		return // Don't adjust too frequently
	}

	t.lastQualityCheck = now

	// Simple adaptive logic - could be enhanced with network stats
	subscriberCount := len(t.subscribers)
	profile := t.qualityProfile

	// Adjust target bitrate based on subscriber load
	if subscriberCount > 10 {
		t.targetBitrate = int(float64(profile.bitrate) * 0.7) // Reduce by 30%
	} else if subscriberCount > 5 {
		t.targetBitrate = int(float64(profile.bitrate) * 0.85) // Reduce by 15%
	} else {
		t.targetBitrate = profile.bitrate // Full quality
	}

	// Gradual bitrate adjustment
	if t.currentBitrate < t.targetBitrate {
		t.currentBitrate += min(100, t.targetBitrate-t.currentBitrate)
	} else if t.currentBitrate > t.targetBitrate {
		t.currentBitrate -= min(100, t.currentBitrate-t.targetBitrate)
	}

	// Keep history for trend analysis
	t.bitrateHistory = append(t.bitrateHistory, t.currentBitrate)
	if len(t.bitrateHistory) > 10 {
		t.bitrateHistory = t.bitrateHistory[1:] // Keep only last 10 entries
	}

	log.Printf("Track %s bitrate adapted: current=%d, target=%d, subscribers=%d",
		t.publisherID, t.currentBitrate, t.targetBitrate, subscriberCount)
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// createTrack creates a new track for a publisher
func (s *sdpServer) createTrack(publisherID string, metadata map[string]interface{}) (*trackInfo, error) {
	s.trackMutex.Lock()
	defer s.trackMutex.Unlock()

	// Check publisher's track count limit
	publisherTrackCount := 0
	for _, track := range s.tracks {
		if track.publisherID == publisherID {
			publisherTrackCount++
		}
	}

	if publisherTrackCount >= s.maxTracksPerPublisher {
		return nil, appErrors.NewServerError(
			appErrors.ServerOverloaded,
			"Publisher has reached maximum track limit",
			"track_management",
			nil,
		)
	}

	// Determine codec type from metadata or default to VP8
	codecType, ok := metadata["codec"].(string)
	if !ok {
		codecType = webrtc.MimeTypeVP8
	}

	// Select optimal quality based on server load and preferences
	quality := s.selectOptimalQuality(metadata)
	profile := qualityProfiles[quality]

	// Create new track with specified codec
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: codecType,
	}, publisherID+"-video", publisherID)
	if err != nil {
		return nil, err
	}

	trackID := generateConnectionID() // Reuse ID generation
	trackInfo := &trackInfo{
		track:            track,
		publisherID:      publisherID,
		createdAt:        time.Now(),
		subscribers:      make(map[string]bool),
		metadata:         metadata,
		packetBuffer:     createPacketBuffer(1000), // Buffer up to 1000 packets
		codecType:        codecType,
		quality:          quality,
		qualityProfile:   profile,
		adaptiveBitrate:  true, // Enable adaptive bitrate by default
		currentBitrate:   profile.bitrate,
		targetBitrate:    profile.bitrate,
		bitrateHistory:   make([]int, 0, 10),
		lastQualityCheck: time.Now(),
	}

	// Start packet forwarder
	trackInfo.startPacketForwarder(s.ctx)

	// Start adaptive bitrate monitoring
	go func() {
		ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds
		defer ticker.Stop()

		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				trackInfo.adaptBitrate()
			}
		}
	}()

	s.tracks[trackID] = trackInfo

	// Add to publisher's track list
	s.connMutex.RLock()
	if conn, exists := s.connections[publisherID]; exists {
		conn.publishedTracks[trackID] = trackInfo
	}
	s.connMutex.RUnlock()

	log.Printf("Created new track %s for publisher %s (total tracks: %d)", trackID, publisherID, len(s.tracks))
	return trackInfo, nil
}

// subscribeToTrack subscribes a client to a track
func (s *sdpServer) subscribeToTrack(clientID, trackID string) error {
	s.trackMutex.Lock()
	defer s.trackMutex.Unlock()

	trackInfo, exists := s.tracks[trackID]
	if !exists {
		return appErrors.NewServerError(
			appErrors.ServerInternal,
			"Track not found",
			"track_management",
			nil,
		)
	}

	// Add client to subscribers
	trackInfo.subscribers[clientID] = true

	// Add to client's subscribed tracks
	s.connMutex.RLock()
	if conn, exists := s.connections[clientID]; exists {
		conn.subscribedTracks[trackID] = trackInfo
	}
	s.connMutex.RUnlock()

	log.Printf("Client %s subscribed to track %s (total subscribers: %d)", clientID, trackID, len(trackInfo.subscribers))
	return nil
}

// unsubscribeFromTrack unsubscribes a client from a track
func (s *sdpServer) unsubscribeFromTrack(clientID, trackID string) {
	s.trackMutex.Lock()
	defer s.trackMutex.Unlock()

	if trackInfo, exists := s.tracks[trackID]; exists {
		delete(trackInfo.subscribers, clientID)
		log.Printf("Client %s unsubscribed from track %s (remaining subscribers: %d)", clientID, trackID, len(trackInfo.subscribers))

		// If no more subscribers and publisher is gone, clean up track
		if len(trackInfo.subscribers) == 0 {
			s.connMutex.RLock()
			_, publisherExists := s.connections[trackInfo.publisherID]
			s.connMutex.RUnlock()

			if !publisherExists {
				delete(s.tracks, trackID)
				log.Printf("Cleaned up abandoned track %s", trackID)
			}
		}
	}

	// Remove from client's subscribed tracks
	s.connMutex.RLock()
	if conn, exists := s.connections[clientID]; exists {
		delete(conn.subscribedTracks, trackID)
	}
	s.connMutex.RUnlock()
}

// getAvailableTracks returns list of available tracks for clients
func (s *sdpServer) getAvailableTracks() map[string]*trackInfo {
	s.trackMutex.RLock()
	defer s.trackMutex.RUnlock()

	tracks := make(map[string]*trackInfo)
	for trackID, trackInfo := range s.tracks {
		tracks[trackID] = trackInfo
	}
	return tracks
}

// getMemoryStats returns current memory usage statistics
func (s *sdpServer) getMemoryStats() map[string]interface{} {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]interface{}{
		"alloc_mb":        float64(m.Alloc) / 1024 / 1024,
		"total_alloc_mb":  float64(m.TotalAlloc) / 1024 / 1024,
		"sys_mb":          float64(m.Sys) / 1024 / 1024,
		"num_gc":          m.NumGC,
		"heap_objects":    m.HeapObjects,
		"stack_inuse_mb":  float64(m.StackInuse) / 1024 / 1024,
		"goroutines":      runtime.NumGoroutine(),
		"connections":     len(s.connections),
		"tracks":          len(s.tracks),
	}
}

// monitorMemoryUsage periodically logs memory usage statistics
func (s *sdpServer) monitorMemoryUsage() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	logger := logging.GetLogger()
	logger.Info("Starting memory usage monitoring")

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("Memory usage monitor stopped")
			return
		case <-ticker.C:
			stats := s.getMemoryStats()

			// Log memory stats
			logger.WithFields(stats).Info("Memory usage statistics")

			// Check for potential memory issues
			allocMB := stats["alloc_mb"].(float64)
			goroutines := stats["goroutines"].(int)

			if allocMB > 500 { // Alert if using more than 500MB
				logger.WithFields(stats).Warn("High memory usage detected")
			}

			if goroutines > 1000 { // Alert if too many goroutines
				logger.WithFields(stats).Warn("High goroutine count detected")
			}

			// Force GC if memory usage is high
			if allocMB > 1000 { // Force GC if using more than 1GB
				logger.WithField("alloc_mb", allocMB).Info("Forcing garbage collection due to high memory usage")
				runtime.GC()
			}
		}
	}
}

func (s *sdpServer) removeConnection(connID string) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if conn, exists := s.connections[connID]; exists {
		conn.cancel()
		conn.qualityCancel() // Stop quality monitoring

		// Clean up published tracks if this is a publisher
		if conn.connType == "Publisher" {
			s.trackMutex.Lock()
			for trackID := range conn.publishedTracks {
				if trackInfo, exists := s.tracks[trackID]; exists {
					// Notify all subscribers that track is ending
					log.Printf("Publisher %s disconnected, cleaning up track %s with %d subscribers", connID, trackID, len(trackInfo.subscribers))
					delete(s.tracks, trackID)
				}
			}
			s.trackMutex.Unlock()
		}

		// Clean up subscribed tracks if this is a client
		if conn.connType == "Client" {
			for trackID := range conn.subscribedTracks {
				s.unsubscribeFromTrack(connID, trackID)
			}
		}

		delete(s.connections, connID)
		log.Printf("Removed %s connection: %s (total: %d)", conn.connType, connID, len(s.connections))
	}
}

// monitorConnectionQuality periodically checks connection statistics
func (s *sdpServer) monitorConnectionQuality(connID string) {
	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds
	defer ticker.Stop()

	logger := logging.WithConnectionID(connID)
	logger.Debug("Starting connection quality monitoring")

	for {
		s.connMutex.RLock()
		conn, exists := s.connections[connID]
		s.connMutex.RUnlock()

		if !exists {
			logger.Debug("Connection no longer exists, stopping quality monitoring")
			return
		}

		select {
		case <-conn.qualityCtx.Done():
			logger.Debug("Quality monitoring stopped")
			return
		case <-ticker.C:
			// Check if connection is still active
			iceState := conn.pc.ICEConnectionState()
			connState := conn.pc.ConnectionState()

			qualityInfo := map[string]interface{}{
				"ice_connection_state": iceState.String(),
				"connection_state":     connState.String(),
				"retry_count":          conn.retryCount,
				"is_recovering":        conn.isRecovering,
				"connection_age":       time.Since(conn.createdAt).String(),
				"last_activity":        time.Since(conn.lastActivity).String(),
			}

			// Log quality information
			if iceState == webrtc.ICEConnectionStateConnected || iceState == webrtc.ICEConnectionStateCompleted {
				logger.WithFields(qualityInfo).Debug("Connection quality check - healthy")
			} else {
				logger.WithFields(qualityInfo).Warn("Connection quality check - potential issues")
			}

			// Check for stale connections (no activity for extended period)
			if time.Since(conn.lastActivity) > 2*time.Minute && !conn.isRecovering {
				logger.WithFields(qualityInfo).Warn("Connection appears stale, may need attention")
			}
		}
	}
}

// attemptICERestart attempts to restart ICE for a failed connection
func (s *sdpServer) attemptICERestart(connID string) bool {
	s.connMutex.Lock()
	conn, exists := s.connections[connID]
	if !exists || conn.isRecovering {
		s.connMutex.Unlock()
		return false
	}

	if conn.retryCount >= conn.maxRetries {
		s.connMutex.Unlock()
		log.Printf("Connection %s exceeded max retry attempts (%d), giving up", connID, conn.maxRetries)
		s.removeConnection(connID)
		return false
	}

	conn.isRecovering = true
	conn.retryCount++
	s.connMutex.Unlock()

	logger := logging.WithConnectionID(connID)
	logger.WithFields(map[string]interface{}{
		"retry_attempt": conn.retryCount,
		"max_retries":   conn.maxRetries,
	}).Info("Attempting ICE restart for failed connection")

	// Create a new offer/answer with ICE restart
	offer, err := conn.pc.CreateOffer(&webrtc.OfferOptions{
		ICERestart: true,
	})
	if err != nil {
		logger.WithError(err).Error("Failed to create ICE restart offer")
		s.connMutex.Lock()
		conn.isRecovering = false
		s.connMutex.Unlock()
		return false
	}

	err = conn.pc.SetLocalDescription(offer)
	if err != nil {
		logger.WithError(err).Error("Failed to set local description for ICE restart")
		s.connMutex.Lock()
		conn.isRecovering = false
		s.connMutex.Unlock()
		return false
	}

	// Mark as no longer recovering - the actual recovery will be handled by ICE state changes
	s.connMutex.Lock()
	conn.isRecovering = false
	s.connMutex.Unlock()

	logger.Info("ICE restart initiated successfully")
	return true
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

	// Start memory usage monitor
	go s.monitorMemoryUsage()

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

		// Check connection limits before adding
		if s.getConnectionCount() >= s.maxConnections {
			connectionErr := appErrors.NewConnectionError(
				appErrors.ConnectionLimit,
				"Maximum number of connections reached",
				connID,
				request.RemoteAddr,
				0,
				nil,
			)
			connectionErr = appErrors.AddCorrelationID(connectionErr, correlationID).(*appErrors.ConnectionError)
			logger.WithError(connectionErr).Error("Connection limit exceeded")
			handler.RespondWithAppError(writer, connectionErr, correlationID)
			return
		}

		// Add connection to registry and set up monitoring
		cancel := s.addConnection(connID, peerConnection, connType)

		// Set up ICE connection state monitoring with recovery
		peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
			connLogger := logging.WithConnectionID(connID)
			connLogger.WithField("ice_state", state.String()).Info("ICE connection state changed")

			switch state {
			case webrtc.ICEConnectionStateConnected:
				connLogger.Info("ICE connection established successfully")
				s.updateConnectionActivity(connID)
			case webrtc.ICEConnectionStateChecking:
				connLogger.Debug("ICE connection checking")
				s.updateConnectionActivity(connID)
			case webrtc.ICEConnectionStateCompleted:
				connLogger.Info("ICE connection completed")
				s.updateConnectionActivity(connID)
			case webrtc.ICEConnectionStateDisconnected:
				connLogger.Warn("ICE connection disconnected, attempting recovery")
				// Try to restart ICE before giving up
				go func() {
					time.Sleep(2 * time.Second) // Brief delay before restart attempt
					if !s.attemptICERestart(connID) {
						connLogger.Error("ICE restart failed, cleaning up connection")
						s.removeConnection(connID)
					}
				}()
			case webrtc.ICEConnectionStateFailed:
				connLogger.Error("ICE connection failed, attempting recovery")
				go func() {
					if !s.attemptICERestart(connID) {
						connLogger.Error("ICE restart failed, cleaning up connection")
						s.removeConnection(connID)
					}
				}()
			case webrtc.ICEConnectionStateClosed:
				connLogger.Info("ICE connection closed, cleaning up")
				s.removeConnection(connID)
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
