package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// Buffer sizes
	downloadBufferSize = 1024 * 1024      // 1MB chunks for downloads
	minBytes           = 1 * 1024 * 1024  // 1MB minimum
	maxBytes           = 10 * 1024 * 1024 * 1024 // 10GB maximum

	// Timeouts
	defaultReadTimeout  = 30 * time.Second
	defaultWriteTimeout = 30 * time.Second
	defaultHTTPTimeout  = 5 * time.Minute

	// Modes
	modeClient = "client"
	modeServer = "server"

	// Directions
	directionDown = "down"
	directionUp   = "up"
	directionBoth = "both"
)

// Config represents application configuration
type Config struct {
	// Common
	Mode      string // "client" or "server"
	Direction string // "down", "up", or "both"

	// Client-specific
	Count  int    // number of speed tests
	Size   int    // file size in MB
	Server string // server address

	// Server-specific
	Port string // listening port
	Host string // listening host
}

// ServerStats tracks server statistics with thread-safe operations
type ServerStats struct {
	mu                sync.RWMutex
	totalDownloads    int64
	totalUploads      int64
	totalBytesDown    int64
	totalBytesUp      int64
	totalConnections  int64
	startTime         time.Time
	lastRequestTime   time.Time
	peakConcurrent    int64
	currentConcurrent int64
}

var (
	stats = &ServerStats{
		startTime: time.Now(),
	}
	httpClient = &http.Client{
		Timeout: defaultHTTPTimeout,
	}
	logger = log.New(os.Stdout, "", log.LstdFlags)

	//go:embed http/*
	embeddedFS embed.FS
)

// main entry point
func main() {
	config := parseFlags()

	if err := config.validate(); err != nil {
		logger.Fatalf("Configuration error: %v", err)
	}

	if config.Mode == modeServer {
		runServer(config)
	} else {
		runClient(config)
	}
}

// Config validation
func (c *Config) validate() error {
	switch c.Mode {
	case modeClient:
		if c.Count < 1 {
			return fmt.Errorf("count must be at least 1, got %d", c.Count)
		}
		if c.Size < 1 {
			return fmt.Errorf("size must be at least 1 MB, got %d", c.Size)
		}
		if !isValidDirection(c.Direction) {
			return fmt.Errorf("invalid direction '%s', must be 'down', 'up', or 'both'", c.Direction)
		}
		if c.Server == "" {
			return fmt.Errorf("server address cannot be empty")
		}
	case modeServer:
		if c.Port == "" || c.Port == "0" {
			return fmt.Errorf("port cannot be empty")
		}
		if _, err := strconv.Atoi(c.Port); err != nil {
			return fmt.Errorf("port must be a valid number")
		}
	default:
		return fmt.Errorf("invalid mode '%s', must be 'client' or 'server'", c.Mode)
	}
	return nil
}

func isValidDirection(d string) bool {
	return d == directionDown || d == directionUp || d == directionBoth
}

// ============== SERVER IMPLEMENTATION ==============

