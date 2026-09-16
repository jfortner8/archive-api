# archive-api

Go backend for archive-ui. Stores item metadata in DynamoDB and files
(photos, PDFs, etc.) in S3, accessed via presigned URLs so files never
pass through this service's own request handling.

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (for local dev dependencies only)
- [AWS CLI v2](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html), configured (`aws configure`) with credentials for real AWS deployment steps

## Local development

Local dev runs against MinIO (an S3-compatible server) and DynamoDB Local
instead of real AWS, so you can't rack up a bill or need real credentials
just to run the server.

```bash
cp .env.example .env
make dev-up      # starts MinIO + DynamoDB Local via Docker Compose
make dev-setup   # creates the local bucket + table (safe to re-run)
make tidy        # fetches Go module dependencies (first run only)
make run         # starts the API on :8080
```

Check it's alive:

```bash
curl http://localhost:8080/healthz
```

MinIO's web console is at http://localhost:9001 (login: `minioadmin` /
`minioadmin`) if you want to poke around the bucket visually.

## API shape

Every route below except `/healthz` requires an `X-API-Key` header
matching the `API_KEY` config value, and (in production) a valid
AWS SigV4 signature - see [Auth](#auth) below.

- `POST /items` — create an item (`{"type": "photo", "title": "..."}`).
  `date`/`location`/`notes`/`tags`/`people` aren't set here - a capture
  flow often knows the file before it knows the details, so those are
  added afterward via `PATCH`.
- `GET /items` — list items
- `GET /items/{id}` — get one item
- `PATCH /items/{id}` — partial update: only the fields present in the
  body are changed. Any of `title`, `date`, `location`, `notes`, `tags`,
  `people` -  e.g. `{"tags": ["family", "1952"]}` leaves everything else
  as-is. `date` is `{"kind": "exact"|"range"|"circa", ...}` (see
  `store.ArchiveDate`); `location` is `{"label": "...", "lat": ..., "lon": ..., "confidence": "..."}`.
- `POST /items/{id}/upload-url` — get a presigned S3 URL to upload a file
  (`{"role": "front", "filename": "scan.jpg", "contentType": "image/jpeg"}`,
  `order` optional for multi-page documents); the frontend `PUT`s the
  file bytes directly to the returned URL
- `POST /items/{id}/files` — record that an upload finished, attaching it
  to the item (`{"role": "front", "key": "...", "contentType": "...", "sizeBytes": 123}`).
  The response includes the attached file's generated `id`.
- `GET /items/{id}/files/{fileID}/download-url` — get a presigned URL to
  download a specific file, addressed by its own `id` (not `role` - an
  item can have several files sharing a role, e.g. multiple voice memos
  or document pages)

## Auth

Two independent layers protect this API, since it's otherwise reachable
by anyone who finds the URL:

1. **AWS IAM** on the Lambda Function URL itself (`auth-type AWS_IAM`) —
   every request must carry a valid AWS SigV4 signature, or AWS rejects
   it before it ever reaches the Go code. A browser can't produce this
   signature itself (it would need real AWS credentials, which must
   never reach client-side JS), so the frontend needs a small server-side
   proxy (a Next.js API route/Server Action) that holds a narrowly-scoped
   IAM credential and signs each request - e.g. via the `aws4fetch` npm
   package - before forwarding it here.
2. **`X-API-Key` header**, checked in the Go code itself
   (`internal/api/handlers.go`), independent of the IAM layer above.

Both must be satisfied. `/healthz` requires neither, so it can be used
for uptime checks.

## Setting up real AWS (once you're ready to deploy)

1. **S3 bucket** — create one (e.g. `archive-prod`) in the console or CLI.
   No public access needed; the API generates presigned URLs.
2. **DynamoDB table** — partition key `id` (String), on-demand ("pay per
   request") billing mode. Matches what `make dev-setup` creates locally.
3. **Lambda execution role** — a role Lambda assumes to run the function,
   with a policy scoped to just that bucket and table (not full
   S3/DynamoDB access):
   - `s3:PutObject`, `s3:GetObject` on `arn:aws:s3:::archive-prod/*`
   - `dynamodb:GetItem`, `PutItem`, `Scan` on the table's ARN
   - the AWS-managed `AWSLambdaBasicExecutionRole` policy, for CloudWatch Logs

   The app never needs a hardcoded AWS access key in production - the SDK
   picks up this role's credentials automatically, which is why
   `.env.example`'s `AWS_ACCESS_KEY_ID`/`SECRET` are marked local-only.

## Deploying to Lambda

1. Build and package the Lambda binary: `make build-lambda` → produces
   `lambda.zip` (cross-compiled for Lambda's arm64 runtime).
2. Create the function (first time only):
   ```bash
   aws lambda create-function \
     --function-name archive-api \
     --runtime provided.al2023 \
     --architectures arm64 \
     --handler bootstrap \
     --role <execution-role-arn> \
     --zip-file fileb://lambda.zip \
     --timeout 15 --memory-size 128 \
     --environment "Variables={S3_BUCKET=...,DYNAMODB_TABLE=...,API_KEY=...}"
   ```
3. Give it a public HTTPS address, locked to signed requests only:
   ```bash
   aws lambda create-function-url-config \
     --function-name archive-api \
     --auth-type AWS_IAM \
     --cors '{"AllowOrigins":["*"],"AllowMethods":["*"],"AllowHeaders":["*"]}'
   ```
   Unlike most Lambda triggers, a Function URL also needs an explicit
   resource-based permission before *any* signed caller (not just
   anonymous ones) can invoke it - AWS added this requirement after
   Function URLs first launched, so older docs/tutorials often miss it:
   ```bash
   aws lambda add-permission \
     --function-name archive-api \
     --statement-id AllowSignedInvoke \
     --action lambda:InvokeFunctionUrl \
     --principal <caller's-IAM-arn-or-account-id> \
     --function-url-auth-type AWS_IAM
   ```
4. Redeploying after a code change: `make deploy-lambda` (rebuilds and
   runs `aws lambda update-function-code`).

Testing an `AWS_IAM`-protected Function URL with `curl` alone doesn't
work, since `curl` can't produce an AWS SigV4 signature - use something
that can sign requests with your AWS credentials, e.g.
[`awscurl`](https://github.com/okigan/awscurl) (`awscurl --service lambda
--region <region> <url>`).
