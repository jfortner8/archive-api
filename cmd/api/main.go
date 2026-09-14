// Command api runs the archive-api HTTP server.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/jfortner8/archive-api/internal/api"
	"github.com/jfortner8/archive-api/internal/awsclients"
	"github.com/jfortner8/archive-api/internal/config"
	"github.com/jfortner8/archive-api/internal/storage"
	"github.com/jfortner8/archive-api/internal/store"
)

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	clients, err := awsclients.New(ctx, cfg)
	if err != nil {
		log.Fatalf("aws clients: %v", err)
	}

	server := &api.Server{
		Items: store.NewItemStore(clients.Dynamo, cfg.DynamoTable),
		Files: storage.NewFileStore(clients.S3Presign, cfg.S3Bucket),
	}

	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, server.Routes()); err != nil {
		log.Fatalf("server: %v", err)
	}
}
