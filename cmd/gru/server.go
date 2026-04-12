package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func runServer() error {
	cfgPath := filepath.Join(os.Getenv("HOME"), ".gru", "server.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Ensure DB directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer s.Close()

	pub := ingestion.NewPublisher()
	reg := adapter.NewRegistry()
	reg.Register(claude.NewNormalizer())

	svc := server.NewService(s, pub)
	ingestionHandler := ingestion.NewHandler(s, reg, pub)

	mux := http.NewServeMux()

	// gRPC + grpc-web via connect-go (single port, no Envoy).
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc,
		connect.WithCompressMinBytes(1024),
	)
	mux.Handle(grpcPath, server.BearerAuth(cfg.APIKey, grpcHandler))

	// Hook event ingestion (plain HTTP POST).
	mux.Handle("POST /events", server.BearerAuth(cfg.APIKey, ingestionHandler))

	// h2c enables HTTP/2 cleartext (required for gRPC without TLS).
	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}

	log.Printf("gru server listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	log.Printf("API key: %s", cfg.APIKey)
	return httpServer.ListenAndServe()
}
