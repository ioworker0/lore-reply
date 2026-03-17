package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ioworker0/lore-reply/internal/config"
	"github.com/ioworker0/lore-reply/internal/web"
)

const (
	heartbeatPath     = "/api/heartbeat"
	idleTimeout       = 10 * time.Minute
	idleCheckInterval = 5 * time.Second
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	handler, err := web.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	tracker := newIdleTracker(cfg.ExitOnIdle)
	handler = withIdleSupport(handler, tracker)

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatal(err)
	}

	baseURL := browserURL(listener.Addr().String())
	log.Printf("Starting lore-reply on %s", baseURL)

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	if cfg.ExitOnIdle {
		go shutdownOnIdle(server, tracker)
	}

	if cfg.AutoLoadURL != "" {
		if err := openBrowser(baseURL); err != nil {
			log.Printf("Could not open browser automatically: %v", err)
		}
	}

	err = <-serverErrors
	if !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type idleTracker struct {
	enabled  bool
	lastSeen atomic.Int64
}

func newIdleTracker(enabled bool) *idleTracker {
	tracker := &idleTracker{enabled: enabled}
	tracker.Touch()
	return tracker
}

func (t *idleTracker) Touch() {
	if !t.enabled {
		return
	}
	t.lastSeen.Store(time.Now().UnixNano())
}

func (t *idleTracker) IdleFor(now time.Time) time.Duration {
	if !t.enabled {
		return 0
	}

	lastSeen := t.lastSeen.Load()
	if lastSeen == 0 {
		return 0
	}

	return now.Sub(time.Unix(0, lastSeen))
}

func withIdleSupport(next http.Handler, tracker *idleTracker) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == heartbeatPath {
			tracker.Touch()
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		if request.URL.Path == "/" || strings.HasPrefix(request.URL.Path, "/api/") {
			tracker.Touch()
		}

		next.ServeHTTP(writer, request)
	})
}

func shutdownOnIdle(server *http.Server, tracker *idleTracker) {
	ticker := time.NewTicker(idleCheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		if tracker.IdleFor(time.Now()) < idleTimeout {
			continue
		}

		log.Printf("No active page heartbeat for %s, shutting down", idleTimeout)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := server.Shutdown(ctx)
		cancel()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Idle shutdown failed: %v", err)
		}
		return
	}
}

func browserURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "http://" + address
	}

	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(host, port)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform %q", runtime.GOOS)
	}

	return cmd.Start()
}
