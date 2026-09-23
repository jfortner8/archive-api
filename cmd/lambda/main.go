// Command lambda runs archive-api as an AWS Lambda function behind a
// Function URL, instead of a listening server (see cmd/api). Same
// handlers, same business logic - only how a request reaches them differs.
package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/jfortner8/archive-api/internal/authtoken"
	"github.com/jfortner8/archive-api/internal/awsclients"
	"github.com/jfortner8/archive-api/internal/config"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/httpapi"
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

	verifier, err := authtoken.NewCognitoVerifier(ctx, cfg.CognitoUserPoolID, cfg.CognitoRegion, cfg.CognitoAppClientID)
	if err != nil {
		log.Fatalf("cognito verifier: %v", err)
	}

	server := &httpapi.Server{
		Store:    store.New(clients.Dynamo, cfg.DynamoTable, itemtypes.Default),
		Files:    storage.NewFileStore(clients.S3Presign, cfg.S3Bucket),
		Verifier: verifier,
		Types:    itemtypes.Default,
		APIKey:   cfg.APIKey,
	}

	// Lambda Function URLs send events in the API Gateway HTTP API v2
	// payload format, hence NewV2.
	adapter := httpadapter.NewV2(server.Routes())
	lambda.Start(adapter.ProxyWithContext)
}
