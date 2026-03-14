package main

import (
	"log"
	"net/http"
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
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Starting lore-reply on http://%s", cfg.Listen)
	log.Fatal(server.ListenAndServe())
}
