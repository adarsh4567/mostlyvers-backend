# MOSTLYVERS backend

Go modular monolith for the Android reader and Owner dashboard. Launch uses real, non-financial `FREE_LAUNCH` entitlements. Payment routes still fail closed with `503 PAYMENT_NOT_CONFIGURED`; the provider seam remains ready for Google Play Billing.

## Run locally

Requirements: Go 1.27 and Docker.

```bash
cp .env.example .env
docker compose up -d postgres minio create-buckets mailpit
set -a
source .env
set +a
go run ./cmd/migrate up
OWNER_EMAIL=owner@example.com OWNER_PASSWORD='replace-this-password' OWNER_NAME='Owner' OWNER_PHONE='+910000000000' go run ./cmd/seed
go run ./cmd/api
```

The API listens at `http://localhost:5001`, the local object-storage console at `http://localhost:9001`, and Mailpit at `http://localhost:8025`. Development OTP responses include a marked `developmentCode`; production sends OTP and recovery mail through Resend and never returns the code.

The full local stack can instead be started with `docker compose up --build`. Do not use the sample `.env` secrets in a deployed environment.

## Useful commands

```bash
make test
make test-race
make lint
make migrate
make seed # requires OWNER_* environment variables
```

The API contract is [openapi/openapi.yaml](openapi/openapi.yaml). Migrations are embedded into both the API binary and migration command. API startup applies pending migrations before accepting traffic.

## Security and data behavior

- Reader access JWTs last 15 minutes; opaque refresh tokens rotate on every use and replay revokes the family.
- Owner refresh credentials are Secure/HttpOnly cookies in production, with a separate CSRF token for mutations.
- Passwords use Argon2id. Tokens, EPUB data, payment proofs, and content keys are excluded from application logs.
- Play Integrity validation can be enforced with `PLAY_INTEGRITY_REQUIRED=true`. Render uses a base64-encoded Google service-account JSON with Play Integrity API access.
- EPUB files are validated for traversal, decompression limits, remote/active content, manifest and spine structure. Sanitized reader resources remain private. Offline packages use AES-256-GCM and wrapped per-package keys.
- Account deletion immediately revokes sessions and a database job pseudonymizes retained financial/audit records while removing reader data.
- Admin uploads stop at the configured storage hard limit. Presigned upload parts expire after five minutes.

## Render deployment outline

1. Create Neon PostgreSQL and use its pooled TLS URL for `DATABASE_URL`.
2. Create one private Supabase Storage bucket, enable its S3 protocol, and generate server-side S3 access keys. EPUBs, protected resources, offline packages and backups stay there. The legacy `R2_*` variables remain supported, but production uses the provider-neutral `STORAGE_*` names.
3. Create a Cloudinary product environment for cover, author and profile images.
4. Create a Render Blueprint from `render.yaml`, then enter every `sync: false` value in Render. Render supplies `PORT` automatically.
5. Configure GitHub `API_ORIGIN` and `INTERNAL_JOB_TOKEN`. The hourly workflow wakes the free service and drains durable jobs; an in-process worker handles jobs while awake.
6. Set Cloudflare Pages `API_ORIGIN` to the Render origin and `VITE_API_URL=/v1`, preserving same-origin admin refresh cookies.

No PDF conversion exists: admin book content accepts EPUB only. Free Render instances sleep when idle, so the first request after inactivity can be slow.
