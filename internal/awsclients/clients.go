// Package awsclients builds the AWS SDK clients the service depends on,
// transparently pointing them at local emulators (MinIO, DynamoDB Local)
// when the corresponding endpoint override is set.
package awsclients

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/jfortner8/archive-api/internal/config"
)

// Clients bundles the AWS service clients used across the API.
type Clients struct {
	S3       *s3.Client
	S3Presign *s3.PresignClient
	Dynamo   *dynamodb.Client
}

// New builds AWS clients from the given config.
func New(ctx context.Context, cfg config.Config) (*Clients, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = true // required by MinIO and most local S3-compatible endpoints
		}
	})

	dynamoClient := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if cfg.DynamoEndpoint != "" {
			o.BaseEndpoint = aws.String(cfg.DynamoEndpoint)
		}
	})

	return &Clients{
		S3:        s3Client,
		S3Presign: s3.NewPresignClient(s3Client),
		Dynamo:    dynamoClient,
	}, nil
}
