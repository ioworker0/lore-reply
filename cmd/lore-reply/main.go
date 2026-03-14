package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/ioworker0/lore-reply/internal/config"
	"github.com/ioworker0/lore-reply/internal/web"
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