func runServer(config Config) {
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	logger.Printf("Starting speed test server on %s", addr)

	mux := http.NewServeMux()

	// Serve static files from embedded FS
	sub, err := fs.Sub(embeddedFS, "http")
	if err != nil {
		logger.Fatalf("fs.Sub: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// Serve the binary itself for download
	mux.HandleFunc("/ethspeed", func(w http.ResponseWriter, r *http.Request) {
		exe, err := os.Executable()
		if err != nil {
			http.Error(w, "cannot find executable", http.StatusInternalServerError)
			logger.Printf("os.Executable error: %v", err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="ethspeed"`)
		http.ServeFile(w, r, exe)
	})

	mux.HandleFunc("/__down", downloadHandler)
	mux.HandleFunc("/__up", uploadHandler)
	mux.HandleFunc("/__stats", statsHandler)
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
	}

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Println("\nShutting down server gracefully...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Printf("Server shutdown error: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Server error: %v", err)
	}
}

// downloadHandler handles GET requests for download speed testing
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	numBytes, err := parseBytes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Increment concurrent connections
	atomic.AddInt64(&stats.currentConcurrent, 1)
	defer atomic.AddInt64(&stats.currentConcurrent, -1)

	// Update peak concurrent
	current := atomic.LoadInt64(&stats.currentConcurrent)
	peak := atomic.LoadInt64(&stats.peakConcurrent)
	for current > peak && !atomic.CompareAndSwapInt64(&stats.peakConcurrent, peak, current) {
		peak = atomic.LoadInt64(&stats.peakConcurrent)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(numBytes, 10))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	buffer := make([]byte, downloadBufferSize)
	remaining := numBytes

	for remaining > 0 {
		writeSize := int64(len(buffer))
		if remaining < writeSize {
			writeSize = remaining
			buffer = buffer[:writeSize]
		}

		if _, err := w.Write(buffer); err != nil {
			logger.Printf("Download write error for %s: %v", r.RemoteAddr, err)
			return
		}

		remaining -= writeSize
	}

	// Update statistics
	stats.mu.Lock()
	stats.totalDownloads++
	stats.totalBytesDown += numBytes
	stats.lastRequestTime = time.Now()
	stats.mu.Unlock()

	logger.Printf("[DOWNLOAD] %s - %s", r.RemoteAddr, formatBytes(numBytes))
}

// uploadHandler handles POST requests for upload speed testing
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	expectedBytes, err := parseBytes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Increment concurrent connections
	atomic.AddInt64(&stats.currentConcurrent, 1)
	defer atomic.AddInt64(&stats.currentConcurrent, -1)

	// Update peak concurrent
	current := atomic.LoadInt64(&stats.currentConcurrent)
	peak := atomic.LoadInt64(&stats.peakConcurrent)
	for current > peak && !atomic.CompareAndSwapInt64(&stats.peakConcurrent, peak, current) {
		peak = atomic.LoadInt64(&stats.peakConcurrent)
	}

	uploadedBytes, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		logger.Printf("Upload read error for %s: %v", r.RemoteAddr, err)
		http.Error(w, "upload error", http.StatusInternalServerError)
		return
	}

	if uploadedBytes != expectedBytes {
		logger.Printf("Warning: %s expected %s, received %s",
			r.RemoteAddr, formatBytes(expectedBytes), formatBytes(uploadedBytes))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"ok":true,"bytes":%d}`, uploadedBytes)

	// Update statistics
	stats.mu.Lock()
	stats.totalUploads++
	stats.totalBytesUp += uploadedBytes
	stats.totalConnections++
	stats.lastRequestTime = time.Now()
	stats.mu.Unlock()

	logger.Printf("[UPLOAD] %s - %s", r.RemoteAddr, formatBytes(uploadedBytes))
}

// statsHandler returns server statistics
func statsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats.mu.RLock()
	totalDownloads := stats.totalDownloads
	totalUploads := stats.totalUploads
	totalBytesDown := stats.totalBytesDown
	totalBytesUp := stats.totalBytesUp
	totalConnections := stats.totalConnections
	uptime := time.Since(stats.startTime)
	lastRequest := stats.lastRequestTime
	peakConcurrent := stats.peakConcurrent
	stats.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
  "ok": true,
  "total_downloads": %d,
  "total_uploads": %d,
  "total_bytes_down": %d,
  "total_bytes_up": %d,
  "total_connections": %d,
  "total_data_gb": %.2f,
  "uptime_seconds": %.0f,
  "peak_concurrent": %d,
  "last_request": "%s"
}`,
		totalDownloads,
		totalUploads,
		totalBytesDown,
		totalBytesUp,
		totalConnections,
		float64(totalBytesDown+totalBytesUp)/1_000_000_000,
		uptime.Seconds(),
		peakConcurrent,
		lastRequest.Format(time.RFC3339),
	)
}

// healthHandler returns health status
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"status":"healthy","timestamp":"%s"}`, time.Now().Format(time.RFC3339))
}

// ============== CLIENT IMPLEMENTATION ==============

func runClient(config Config) {
	fmt.Printf("Speed Test - %d MB per run\n", config.Size)
	fmt.Printf("Server: %s\n\n", config.Server)

	switch config.Direction {
	case directionBoth:
		runBothTests(config)
	case directionDown:
		runDownloadTests(config)
	case directionUp:
		runUploadTests(config)
	}
}

func runBothTests(config Config) {
	downloadSpeeds := make([]float64, config.Count)
	uploadSpeeds := make([]float64, config.Count)
	totalTime := time.Duration(0)

	fmt.Printf("%-8s | %-8s | %s\n", "down", "up", "Mbps")
	fmt.Println(strings.Repeat("-", 30))

	for i := 0; i < config.Count; i++ {
		downSpeed, downDur, err := runDownloadTest(config)
		if err != nil {
			fmt.Printf("ERROR: download test %d: %v\n", i+1, err)
			return
		}
		downloadSpeeds[i] = downSpeed
		totalTime += downDur

		upSpeed, upDur, err := runUploadTest(config)
		if err != nil {
			fmt.Printf("ERROR: upload test %d: %v\n", i+1, err)
			return
		}
		uploadSpeeds[i] = upSpeed
		totalTime += upDur

		fmt.Printf("%-8.1f | %-8.1f | Mbps\n", downSpeed, upSpeed)

		if i < config.Count-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	avgDown := calculateAverage(downloadSpeeds)
	avgUp := calculateAverage(uploadSpeeds)

	fmt.Println(strings.Repeat("-", 30))
	fmt.Printf("%-8.1f | %-8.1f | Avg\n", avgDown, avgUp)
	fmt.Printf("Total time: %.2f seconds\n\n", totalTime.Seconds())
}

func runDownloadTests(config Config) {
	speeds := make([]float64, config.Count)
	totalTime := time.Duration(0)

	fmt.Printf("%-8s\n", "down")
	fmt.Println(strings.Repeat("-", 18))

	for i := 0; i < config.Count; i++ {
		speed, duration, err := runDownloadTest(config)
		if err != nil {
			fmt.Printf("ERROR: test %d: %v\n", i+1, err)
			return
		}

		speeds[i] = speed
		totalTime += duration
		fmt.Printf("%-8.1f Mbps\n", speed)

		if i < config.Count-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	avgSpeed := calculateAverage(speeds)
	fmt.Println(strings.Repeat("-", 18))
	fmt.Printf("%-8.1f Avg\n", avgSpeed)
	fmt.Printf("Total time: %.2f seconds\n\n", totalTime.Seconds())
}

func runUploadTests(config Config) {
	speeds := make([]float64, config.Count)
	totalTime := time.Duration(0)

	fmt.Printf("%-8s\n", "up")
	fmt.Println(strings.Repeat("-", 18))

	for i := 0; i < config.Count; i++ {
		speed, duration, err := runUploadTest(config)
		if err != nil {
			fmt.Printf("ERROR: test %d: %v\n", i+1, err)
			return
		}

		speeds[i] = speed
		totalTime += duration
		fmt.Printf("%-8.1f Mbps\n", speed)

		if i < config.Count-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	avgSpeed := calculateAverage(speeds)
	fmt.Println(strings.Repeat("-", 18))
	fmt.Printf("%-8.1f Avg\n", avgSpeed)
	fmt.Printf("Total time: %.2f seconds\n\n", totalTime.Seconds())
}

func runDownloadTest(config Config) (float64, time.Duration, error) {
	numBytes := int64(config.Size) * 1_000_000
	url := fmt.Sprintf("http://%s/__down?bytes=%d", config.Server, numBytes)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("request creation failed: %w", err)
	}

	startTime := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	bytesDownloaded, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("read failed: %w", err)
	}

	elapsed := time.Since(startTime)
	if elapsed == 0 {
		return 0, 0, fmt.Errorf("test completed too quickly to measure")
	}

	speedBytesPerSec := float64(bytesDownloaded) / elapsed.Seconds()
	speedMbps := (speedBytesPerSec * 8) / 1_000_000

	return speedMbps, elapsed, nil
}

func runUploadTest(config Config) (float64, time.Duration, error) {
	numBytes := int64(config.Size) * 1_000_000
	url := fmt.Sprintf("http://%s/__up?bytes=%d", config.Server, numBytes)

	data := make([]byte, numBytes)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("request creation failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/octet-stream")

	startTime := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	io.Copy(io.Discard, resp.Body)

	elapsed := time.Since(startTime)
	if elapsed == 0 {
		return 0, 0, fmt.Errorf("test completed too quickly to measure")
	}

	speedBytesPerSec := float64(numBytes) / elapsed.Seconds()
	speedMbps := (speedBytesPerSec * 8) / 1_000_000

	return speedMbps, elapsed, nil
}

// ============== UTILITY FUNCTIONS ==============

func calculateAverage(speeds []float64) float64 {
	if len(speeds) == 0 {
		return 0
	}

	sum := 0.0
	for _, speed := range speeds {
		sum += speed
	}

	return sum / float64(len(speeds))
}

func parseBytes(r *http.Request) (int64, error) {
	bytesParam := r.URL.Query().Get("bytes")
	if bytesParam == "" {
		return 0, fmt.Errorf("missing 'bytes' parameter")
	}

	numBytes, err := strconv.ParseInt(bytesParam, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid 'bytes' parameter: %w", err)
	}

	if numBytes < minBytes || numBytes > maxBytes {
		return 0, fmt.Errorf("bytes must be between %s and %s",
			formatBytes(minBytes), formatBytes(maxBytes))
	}

	return numBytes, nil
}

func formatBytes(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)

	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func parseFlags() Config {
	// Mode flags
	mode := flag.String("mode", modeClient,
		"operation mode: 'client' or 'server'")

	// Server-specific flags
	port := flag.String("port", "8080",
		"server listening port")
	host := flag.String("host", "0.0.0.0",
		"server listening host")

	// Client-specific flags (short and long versions)
	count := flag.Int("c", 1, "number of speed tests to run")
	countLong := flag.Int("count", 1, "number of speed tests to run")

	size := flag.Int("s", 100, "file size per test in MB")
	sizeLong := flag.Int("size", 100, "file size per test in MB")

	server := flag.String("S", "speed.cloudflare.com",
		"server address for tests")
	serverLong := flag.String("server", "speed.cloudflare.com",
		"server address for tests")

	direction := flag.String("d", directionBoth,
		"test direction: 'down', 'up', or 'both'")
	directionLong := flag.String("direction", directionBoth,
		"test direction: 'down', 'up', or 'both'")

	flag.Parse()

	// Resolve flags (prefer long versions if explicitly set)
	finalCount := *count
	if *countLong != 1 {
		finalCount = *countLong
	}

	finalSize := *size
	if *sizeLong != 100 {
		finalSize = *sizeLong
	}

	finalServer := *server
	if *serverLong != "speed.cloudflare.com" {
		finalServer = *serverLong
	}

	finalDirection := *direction
	if *directionLong != directionBoth {
		finalDirection = *directionLong
	}

	return Config{
		Mode:      *mode,
		Port:      *port,
		Host:      *host,
		Count:     finalCount,
		Size:      finalSize,
		Server:    finalServer,
		Direction: finalDirection,
	}
}
