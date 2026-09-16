// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"os"
)

// Config holds everything the service needs to talk to AWS (or local
// stand-ins for AWS during development).
type Config struct {
	Port string

	AWSRegion   string
	S3Bucket    string
	DynamoTable string

	// APIKey must be sent as the X-API-Key header on every request except
	// /healthz. There's no other access control on this API, so this is
	// what stops a stranger who finds the URL from reading or writing
	// your data.
	APIKey string

	// S3Endpoint and DynamoEndpoint override the default AWS endpoints.
	// Leave unset in production; point them at MinIO/DynamoDB Local for
	// local development (see docker-compose.yml).
	S3Endpoint     string
	DynamoEndpoint string
}

// Load reads configuration from the environment, applying defaults where
// reasonable and failing fast on anything that must be set explicitly.
func Load() (Config, error) {
	cfg := Config{
		Port:           getEnv("PORT", "8080"),
		AWSRegion:      getEnv("AWS_REGION", "us-east-1"),
		S3Bucket:       os.Getenv("S3_BUCKET"),
		DynamoTable:    os.Getenv("DYNAMODB_TABLE"),
		S3Endpoint:     os.Getenv("S3_ENDPOINT"),
		DynamoEndpoint: os.Getenv("DYNAMODB_ENDPOINT"),
		APIKey:         os.Getenv("API_KEY"),
	}

	if cfg.S3Bucket == "" {
		return Config{}, fmt.Errorf("S3_BUCKET is required")
	}
	if cfg.DynamoTable == "" {
		return Config{}, fmt.Errorf("DYNAMODB_TABLE is required")
	}
	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("API_KEY is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
