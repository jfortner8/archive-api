.PHONY: build run test lint tidy dev-up dev-down dev-setup

build:
	go build -o bin/api ./cmd/api

build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/api-linux ./cmd/api

run:
	go run ./cmd/api

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

# Start local stand-ins for S3 (MinIO) and DynamoDB (DynamoDB Local).
dev-up:
	docker compose up -d

dev-down:
	docker compose down

# Create the local bucket and table against the containers started by
# dev-up. Safe to run more than once.
dev-setup:
	docker compose --profile tools run --rm awscli s3 mb s3://archive-dev --endpoint-url http://minio:9000 || true
	docker compose --profile tools run --rm awscli dynamodb create-table \
		--table-name archive-items-dev \
		--attribute-definitions AttributeName=id,AttributeType=S \
		--key-schema AttributeName=id,KeyType=HASH \
		--billing-mode PAY_PER_REQUEST \
		--endpoint-url http://dynamodb-local:8000 || true
