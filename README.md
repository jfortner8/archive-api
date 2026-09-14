# archive-api

Go backend for archive-ui. Stores item metadata in DynamoDB and files
(photos, PDFs, etc.) in S3, accessed via presigned URLs so files never
pass through this service's own request handling.

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (for local dev dependencies only)

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

- `POST /items` — create an item (`{"type": "photo", "title": "..."}`)
- `GET /items` — list items
- `GET /items/{id}` — get one item
- `POST /items/{id}/upload-url` — get a presigned S3 URL to upload a file
  (`{"role": "front", "filename": "scan.jpg", "contentType": "image/jpeg"}`);
  the frontend `PUT`s the file bytes directly to the returned URL
- `POST /items/{id}/files` — record that an upload finished, attaching it
  to the item (`{"role": "front", "key": "...", "contentType": "...", "sizeBytes": 123}`)
- `GET /items/{id}/files/{role}/download-url` — get a presigned URL to
  download a specific file (e.g. `role=front`, `role=back`)

## Setting up real AWS (once you're ready to deploy)

1. **S3 bucket** — create one (e.g. `archive-prod`) in the console or CLI.
   No public access needed; the API generates presigned URLs.
2. **DynamoDB table** — partition key `id` (String), on-demand ("pay per
   request") billing mode. Matches what `make dev-setup` creates locally.
3. **IAM role** — create a role with a policy scoped to just that bucket
   and table (not full S3/DynamoDB access):
   - `s3:PutObject`, `s3:GetObject` on `arn:aws:s3:::archive-prod/*`
   - `dynamodb:GetItem`, `PutItem`, `Scan` on the table's ARN
4. Attach that role to the Lightsail instance (Lightsail supports
   attaching IAM roles to instances) so the app never needs a hardcoded
   access key in production — the SDK picks up the role automatically,
   which is why `.env.example`'s `AWS_ACCESS_KEY_ID`/`SECRET` are marked
   local-only.

## Deploying to Lightsail

1. Create the cheapest Lightsail instance (Linux, e.g. Amazon Linux or
   Ubuntu), attach the IAM role from above.
2. Build a Linux binary locally: `make build-linux` → produces `bin/api-linux`.
3. Copy it and a production `.env` (real bucket/table names, no
   `S3_ENDPOINT`/`DYNAMODB_ENDPOINT`/AWS keys) to the instance, e.g.:
   ```bash
   scp bin/api-linux ubuntu@<instance-ip>:/tmp/api
   scp .env.prod ubuntu@<instance-ip>:/tmp/.env
   ```
4. On the instance, move them into place and install the systemd unit:
   ```bash
   sudo mkdir -p /opt/archive-api
   sudo mv /tmp/api /opt/archive-api/api
   sudo mv /tmp/.env /opt/archive-api/.env
   sudo chmod +x /opt/archive-api/api
   sudo cp deploy/archive-api.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now archive-api
   ```
5. Open port 8080 (or whatever `PORT` you set) in the Lightsail
   networking tab, or put it behind Lightsail's built-in load
   balancer/HTTPS if you want a real domain and TLS later.

Redeploying after a code change is just repeating steps 2-3 plus
`sudo systemctl restart archive-api`.
