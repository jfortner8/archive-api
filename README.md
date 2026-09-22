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

### Local dev and Cognito

`make dev-up`/`make dev-setup` only cover S3 and DynamoDB - Cognito has
no local emulator equivalent to MinIO/DynamoDB Local. Local dev points at
the same real User Pool production uses (see "Setting up real AWS" below
for how it was created) - this app is small enough that one pool for
everything is simpler than keeping a second one in sync, so just use a
throwaway test account in it rather than your own. Point `.env`'s
`COGNITO_USER_POOL_ID`/`COGNITO_REGION` at it, then get a test token to
`curl` with:

```bash
# One-time: create a confirmed test user
aws cognito-idp admin-create-user --user-pool-id <pool-id> \
  --username test@example.com --temporary-password 'Temp1234!' \
  --message-action SUPPRESS
aws cognito-idp admin-set-user-password --user-pool-id <pool-id> \
  --username test@example.com --password 'Temp1234!' --permanent

# Get an access token
aws cognito-idp admin-initiate-auth --user-pool-id <pool-id> \
  --client-id <app-client-id> --auth-flow ADMIN_USER_PASSWORD_AUTH \
  --auth-parameters USERNAME=test@example.com,PASSWORD='Temp1234!'
# -> copy AuthenticationResult.AccessToken

curl -H "Authorization: Bearer <access-token>" -H "X-API-Key: dev-key" \
  http://localhost:8080/items
```

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

Every item is scoped to the account that created it (`accountId` in the
response body, a Cognito user's `sub`) - `GET /items` only ever returns
your own items, and any other endpoint given another account's item `id`
responds `404` exactly as if it didn't exist.

## Auth

Three independent layers protect this API, since it's otherwise reachable
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
3. **`Authorization: Bearer <token>` header**, a Cognito access token
   verified against the User Pool's published JWKS
   (`internal/authtoken`) - no secret is shared with archives-ui, since
   Cognito signs tokens asymmetrically. This layer answers a different
   question than the two above: 1 and 2 answer "is this our trusted
   proxy calling," this one answers "which end user is this," and is
   what every `accountId` scoping check above is based on.

All three must be satisfied. `/healthz` requires none of them, so it can
be used for uptime checks.

## Setting up real AWS (once you're ready to deploy)

1. **S3 bucket** — create one (e.g. `archive-prod`) in the console or CLI.
   No public access needed; the API generates presigned URLs.
2. **DynamoDB table** — partition key `id` (String), on-demand ("pay per
   request") billing mode. Matches what `make dev-setup` creates locally.
3. **Cognito User Pool + App Client** — the user directory. No IAM policy
   changes needed for this one: JWKS verification is a public HTTPS
   fetch, not an AWS API call.
   ```bash
   aws cognito-idp create-user-pool \
     --pool-name archive-users \
     --auto-verified-attributes email --username-attributes email \
     --account-recovery-setting RecoveryMechanisms='[{Name=verified_email,Priority=1}]' \
     --policies 'PasswordPolicy={MinimumLength=8,RequireUppercase=true,RequireLowercase=true,RequireNumbers=true,RequireSymbols=false}'
   # capture UserPool.Id -> COGNITO_USER_POOL_ID

   aws cognito-idp create-user-pool-client \
     --user-pool-id <pool-id> --client-name archive-ui \
     --explicit-auth-flows ALLOW_USER_PASSWORD_AUTH ALLOW_REFRESH_TOKEN_AUTH \
     --no-generate-secret \
     --access-token-validity 12 --id-token-validity 12 \
     --token-validity-units AccessToken=hours,IdToken=hours
   # capture UserPoolClient.ClientId -> COGNITO_APP_CLIENT_ID (archives-ui side)
   ```
   No client secret: every Cognito call in this system originates
   server-side (archives-ui's own API routes), so a confidential-client
   secret (meant for OAuth flows this design doesn't use) adds nothing.
4. **Lambda execution role** — a role Lambda assumes to run the function,
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
     --environment "Variables={S3_BUCKET=...,DYNAMODB_TABLE=...,API_KEY=...,COGNITO_USER_POOL_ID=...,COGNITO_REGION=us-east-1}"
   ```
3. Give it a public HTTPS address, locked to signed requests only:
   ```bash
   aws lambda create-function-url-config \
     --function-name archive-api \
     --auth-type AWS_IAM \
     --cors '{"AllowOrigins":["*"],"AllowMethods":["*"],"AllowHeaders":["*"]}'
   ```
4. For each same-account IAM identity that needs to call it (e.g. the
   `archive-ui-proxy` user archives-ui signs requests with), attach an
   identity-based policy granting **both** `lambda:InvokeFunctionUrl`
   *and* `lambda:InvokeFunction` on the function's ARN - Function URLs
   started requiring both fairly recently, so older docs/tutorials that
   only mention `InvokeFunctionUrl` will leave you with a confusing
   "Forbidden" even though everything looks correctly configured:
   ```bash
   aws iam put-user-policy --user-name <caller> --policy-name invoke-archive-api \
     --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["lambda:InvokeFunctionUrl","lambda:InvokeFunction"],"Resource":"<function-arn>"}]}'
   ```
   No resource-based permission (`aws lambda add-permission`) is needed
   for same-account callers - that's only for anonymous/public access
   or cross-account callers, neither of which applies here.

## Deploying

Every push to `main` (i.e. every merged PR) auto-deploys to the real
Lambda function via `.github/workflows/ci.yml`'s `deploy` job, using
credentials for `archive-api-ci-deploy` - an IAM user scoped to only
`lambda:UpdateFunctionCode`/`GetFunction` on this one function, stored
as the `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` GitHub Actions
secrets (repo Settings → Secrets and variables → Actions). Nothing
manual required for normal changes.

CI's `deploy` job only updates the function's **code**
(`aws lambda update-function-code`) - it never touches environment
variables or IAM policy. Adding `COGNITO_USER_POOL_ID`/`COGNITO_REGION`/
`COGNITO_APP_CLIENT_ID` (or changing `API_KEY`, for that matter) needs a
one-time manual step:
```bash
aws lambda update-function-configuration --function-name archive-api \
  --environment "Variables={S3_BUCKET=...,DYNAMODB_TABLE=...,API_KEY=...,COGNITO_USER_POOL_ID=...,COGNITO_REGION=us-east-1}"
```

To deploy without going through GitHub (e.g. testing a change before
opening a PR): `make deploy-lambda`, using whatever AWS credentials
`aws configure` has set up locally.

Testing an `AWS_IAM`-protected Function URL with `curl` alone doesn't
work, since `curl` can't produce an AWS SigV4 signature - use something
that can sign requests with your AWS credentials, e.g.
[`awscurl`](https://github.com/okigan/awscurl) (`awscurl --service lambda
--region <region> <url>`).
