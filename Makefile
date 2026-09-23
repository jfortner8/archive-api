.PHONY: build run test test-integration lint tidy dev-up dev-down dev-setup

build:
	go build -o bin/api ./cmd/api

# Cross-compiles the Lambda entrypoint for Lambda's arm64 (Graviton)
# runtime and zips it as `bootstrap`, the name the provided.al2023 custom
# runtime expects. Uses PowerShell's Compress-Archive since `zip` isn't a
# standard tool on Windows.
build-lambda:
	GOOS=linux GOARCH=arm64 go build -o bootstrap ./cmd/lambda
	powershell -NoProfile -Command "Compress-Archive -Path bootstrap -DestinationPath lambda.zip -Force"
	rm -f bootstrap

deploy-lambda: build-lambda
	aws lambda update-function-code --function-name archive-api --zip-file fileb://lambda.zip

run:
	go run ./cmd/api

test:
	go test ./...

# The store tests need a real DynamoDB - they exercise conditional writes,
# index ordering and cursor paging, none of which a hand-written fake would
# tell you the truth about. They skip when DYNAMODB_ENDPOINT is unset, so
# plain `make test` stays fast and dependency-free; this target is the one
# that actually covers internal/store.
#
# Each test provisions and drops its own table, so `make dev-setup` is not a
# prerequisite - only `make dev-up`.
test-integration:
	DYNAMODB_ENDPOINT=http://localhost:8000 go test ./... -count=1

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
# dev-up. Safe to run more than once. Doesn't cover Cognito - there's no
# local emulator for it, so local dev points at the same real User Pool
# production uses; see the README's "Local dev and Cognito" section.
dev-setup:
	docker compose --profile tools run --rm awscli s3 mb s3://archive-dev --endpoint-url http://minio:9000 || true
	docker compose --profile tools run --rm awscli dynamodb create-table \
		--table-name archive-items-dev \
		--attribute-definitions AttributeName=id,AttributeType=S \
		--key-schema AttributeName=id,KeyType=HASH \
		--billing-mode PAY_PER_REQUEST \
		--endpoint-url http://dynamodb-local:8000 || true
