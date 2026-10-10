# Production deployment

Secrets must be entered directly in Render, Cloudflare Pages, EAS, or GitHub. Never add them to a repository or send them through chat.

## Provisioning order

1. Create Neon PostgreSQL and copy the pooled TLS connection string to Render as `DATABASE_URL`.
2. Create a private Supabase Storage bucket, enable S3 access, and generate server-side access keys. Set `STORAGE_ENDPOINT`, `STORAGE_REGION`, `STORAGE_BUCKET`, `STORAGE_PUBLIC_BUCKET` (the same private bucket), `STORAGE_ACCESS_KEY_ID`, and `STORAGE_SECRET_ACCESS_KEY` in Render. The endpoint is `https://<project-ref>.storage.supabase.co/storage/v1/s3`. Free-plan EPUB uploads are limited to 50 MiB.
3. Create a Cloudinary product environment. Set `CLOUDINARY_CLOUD_NAME`, `CLOUDINARY_API_KEY`, and `CLOUDINARY_API_SECRET` in Render.
4. Verify the sending domain in Resend. Set `RESEND_API_KEY` and `EMAIL_FROM` in Render.
5. Keep `PLAY_INTEGRITY_REQUIRED=false` for the initial backend smoke test. Before the Play Store release, create a Google Cloud service account allowed to decode Play Integrity verdicts, set `GOOGLE_CLOUD_PROJECT_NUMBER` and base64-encoded `GOOGLE_SERVICE_ACCOUNT_JSON_BASE64` in Render, then enable enforcement.
6. Generate a 64-byte Ed25519 private key, a 32-byte AES content key, and a random internal job token. Store them as `ACCESS_TOKEN_PRIVATE_KEY_BASE64`, `CONTENT_KEY_ENCRYPTION_KEY_BASE64`, and `INTERNAL_JOB_TOKEN`.
7. Set `ADMIN_ORIGIN` to the final Cloudflare Pages origin. If the backend must deploy first, use `https://bootstrap.invalid` temporarily, deploy Pages with the resulting Render origin, then immediately replace `ADMIN_ORIGIN` with the exact `https://<project>.pages.dev` origin and redeploy. On Render, `PUBLIC_BASE_URL` is derived securely from `RENDER_EXTERNAL_HOSTNAME`; set it explicitly only when attaching a custom API domain.
8. Deploy the Render Blueprint. Confirm `/health/live`, `/health/ready`, `/health/uptime`, and `/version` before creating the Owner.
9. Run the one-time Owner seed command against production with `OWNER_*` values supplied only to that process.
10. In Cloudflare Pages set `VITE_API_URL=/v1` at build time and `API_ORIGIN` to the Render origin for the Pages Function.
11. In GitHub set backend repository secrets `RENDER_DEPLOY_HOOK`, `API_ORIGIN`, `INTERNAL_JOB_TOKEN`, `DATABASE_URL`, `STORAGE_ENDPOINT`, `STORAGE_REGION`, `STORAGE_BUCKET`, `STORAGE_ACCESS_KEY_ID`, `STORAGE_SECRET_ACCESS_KEY`, and `BACKUP_PASSPHRASE`.
12. Configure the EAS project and Android FCM V1 credentials. Set Android build values `EXPO_PUBLIC_API_URL=https://<admin-pages-domain>/v1`, `EXPO_PUBLIC_EAS_PROJECT_ID`, and `EXPO_PUBLIC_GOOGLE_CLOUD_PROJECT_NUMBER`.
13. Configure UptimeRobot using [uptime-monitoring.md](uptime-monitoring.md). Use the five-minute keyword monitor; do not use the database-backed readiness endpoint for keep-alive traffic.

## Acceptance gate

- Upload one cover image and one EPUB in the Owner dashboard.
- Observe the EPUB job transition from `PENDING` to `COMPLETED`.
- Publish the book and confirm it appears in Android after refresh.
- Create a reader through email OTP, claim the book, read online, download it, reopen offline, save progress, and submit feedback.
- Send an Owner notification and confirm both the inbox item and push delivery.
- Confirm `/v1/checkouts` returns `PAYMENT_NOT_CONFIGURED` and that no transaction is created.
- Run a backup workflow manually and complete a restore validation before launch.
